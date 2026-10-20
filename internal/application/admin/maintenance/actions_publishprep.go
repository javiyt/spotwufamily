package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/catalogaudit"
	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// ResourceExport is the generated site export (data, search index, pages).
const ResourceExport = "export-artifacts"

// noDatabaseReason is shown when an action needs the catalog database.
const noDatabaseReason = "No database found; run the init workflow first"

// ErrNoDatabase is returned when the catalog database does not exist.
var ErrNoDatabase = &SafeError{Code: "unavailable", Message: noDatabaseReason + "."}

// ErrDatabaseInvalid is wrapped by VerifyPort (and SnapshotPort) when the
// database fails an integrity, foreign key or migration check.
var ErrDatabaseInvalid = errors.New("database failed verification")

// Snapshot file states reported by VerifyPort.
const (
	SnapshotNotChecked = ""
	SnapshotFresh      = "fresh"
	SnapshotStale      = "stale"
	SnapshotAbsent     = "absent"
	SnapshotUnreadable = "unreadable"
)

// VerifyResult is the outcome of a read-only database verification.
type VerifyResult struct {
	Migrations int
	// Checks are the checks that passed, in the order they ran.
	Checks []string
	// FailedCheck names the check that failed, if any.
	FailedCheck string
	// Snapshot is the snapshot file state (only when it was checked).
	Snapshot string
}

// VerifyPort verifies the database read-only (integrity, foreign keys,
// migrations). With checkSnapshot it also compares an existing snapshot
// file with the database, as the CLI's db verify does. A failed check
// returns an error wrapping ErrDatabaseInvalid with the partial result.
type VerifyPort interface {
	Verify(ctx context.Context, checkSnapshot bool) (VerifyResult, error)
}

// SnapshotWrite reports what a snapshot write did.
type SnapshotWrite struct {
	// Changed is false when the snapshot already matched the database and
	// nothing was written.
	Changed bool
	// PreviousKept is true when a replaced snapshot was kept as <name>.prev.
	PreviousKept bool
}

// SnapshotPort verifies the database and writes its snapshot atomically
// (temporary file, then rename). Before replacing a different snapshot it
// copies the current one to <snapshot>.prev, replacing any older .prev (one
// generation is kept). An unchanged snapshot is not rewritten and .prev is
// left alone. Cancellation before the rename leaves the old snapshot intact.
type SnapshotPort interface {
	Write(ctx context.Context) (SnapshotWrite, error)
}

// ExportPort verifies the database and runs the catalog export into w.
type ExportPort interface {
	Run(ctx context.Context, w catalogexport.Writer) (catalogexport.Report, error)
}

// StagedExport collects export writes without touching the published
// artifacts. Commit publishes them (each file by rename) and applies the
// prunes; Discard drops whatever was staged and is safe after Commit.
type StagedExport interface {
	catalogexport.Writer
	Commit() error
	Discard() error
}

// ExportStager starts a staged export.
type ExportStager interface {
	Stage() (StagedExport, error)
}

// RepoAuditPort runs the read-only snapshot and export audit.
type RepoAuditPort func(ctx context.Context) (catalogaudit.Report, error)

// PublishPrepDeps are the ports of the publish preparation actions: db-verify,
// db-snapshot, export, audit and refresh-export.
type PublishPrepDeps struct {
	DatabaseExists func() bool
	// Digest covers the database, the snapshot and the export artifacts.
	Digest   CatalogDigest
	Verifier VerifyPort
	Snapshot SnapshotPort
	Export   ExportPort
	Stager   ExportStager
	Audit    RepoAuditPort
	Locker   Locker
	// Display names (never absolute paths) used in previews and reports.
	DatabaseName string
	SnapshotName string
	ExportNames  []string
}

type prepStep string

const (
	stepVerify   prepStep = "verify"
	stepSnapshot prepStep = "snapshot"
	stepExport   prepStep = "export"
	stepAudit    prepStep = "audit"
)

var prepColumns = []string{"check", "status", "detail"}

type prepAction struct {
	deps  PublishPrepDeps
	name  string
	title string
	steps []prepStep
}

