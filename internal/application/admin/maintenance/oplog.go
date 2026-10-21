package maintenance

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Operation log entry kinds.
const (
	EntryConfirmed = "confirmed"
	EntryFinished  = "finished"
)

// maxSummaryRunes bounds a logged summary.
const maxSummaryRunes = 200

// OperationEntry is one line of the local review/decision log. It never
// carries params, paths or raw errors.
type OperationEntry struct {
	At time.Time `json:"at"`
	// Kind is EntryConfirmed or EntryFinished.
	Kind   string `json:"kind"`
	Action string `json:"action"`
	JobID  string `json:"job_id"`
	// Outcome is the job status for finished entries, "submitted" for confirmed.
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
}

// OperationLog stores operation entries durably.
type OperationLog interface {
	Append(ctx context.Context, e OperationEntry) error
	// Recent returns up to n entries, newest first.
	Recent(ctx context.Context, n int) ([]OperationEntry, error)
}

// WithOperationLog makes the Service record confirmed and finished jobs.
func WithOperationLog(l OperationLog) Option {
	return func(s *Service) { s.oplog = l }
}

// WithOperationLogErrorHandler receives (and must not rely on) operation log
// failures; they never fail or block the operation.
func WithOperationLogErrorHandler(f func(error)) Option {
	return func(s *Service) { s.oplogErr = f }
}

var secretFile = regexp.MustCompile(`(?i)\.(db|yaml|yml|gz|sql|json|jsonl)[.,;:)]*$`)

var goFrame = regexp.MustCompile(`\.go:\d+`)

// SanitizeSummary is a safety net, not a guarantee: callers should still pass
// only already-sanitized text. It makes free text safe for the operation log: tokens that
// look like paths, stack frames or key=value secrets are replaced by
// "[redacted]", control characters become spaces and the result is truncated
// to 200 characters.
func SanitizeSummary(in string) string {
	in = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, in)
	fields := strings.Fields(in)
	for i, f := range fields {
		if strings.ContainsAny(f, `/\=`) || goFrame.MatchString(f) || secretFile.MatchString(f) {
			fields[i] = "[redacted]"
		}
	}
	// The value after "Bearer"/"token" is a credential, whatever its shape.
	for i := 0; i+1 < len(fields); i++ {
		if w := strings.ToLower(strings.TrimRight(fields[i], ":")); w == "bearer" || w == "token" {
			fields[i+1] = "[redacted]"
		}
	}
	out := strings.Join(fields, " ")
	if r := []rune(out); len(r) > maxSummaryRunes {
		out = string(r[:maxSummaryRunes])
	}
	return out
}

// record appends e, swallowing every failure (including panics).
func (s *Service) record(e OperationEntry) {
	if s.oplog == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.reportLogErr(errLogPanic)
		}
	}()
	e.Summary = SanitizeSummary(e.Summary)
	if err := s.oplog.Append(context.Background(), e); err != nil {
		s.reportLogErr(err)
	}
}

func (s *Service) reportLogErr(err error) {
	if s.oplogErr == nil {
		return
	}
	defer func() { _ = recover() }()
	s.oplogErr(err)
}

// FinishHook returns the callback for jobs.WithOnFinish that logs a finished
// entry. It is safe to call on a Service without an operation log.
func (s *Service) FinishHook() func(jobs.Job) {
	return func(j jobs.Job) {
		// A finished release job changes the package, the journal or the
		// pin: drop the cached check so the publish page does not show the
		// old state for up to availabilityTTL.
		if strings.HasPrefix(j.Kind, "release-") {
			s.invalidateRelease()
		}
		// Wait for Run to log the matching confirmed entry first. The hook
		// runs on the job goroutine, never inside Submit, so this cannot
		// deadlock.
		s.confirmMu.Lock()
		defer s.confirmMu.Unlock()
		summary := j.Message
		if j.Result != nil && j.Result.Summary != "" {
			summary = j.Result.Summary
		}
		s.record(OperationEntry{
			At: s.now(), Kind: EntryFinished, Action: j.Kind, JobID: j.ID,
			Outcome: string(j.Status), Summary: summary,
		})
	}
}

type logPanicError struct{}

func (logPanicError) Error() string { return "operation log panicked" }

var errLogPanic error = logPanicError{}
