package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Per-artist outcomes of reconcile-all-apply.
const (
	OutcomeApplied      = "applied"
	OutcomeRolledBack   = "rolled_back"
	OutcomeFailed       = "failed"
	OutcomeNotAttempted = "not_attempted"
)

// Fixed, path-free reasons of the per-artist outcomes.
const (
	reasonRolledBackOther   = "Applied, then rolled back because another artist failed."
	reasonRolledBackCommit  = "Applied, then rolled back because the changes could not be committed."
	reasonRolledBackCancel  = "Applied, then rolled back because the job was cancelled."
	reasonCancelledCurrent  = "Rolled back: the job was cancelled while this artist was being applied."
	reasonFailed            = "The database update for this artist failed; it was rolled back."
	reasonNotAttempted      = "Not attempted: the run stopped before this artist."
	reasonNotStarted        = "Not attempted: the run stopped before any artist was applied (no backup or no change was made)."
	reasonNotStartedCancel  = "Not attempted: the job was cancelled before any artist was applied."
	reasonAwaitingResync    = "A sync of this artist is needed to finish."
	rolledBackProvenanceMsg = " The update status was reset, so the database shows as needing a check."
)

var reconcileAllColumns = []string{"artist", "outcome", "changes", "reason"}

type reconcileAllAction struct {
	deps   ReconcileDeps
	dryRun bool
}

// NewReconcileAllDryRun returns reconcile-all-dry-run: it computes the
// reconciliation plan of every catalog artist without locking or writing
// anything, and lists the artists whose plan would change something.
func NewReconcileAllDryRun(d ReconcileDeps) Action { return &reconcileAllAction{deps: d, dryRun: true} }

// NewReconcileAllApply returns reconcile-all-apply. Its preview fingerprint
// is the bulk plan checksum (the plan checksums of the artists to reconcile
// and the catalog digest) and it needs the typed phrase "reconcile all". The
// job takes the cross-process database lock, migrates the database,
// recomputes every plan and refuses a fingerprint other than the previewed
// one. It then lets the use case take one backup and apply every artist in
// one transaction: all or nothing. A failure or a cancel rolls every artist
// back, and the report lists each artist's outcome.
func NewReconcileAllApply(d ReconcileDeps) Action { return &reconcileAllAction{deps: d} }

func (a *reconcileAllAction) Name() string {
	if a.dryRun {
		return "reconcile-all-dry-run"
	}
	return "reconcile-all-apply"
}

func (a *reconcileAllAction) Describe() Description {
	if a.dryRun {
		return Description{Title: "Reconcile all artists (dry run)"}
	}
	return Description{Title: "Reconcile all artists", Destructive: true}
}

func (a *reconcileAllAction) Available() (bool, string) {
	if !a.deps.DatabaseExists() {
		return false, noDatabaseReason
	}
	return true, ""
}

func (a *reconcileAllAction) Validate(p Params) error { return RejectUnknown(p) }

func (a *reconcileAllAction) plan(ctx context.Context) (artists.BulkPlan, error) {
	if !a.deps.DatabaseExists() {
		return artists.BulkPlan{}, ErrNoDatabase
	}
	return a.deps.Port.PlanAll(ctx)
}

func (a *reconcileAllAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	plan, err := a.plan(ctx)
	if err != nil {
		return Preview{}, err
	}
	changes := plan.Changes()
	writes := "none (dry run)"
	if !a.dryRun {
		writes = "database, every artist in one transaction; one backup and plan manifest are written to " +
			a.deps.BackupName + " first"
	}
	pv := Preview{
		Title: a.Describe().Title,
		Scope: []ScopeLine{
			{Label: "Artists to reconcile", Value: artistList(changes)},
			{Label: "Already reconciled", Value: plural(len(plan.Reports)-len(changes), "artist")},
			{Label: "Associations", Value: bulkAssociationsLine(changes, a.dryRun)},
			{Label: "Sync state", Value: bulkSyncStateLine(changes)},
			{Label: "Plan fingerprint", Value: plan.Checksum},
			{Label: "Writes", Value: writes},
		},
		Fingerprint: plan.Checksum,
	}
	if len(changes) == 0 {
		pv.Warnings = append(pv.Warnings, "Nothing to reconcile: every artist is already reconciled.")
	}
	if !a.dryRun {
		pv.Destructive = true
		pv.RequiresTyped = "reconcile all"
		pv.Warnings = append(pv.Warnings,
			"All or nothing: if any artist fails or the job is cancelled, every artist is rolled back.",
			"Unlinked associations can only be restored from the command line (artists reconcile --restore).")
		if resync := resyncSlugs(changes); len(resync) > 0 {
			pv.Warnings = append(pv.Warnings, "A sync of "+strings.Join(resync, ", ")+" is needed afterwards to finish the reconciliation.")
		}
	}
	return pv, nil
}