// NewPublishPrepActions returns db-verify, db-snapshot, export, audit and
// refresh-export.
//
// db-verify and audit are read-only and hold no lock. db-snapshot, export and
// refresh-export take the cross-process database lock (so a sync cannot
// change the database while it is read) and re-check the preview fingerprint
// once they hold it. None of them modifies the database, so none takes a
// database backup; instead the snapshot keeps its replaced version as .prev
// and the export is fully staged before anything is published, so a cancel
// or failure leaves the previous artifacts byte-identical.
func NewPublishPrepActions(d PublishPrepDeps) []Action {
	return []Action{
		&prepAction{deps: d, name: "db-verify", title: "Verify database", steps: []prepStep{stepVerify}},
		&prepAction{deps: d, name: "db-snapshot", title: "Write database snapshot", steps: []prepStep{stepSnapshot}},
		&prepAction{deps: d, name: "export", title: "Export catalog for the site", steps: []prepStep{stepExport}},
		&prepAction{deps: d, name: "audit", title: "Audit snapshot and export", steps: []prepStep{stepAudit}},
		&prepAction{deps: d, name: "refresh-export", title: "Verify, snapshot, export and audit",
			steps: []prepStep{stepVerify, stepSnapshot, stepExport, stepAudit}},
	}
}

func (a *prepAction) Name() string { return a.name }

func (a *prepAction) Describe() Description { return Description{Title: a.title} }

func (a *prepAction) Available() (bool, string) {
	if !a.deps.DatabaseExists() {
		return false, noDatabaseReason
	}
	return true, ""
}

func (a *prepAction) Validate(p Params) error { return RejectUnknown(p) }

func (a *prepAction) composite() bool { return len(a.steps) > 1 }

// writes reports whether the action writes artifacts and so takes the lock.
func (a *prepAction) writes() bool {
	for _, s := range a.steps {
		if s == stepSnapshot || s == stepExport {
			return true
		}
	}
	return false
}

func (a *prepAction) fingerprint(ctx context.Context) (string, error) {
	if !a.deps.DatabaseExists() {
		return "", ErrNoDatabase
	}
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return "", err
	}
	return fingerprint(a.name, digest), nil
}

func (a *prepAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	fp, err := a.fingerprint(ctx)
	if err != nil {
		return Preview{}, err
	}
	snapshot := a.deps.SnapshotName
	pv := Preview{Title: a.title, Fingerprint: fp}
	var steps, writes []string
	for _, s := range a.steps {
		switch s {
		case stepVerify:
			steps = append(steps, "verify the database (integrity, foreign keys, migrations)")
		case stepSnapshot:
			steps = append(steps, "write the snapshot")
			writes = append(writes, snapshot+" (only if it changed; the replaced version is kept as "+
				snapshot+".prev, replacing any older .prev)")
		case stepExport:
			steps = append(steps, "export the catalog")
			writes = append(writes, strings.Join(a.deps.ExportNames, ", ")+
				" (staged first and published only once complete)")
			pv.Warnings = append(pv.Warnings, "Export files no longer produced by the catalog are removed.")
		case stepAudit:
			steps = append(steps, "audit the snapshot and the export")
		}
	}
	if a.name == "db-verify" {
		steps = append(steps, "compare "+snapshot+" with the database if it exists")
	}
	database := a.deps.DatabaseName + " (read only)"
	if a.writes() {
		database = a.deps.DatabaseName + " (read only; locked while running; not modified, so no backup is taken)"
	}
	if len(writes) == 0 {
		writes = []string{"none (read only)"}
	}
	pv.Scope = []ScopeLine{
		{Label: "Steps", Value: strings.Join(steps, "; ")},
		{Label: "Database", Value: database},
		{Label: "Writes", Value: strings.Join(writes, "; ")},
	}
	return pv, nil
}

func (a *prepAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	fp, err := a.fingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	var resources []string
	for _, s := range a.steps {
		switch s {
		case stepSnapshot:
			resources = appendResource(resources, ResourceDatabase)
		case stepExport:
			resources = appendResource(resources, ResourceDatabase, ResourceExport)
		}
	}
	return jobs.Spec{
		Kind:      a.name,
		Resources: resources,
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, fp)
		},
	}, nil
}

