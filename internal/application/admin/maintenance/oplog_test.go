package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

type memLog struct {
	mu      sync.Mutex
	entries []OperationEntry
	err     error
	panics  bool
}

func (l *memLog) Append(_ context.Context, e OperationEntry) error {
	if l.panics {
		panic("boom")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.entries = append(l.entries, e)
	return nil
}

func (l *memLog) Recent(context.Context, int) ([]OperationEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]OperationEntry(nil), l.entries...), nil
}

func (l *memLog) snapshot() []OperationEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]OperationEntry(nil), l.entries...)
}

func logService(t *testing.T, l OperationLog, onErr func(error), a *fakeAction) (*Service, *jobs.Manager) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	var n atomic.Int32
	var s *Service
	m := jobs.NewManager(&memStore{}, time.Now, func() string { return fmt.Sprintf("job-%d", n.Add(1)) },
		jobs.WithOnFinish(func(j jobs.Job) { s.FinishHook()(j) }))
	s = NewService(m, []Action{a}, []byte("k"), clk.now, WithOperationLog(l), WithOperationLogErrorHandler(onErr))
	return s, m
}

func runFake(t *testing.T, s *Service, m *jobs.Manager, p Params) jobs.Job {
	t.Helper()
	_, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Run(context.Background(), "fake", p, tok, "")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	return j
}

func TestEntryAppendedOnConfirmAndFinish(t *testing.T) {
	l := &memLog{}
	a := newFake("fake")
	s, m := logService(t, l, nil, a)
	j := runFake(t, s, m, Params{"scope": "all"})
	got := l.snapshot()
	if len(got) != 2 {
		t.Fatalf("entries %+v", got)
	}
	if got[0].Kind != "confirmed" || got[0].Action != "fake" || got[0].JobID != j.ID || got[0].At.IsZero() || got[0].Summary == "" {
		t.Fatalf("confirmed %+v", got[0])
	}
	if got[1].Kind != "finished" || got[1].JobID != j.ID || got[1].Outcome != string(jobs.StatusSucceeded) || got[1].Action != "fake" {
		t.Fatalf("finished %+v", got[1])
	}
}

func TestEntrySummaryIsSanitised(t *testing.T) {
	in := "failed reading /Users/me/secret/file.yaml and C:\\data\\x then foo.go:42 ok token=abc"
	out := SanitizeSummary(in)
	for _, bad := range []string{"/Users", "\\data", ".go:42", "token=abc"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q leaked in %q", bad, out)
		}
	}
	if !strings.Contains(out, "failed reading") || !strings.Contains(out, "ok") {
		t.Fatalf("over-redacted: %q", out)
	}
	if got := SanitizeSummary(strings.Repeat("a", 500)); len([]rune(got)) != 200 {
		t.Fatalf("len %d", len([]rune(got)))
	}
	if SanitizeSummary("a\nb\x00c") != "a b c" {
		t.Fatalf("control chars: %q", SanitizeSummary("a\nb\x00c"))
	}
}

func TestNoPathsInEntries(t *testing.T) {
	l := &memLog{}
	a := newFake("fake")
	a.run = func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: "wrote /var/data/out.csv"}}, nil
	}
	s, m := logService(t, l, nil, a)
	runFake(t, s, m, Params{"scope": "/etc/passwd"})
	for _, e := range l.snapshot() {
		if strings.ContainsAny(e.Summary+e.Action+e.Outcome, `/\`) || strings.Contains(e.Summary, "passwd") {
			t.Fatalf("leak: %+v", e)
		}
	}
}

func TestLogFailureDoesNotFailRun(t *testing.T) {
	for _, l := range []*memLog{{err: errors.New("disk full /x")}, {panics: true}} {
		var reported atomic.Int32
		s, m := logService(t, l, func(error) { reported.Add(1) }, newFake("fake"))
		j := runFake(t, s, m, Params{"scope": "all"})
		got, _ := m.Get(j.ID)
		if got.Status != jobs.StatusSucceeded || reported.Load() == 0 {
			t.Fatalf("status %v reported %d", got.Status, reported.Load())
		}
	}
	// nil handler and nil log are fine too.
	s, m := logService(t, &memLog{err: errors.New("x")}, nil, newFake("fake"))
	runFake(t, s, m, Params{"scope": "all"})
	s2 := NewService(nil, nil, nil, time.Now)
	s2.FinishHook()(jobs.Job{ID: "x", Status: jobs.StatusFailed})
}

func TestSanitizeSummaryBearerAndBareFilenames(t *testing.T) {
	out := SanitizeSummary("auth Bearer abc123 failed; token: xyz789 in catalog.db and a.yaml")
	for _, bad := range []string{"abc123", "xyz789", "catalog.db", "a.yaml"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q leaked in %q", bad, out)
		}
	}
	if !strings.Contains(out, "auth") || !strings.Contains(out, "failed;") {
		t.Fatalf("over-redacted: %q", out)
	}
}

func TestFinishedNeverPrecedesConfirmed(t *testing.T) {
	for i := 0; i < 200; i++ {
		l := &memLog{}
		s, m := logService(t, l, nil, newFake("fake"))
		j := runFake(t, s, m, Params{"scope": "all"})
		got := l.snapshot()
		if len(got) != 2 || got[0].Kind != EntryConfirmed || got[1].Kind != EntryFinished ||
			got[0].JobID != j.ID || got[1].JobID != j.ID || got[0].At.After(got[1].At) {
			t.Fatalf("iteration %d: %+v", i, got)
		}
	}
}