// Spec plans now. For apply it binds the job to the verified preview
// fingerprint (fails closed without one), refuses a plan that already
// differs, and re-checks the fingerprint inside the job once the lock is
// held.
func (a *reconcileAllAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	plan, err := a.plan(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	spec := jobs.Spec{Kind: a.Name()}
	if a.dryRun {
		spec.Run = a.runDry
		return spec, nil
	}
	checksum, err := verifiedFingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	if plan.Checksum != checksum {
		return jobs.Spec{}, ErrStalePreview
	}
	spec.Resources = []string{ResourceDatabase}
	spec.Run = func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
		return a.runApply(ctx, r, checksum)
	}
	return spec, nil
}

func (a *reconcileAllAction) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Safe(err)
}

func (a *reconcileAllAction) runDry(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
	r.Step("planning")
	plan, err := a.plan(ctx)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	changes := plan.Changes()
	rep := &jobs.Report{Name: a.Name(), Columns: reconcileColumns}
	for _, c := range changes {
		countReconcile(r, c)
		rep.Rows = append(rep.Rows, reconcileReport(a.Name(), c).Rows...)
		rep.Rows = append(rep.Rows, []string{"summary", c.Slug, "status " + c.Status, "", associationsLine(c, true)})
	}
	SortRows(rep)
	clean := len(plan.Reports) - len(changes)
	var summary string
	if len(changes) == 0 {
		summary = fmt.Sprintf("Dry run: nothing to reconcile; %s already reconciled.", plural(clean, "artist"))
	} else {
		summary = fmt.Sprintf("Dry run: %s need reconciliation (%s); %s already reconciled. %s; %s.",
			plural(len(changes), "artist"), strings.Join(slugsOf(changes), ", "), plural(clean, "artist"),
			bulkAssociationsLine(changes, true), bulkSyncStateLine(changes))
	}
	summary += " Plan fingerprint " + plan.Checksum + "."
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: rep}}, nil
}

func (a *reconcileAllAction) runApply(ctx context.Context, r jobs.Reporter, checksum string) (jobs.Outcome, error) {
	r.Step("locking database")
	release, err := a.deps.Locker.TryLock()
	if err != nil {
		return jobs.Outcome{}, Safe(err)
	}
	defer release()

	if !a.deps.DatabaseExists() {
		return jobs.Outcome{}, ErrNoDatabase
	}
	r.Step("migrating database")
	if err := a.deps.Port.Migrate(ctx); err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return jobs.Outcome{}, err
	}

	r.Step("checking plans")
	plan, err := a.plan(ctx)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	if plan.Checksum != checksum {
		return jobs.Outcome{}, Safe(ErrStalePreview)
	}
	// Last point where a cancel leaves everything untouched.
	if err := ctx.Err(); err != nil {
		return jobs.Outcome{}, err
	}
	changes := plan.Changes()
	if len(changes) == 0 {
		// Nothing to apply, so no backup: the plans still vouch for the
		// artists' Spotify state, as a clean reconcile-apply does.
		summary := "Nothing to reconcile: every artist is already reconciled."
		if err := a.recordProvenance(ctx, plan.Reports); err != nil {
			summary += provenanceWarning
		}
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary,
			Report: &jobs.Report{Name: a.Name(), Columns: reconcileAllColumns}}}, nil
	}

	r.Step("backing up and applying")
	reports, err := a.deps.Port.ApplyAll(ctx, checksum)
	if err != nil {
		if errors.Is(err, artists.ErrBulkPlanChanged) {
			return jobs.Outcome{}, Safe(ErrStalePreview)
		}
		return a.rolledBack(ctx, changes, err)
	}

	rep := &jobs.Report{Name: a.Name(), Columns: reconcileAllColumns}
	for _, c := range reports {
		countReconcile(r, c)
		reason := ""
		if c.ResyncRequired {
			reason = reasonAwaitingResync
		}
		rep.Rows = append(rep.Rows, []string{c.Slug, OutcomeApplied, bulkChanges(c), reason})
	}
	SortRows(rep)
	summary := fmt.Sprintf("Reconciled %s (%s): %s; %s.",
		plural(len(reports), "artist"), strings.Join(slugsOf(reports), ", "),
		bulkAssociationsLine(reports, false), bulkSyncStateLine(reports))
	if len(reports) > 0 && reports[0].Backup != nil {
		summary += fmt.Sprintf(" Backup %s written to %s; restore an artist with artists reconcile --restore %s.",
			filepath.Base(reports[0].Backup.Path), a.deps.BackupName, reports[0].RetiredAt)
	}
	if resync := resyncSlugs(reports); len(resync) > 0 {
		summary += " Run a sync of " + strings.Join(resync, ", ") + " to finish."
	}
	if err := a.recordProvenance(ctx, reports); err != nil {
		summary += provenanceWarning
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: rep}}, nil
}