func appendResource(list []string, keys ...string) []string {
	for _, k := range keys {
		if !slices.Contains(list, k) {
			list = append(list, k)
		}
	}
	return list
}

// stepResult is one finished step's report rows and summary.
type stepResult struct {
	rows    [][]string
	summary string
	partial bool
}

func (a *prepAction) run(ctx context.Context, r jobs.Reporter, fp string) (jobs.Outcome, error) {
	if !a.deps.DatabaseExists() {
		return jobs.Outcome{}, ErrNoDatabase
	}
	if a.writes() {
		r.Step("locking database")
		release, err := a.deps.Locker.TryLock()
		if err != nil {
			return jobs.Outcome{}, Safe(err)
		}
		defer release()

		r.Step("checking preview")
		current, err := a.fingerprint(ctx)
		if err != nil {
			return jobs.Outcome{}, a.fail(ctx, err)
		}
		if current != fp {
			return jobs.Outcome{}, Safe(ErrStalePreview)
		}
	}

	var (
		rows      [][]string
		summaries []string
		partial   bool
		done      []string
	)
	for i, s := range a.steps {
		if err := ctx.Err(); err != nil {
			return jobs.Outcome{}, err
		}
		if a.composite() {
			r.Progress(i, len(a.steps))
		}
		res, err := a.runStep(ctx, r, s)
		if err != nil {
			if ctx.Err() != nil {
				return jobs.Outcome{}, ctx.Err()
			}
			return jobs.Outcome{}, a.stepFailure(s, done, err)
		}
		for _, row := range res.rows {
			if a.composite() && row[0] != string(s) {
				row[0] = string(s) + ": " + row[0]
			}
			rows = append(rows, row)
		}
		summaries = append(summaries, res.summary)
		partial = partial || res.partial
		done = append(done, string(s))
	}
	if a.composite() {
		r.Progress(len(a.steps), len(a.steps))
	}

	rep := &jobs.Report{Name: a.name, Columns: prepColumns, Rows: rows}
	SortRows(rep)
	status := jobs.StatusSucceeded
	if partial {
		status = jobs.StatusPartial
	}
	return jobs.Outcome{Status: status, Result: jobs.Result{Summary: strings.Join(summaries, " "), Report: rep}}, nil
}

// fail maps a step error: cancellation stays cancellation, everything else
// becomes a safe error.
func (a *prepAction) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Safe(err)
}

// stepFailure keeps the step's safe error and, in the composite, says which
// earlier steps had already completed (their results stay in place).
func (a *prepAction) stepFailure(s prepStep, done []string, err error) error {
	se := Safe(err).(*SafeError)
	if !a.composite() || len(done) == 0 {
		return se
	}
	title := strings.ToUpper(string(s[:1])) + string(s[1:])
	return &SafeError{
		Code:    se.Code,
		Message: fmt.Sprintf("%s failed after %s completed. %s", title, strings.Join(done, ", "), se.Message),
	}
}

func (a *prepAction) runStep(ctx context.Context, r jobs.Reporter, s prepStep) (stepResult, error) {
	switch s {
	case stepVerify:
		r.Step("verifying database")
		return a.verify(ctx)
	case stepSnapshot:
		r.Step("writing snapshot")
		return a.snapshot(ctx)
	case stepExport:
		r.Step("exporting")
		return a.export(ctx, r)
	default:
		r.Step("auditing")
		return a.audit(ctx)
	}
}

func (a *prepAction) verify(ctx context.Context) (stepResult, error) {
	// The composite rewrites the snapshot next, so only db-verify fails on a
	// stale snapshot (as the CLI's db verify does).
	checkSnapshot := !a.composite()
	res, err := a.deps.Verifier.Verify(ctx, checkSnapshot)
	if err != nil {
		if errors.Is(err, ErrDatabaseInvalid) {
			msg := "The database failed verification"
			if res.FailedCheck != "" {
				msg += " (" + res.FailedCheck + ")"
			}
			return stepResult{}, &SafeError{Code: "check_failed", Message: msg + "."}
		}
		return stepResult{}, err
	}
	var out stepResult
	for _, c := range res.Checks {
		detail := ""
		if c == "migrations" {
			detail = strconv.Itoa(res.Migrations) + " migrations applied"
		}
		out.rows = append(out.rows, []string{c, "ok", detail})
	}
	out.summary = fmt.Sprintf("Database verified (%d migrations; checks: %s).", res.Migrations, strings.Join(res.Checks, ", "))
	name := a.deps.SnapshotName
	switch res.Snapshot {
	case SnapshotFresh:
		out.rows = append(out.rows, []string{"snapshot", "ok", name + " matches the database"})
	case SnapshotStale:
		out.rows = append(out.rows, []string{"snapshot", "stale", name + " does not match the database; run db-snapshot"})
		out.summary += " The snapshot is out of date; run db-snapshot."
		out.partial = true
	case SnapshotUnreadable:
		out.rows = append(out.rows, []string{"snapshot", "unreadable", name + " could not be read; run db-snapshot"})
		out.summary += " The snapshot could not be read; run db-snapshot."
		out.partial = true
	case SnapshotAbsent:
		out.rows = append(out.rows, []string{"snapshot", "absent", name + " not found; not compared"})
	}
	return out, nil
}

func (a *prepAction) snapshot(ctx context.Context) (stepResult, error) {
	w, err := a.deps.Snapshot.Write(ctx)
	if err != nil {
		if errors.Is(err, ErrDatabaseInvalid) {
			return stepResult{}, &SafeError{Code: "check_failed", Message: "The database failed verification; the snapshot was not written."}
		}
		return stepResult{}, err
	}
	name := a.deps.SnapshotName
	switch {
	case !w.Changed:
		return stepResult{
			rows:    [][]string{{"snapshot", "unchanged", name + " already matches the database"}},
			summary: "Snapshot unchanged.",
		}, nil
	case w.PreviousKept:
		return stepResult{
			rows:    [][]string{{"snapshot", "written", name + " written; the replaced version is kept as " + name + ".prev"}},
			summary: "Snapshot written; the previous version is kept as " + name + ".prev.",
		}, nil
	}
	return stepResult{
		rows:    [][]string{{"snapshot", "written", name + " written (there was no previous snapshot)"}},
		summary: "Snapshot written.",
	}, nil
}

var errExportPublish = &SafeError{Code: "write_failed", Message: "The export could not be published completely; run export again."}

func (a *prepAction) export(ctx context.Context, r jobs.Reporter) (stepResult, error) {
	staged, err := a.deps.Stager.Stage()
	if err != nil {
		return stepResult{}, err
	}
	// Discard is a no-op after a successful Commit; otherwise it removes the
	// staged files so a cancel or failure leaves nothing behind.
	defer func() { _ = staged.Discard() }()

	rep, err := a.deps.Export.Run(ctx, staged)
	if ctx.Err() != nil {
		return stepResult{}, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrDatabaseInvalid) {
			return stepResult{}, &SafeError{Code: "check_failed", Message: "The database failed verification; nothing was exported."}
		}
		return stepResult{}, err
	}
	// Publishing is not cancellable: once everything is staged it is
	// published whole, so the artifacts are never a mix of two exports.
	r.Step("publishing export")
	if err := staged.Commit(); err != nil {
		return stepResult{}, errExportPublish
	}
	detail := fmt.Sprintf("artists=%d albums=%d tracks=%d files_written=%d files_kept=%d",
		rep.Artists, rep.Albums, rep.Tracks, rep.FilesWritten, rep.FilesKept)
	return stepResult{
		rows:    [][]string{{"export", "written", detail}},
		summary: "Export published (" + detail + ").",
	}, nil
}

func (a *prepAction) audit(ctx context.Context) (stepResult, error) {
	rep, err := a.deps.Audit(ctx)
	if err != nil {
		return stepResult{}, err
	}
	var out stepResult
	for _, c := range rep.Checks {
		out.rows = append(out.rows, []string{c.Name, c.Status, c.Detail})
	}
	if failed := rep.Failed(); failed > 0 {
		out.partial = true
		out.summary = fmt.Sprintf("Audit: %d of %d checks failed.", failed, len(rep.Checks))
	} else {
		out.summary = fmt.Sprintf("Audit: all %d checks passed.", len(rep.Checks))
	}
	return out, nil
}