// rolledBack reports a failed or cancelled bulk apply: the use case rolled
// every artist back, so none is applied. The job ends failed (or cancelled)
// with one outcome per artist.
func (a *reconcileAllAction) rolledBack(ctx context.Context, changes []artists.ReconcileReport, err error) (jobs.Outcome, error) {
	cancelled := ctx.Err() != nil || errors.Is(err, context.Canceled)
	var be *artists.BulkApplyError
	attempted := errors.As(err, &be)
	failedAt := len(changes)
	if attempted && be.Slug != "" {
		for i, c := range changes {
			if c.Slug == be.Slug {
				failedAt = i
			}
		}
	}

	rep := &jobs.Report{Name: a.Name(), Columns: reconcileAllColumns}
	for i, c := range changes {
		outcome, reason := OutcomeNotAttempted, reasonNotStarted
		switch {
		case !attempted && cancelled:
			reason = reasonNotStartedCancel
		case !attempted:
		case be.Slug == "":
			outcome, reason = OutcomeRolledBack, reasonRolledBackCommit
		case i < failedAt && cancelled:
			outcome, reason = OutcomeRolledBack, reasonRolledBackCancel
		case i < failedAt:
			outcome, reason = OutcomeRolledBack, reasonRolledBackOther
		case i == failedAt && cancelled:
			outcome, reason = OutcomeRolledBack, reasonCancelledCurrent
		case i == failedAt:
			outcome, reason = OutcomeFailed, reasonFailed
		default:
			reason = reasonNotAttempted
		}
		rep.Rows = append(rep.Rows, []string{c.Slug, outcome, bulkChanges(c), reason})
	}
	SortRows(rep)

	var summary string
	switch {
	case cancelled && attempted:
		summary = "Bulk reconcile was cancelled; every change was rolled back and the database is as it was before the run. No artist was reconciled."
	case cancelled:
		summary = "Bulk reconcile was cancelled before any artist was applied; the database is unchanged."
	case attempted && be.Slug == "":
		summary = "Bulk reconcile could not commit its changes; every change was rolled back and the database is as it was before the run. No artist was reconciled."
	case attempted:
		summary = "Bulk reconcile failed while applying " + be.Slug + "; every change was rolled back and the database is as it was before the run. No artist was reconciled."
	default:
		summary = "Bulk reconcile stopped before applying any artist; the database is unchanged."
	}
	if attempted {
		summary += rolledBackProvenanceMsg
	}
	out := jobs.Outcome{Result: jobs.Result{Summary: summary, Report: rep}}
	if cancelled {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		return out, context.Canceled
	}
	return out, &SafeError{Code: "reconcile_failed", Message: summary, cause: err}
}

// recordProvenance merges, from the catalog the run read, the Spotify state
// of every artist the run applied (or found already reconciled). Artists
// still awaiting their resync are not applied yet: their sync records them.
func (a *reconcileAllAction) recordProvenance(ctx context.Context, reports []artists.ReconcileReport) error {
	if a.deps.Provenance == nil {
		return nil
	}
	var runs []appliedRun
	for _, rep := range reports {
		if rep.ResyncRequired || rep.Catalog.Digest == "" {
			continue
		}
		runs = append(runs, appliedRun{slug: rep.Slug, used: rep.Catalog})
	}
	return mergeApplied(ctx, a.deps.Provenance, artists.StageReconcile, runs, artists.MergeReconciled)
}

func slugsOf(reports []artists.ReconcileReport) []string {
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		out = append(out, r.Slug)
	}
	return out
}

func artistList(reports []artists.ReconcileReport) string {
	if len(reports) == 0 {
		return "none"
	}
	return plural(len(reports), "artist") + ": " + strings.Join(slugsOf(reports), ", ")
}

func resyncSlugs(reports []artists.ReconcileReport) []string {
	var out []string
	for _, r := range reports {
		if r.ResyncRequired {
			out = append(out, r.Slug)
		}
	}
	return out
}

func bulkAssociationsLine(reports []artists.ReconcileReport, dryRun bool) string {
	var total artists.ReconcileReport
	for _, r := range reports {
		total.Orphan.Albums += r.Orphan.Albums
		total.Orphan.Tracks += r.Orphan.Tracks
		total.SharedOrphans.Albums += r.SharedOrphans.Albums
		total.SharedOrphans.Tracks += r.SharedOrphans.Tracks
		total.Supported.Albums += r.Supported.Albums + r.Unverifiable.Albums
		total.Supported.Tracks += r.Supported.Tracks + r.Unverifiable.Tracks
	}
	return associationsLine(total, dryRun)
}

func bulkSyncStateLine(reports []artists.ReconcileReport) string {
	var checkpoints, caches int
	for _, r := range reports {
		checkpoints += len(r.StaleCheckpoints)
		if r.MetadataStale {
			caches++
		}
	}
	return fmt.Sprintf("%s invalidated, %s invalidated, %s required",
		plural(checkpoints, "sync checkpoint"), plural(caches, "metadata cache"),
		plural(len(resyncSlugs(reports)), "replacement sync"))
}

// bulkChanges summarises one artist's plan for its outcome row.
func bulkChanges(r artists.ReconcileReport) string {
	line := "unlink " + countsPhrase(r.Orphan) + ", invalidate " + plural(len(r.StaleCheckpoints), "sync checkpoint")
	if r.MetadataStale {
		line += ", invalidate the metadata cache"
	}
	if r.ResyncRequired {
		line += ", replacement sync required"
	}
	return line
}
