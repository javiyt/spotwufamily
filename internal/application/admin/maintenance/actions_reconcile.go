package maintenance

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// ReconcilePort runs the artists reconcile use case for one catalog artist.
type ReconcilePort interface {
	// Plan computes the write-free plan (the use case's dry run) from the
	// catalog and the existing database, which it neither creates nor
	// migrates. It returns ErrNoDatabase when the database is missing.
	Plan(ctx context.Context, slug string) (artists.ReconcileReport, error)
	// Migrate brings the existing database schema up to date.
	Migrate(ctx context.Context) error
	// Apply runs the use case with Apply and ConfirmChecksum=checksum. The use
	// case recomputes the plan and refuses a different checksum; otherwise,
	// when there is something to change, it writes a VACUUM INTO backup plus
	// a plan manifest into the admin backup directory and then applies the
	// plan in one database transaction. A clean plan changes nothing and
	// takes no backup.
	Apply(ctx context.Context, slug, checksum string) (artists.ReconcileReport, error)
}

// ReconcileDeps are the ports of reconcile-dry-run and reconcile-apply.
type ReconcileDeps struct {
	Port           ReconcilePort
	DatabaseExists func() bool
	// Artists lists the slugs of every artist in the catalog.
	Artists func(ctx context.Context) ([]string, error)
	// Locker is the cross-process database lock; only apply takes it.
	Locker Locker
	// BackupName is the display name (never an absolute path) of the backup
	// directory.
	BackupName string
}

var reconcileColumns = []string{"kind", "artist", "item", "from", "to"}

type reconcileAction struct {
	deps   ReconcileDeps
	dryRun bool
}

// NewReconcileDryRun returns reconcile-dry-run: it computes the
// reconciliation plan of one artist without locking or writing anything.
func NewReconcileDryRun(d ReconcileDeps) Action { return &reconcileAction{deps: d, dryRun: true} }

// NewReconcileApply returns reconcile-apply. Its preview fingerprint is the
// plan checksum and it needs the typed phrase "reconcile <slug>". The job
// takes the cross-process database lock, migrates the database, recomputes
// the plan and refuses a checksum other than the previewed one, then lets the
// use case back up the database and apply the plan in one transaction.
// Restoring a previous application (the CLI's --restore) is not offered.
func NewReconcileApply(d ReconcileDeps) Action { return &reconcileAction{deps: d} }

func (a *reconcileAction) Name() string {
	if a.dryRun {
		return "reconcile-dry-run"
	}
	return "reconcile-apply"
}

func (a *reconcileAction) Describe() Description {
	if a.dryRun {
		return Description{Title: "Reconcile artist associations (dry run)"}
	}
	return Description{Title: "Reconcile artist associations", Destructive: true}
}

func (a *reconcileAction) Available() (bool, string) {
	if !a.deps.DatabaseExists() {
		return false, noDatabaseReason
	}
	return true, ""
}

func (a *reconcileAction) Validate(p Params) error {
	if err := RejectUnknown(p, "artist"); err != nil {
		return err
	}
	slug := p["artist"]
	if slug == "" {
		return &SafeError{Code: "validation", Message: "Artist is required."}
	}
	if !validSlug(slug) {
		return &SafeError{Code: "validation", Message: "Artist " + quoteParam(slug) + " is not a valid slug."}
	}
	return nil
}

// plan validates the artist against the catalog and computes its plan.
func (a *reconcileAction) plan(ctx context.Context, slug string) (artists.ReconcileReport, error) {
	if !a.deps.DatabaseExists() {
		return artists.ReconcileReport{}, ErrNoDatabase
	}
	slugs, err := a.deps.Artists(ctx)
	if err != nil {
		return artists.ReconcileReport{}, err
	}
	if !slices.Contains(slugs, slug) {
		return artists.ReconcileReport{}, &SafeError{Code: "validation",
			Message: "Artist " + quoteParam(slug) + " is not in the catalog."}
	}
	return a.deps.Port.Plan(ctx, slug)
}

func (a *reconcileAction) Preview(ctx context.Context, p Params) (Preview, error) {
	slug := p["artist"]
	rep, err := a.plan(ctx, slug)
	if err != nil {
		return Preview{}, err
	}
	identity := "unchanged"
	if rep.Identity.Drift {
		identity = fmt.Sprintf("%s → %s", idList(rep.Identity.Previous), idList(rep.Identity.Current))
	}
	writes := "none (dry run)"
	if !a.dryRun {
		writes = "database, in one transaction; a backup and plan manifest are written to " +
			a.deps.BackupName + " first"
	}
	pv := Preview{
		Title: a.Describe().Title,
		Scope: []ScopeLine{
			{Label: "Artist", Value: slug},
			{Label: "Status", Value: rep.Status},
			{Label: "Spotify profiles", Value: identity},
			{Label: "Associations", Value: associationsLine(rep, a.dryRun)},
			{Label: "Sync state", Value: syncStateLine(rep)},
			{Label: "Plan checksum", Value: rep.PlanChecksum},
			{Label: "Writes", Value: writes},
		},
		Fingerprint: rep.PlanChecksum,
	}
	if !rep.NeedsChanges() {
		pv.Warnings = append(pv.Warnings, "Nothing to apply: the artist is already reconciled.")
	}
	if !a.dryRun {
		pv.Destructive = true
		pv.RequiresTyped = "reconcile " + slug
		pv.Warnings = append(pv.Warnings,
			"Unlinked associations can only be restored from the command line (artists reconcile --restore).")
		if rep.ResyncRequired {
			pv.Warnings = append(pv.Warnings, "A sync of this artist is needed afterwards to finish the reconciliation.")
		}
	}
	return pv, nil
}

// Spec plans now (validating the artist). For apply it binds the job to the
// verified preview fingerprint (fails closed without one), refuses a plan
// that already differs, and re-checks the checksum inside the job once the
// lock is held.
func (a *reconcileAction) Spec(ctx context.Context, p Params) (jobs.Spec, error) {
	slug := p["artist"]
	rep, err := a.plan(ctx, slug)
	if err != nil {
		return jobs.Spec{}, err
	}
	spec := jobs.Spec{Kind: a.Name()}
	if a.dryRun {
		// Read-only: no resource is held, like db-verify.
		spec.Run = func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.runDry(ctx, r, slug)
		}
		return spec, nil
	}
	// The job confirms the checksum the user reviewed (the verified preview
	// fingerprint), never the plan just recomputed: a plan that changed
	// since the preview is refused here and again inside the lock.
	checksum, err := verifiedFingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	if rep.PlanChecksum != checksum {
		return jobs.Spec{}, ErrStalePreview
	}
	// The use case reads the catalog but only writes the database (and the
	// backup directory), so only the database is held.
	spec.Resources = []string{ResourceDatabase}
	spec.Run = func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
		return a.runApply(ctx, r, slug, checksum)
	}
	return spec, nil
}

func (a *reconcileAction) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Safe(err)
}

func (a *reconcileAction) runDry(ctx context.Context, r jobs.Reporter, slug string) (jobs.Outcome, error) {
	r.Step("planning")
	rep, err := a.plan(ctx, slug)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	countReconcile(r, rep)
	summary := fmt.Sprintf("Dry run for %s (status %s): %s; %s. Plan checksum %s.",
		slug, rep.Status, associationsLine(rep, true), syncStateLine(rep), rep.PlanChecksum)
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: reconcileReport(a.Name(), rep)}}, nil
}

func (a *reconcileAction) runApply(ctx context.Context, r jobs.Reporter, slug, checksum string) (jobs.Outcome, error) {
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

	r.Step("checking plan")
	current, err := a.plan(ctx, slug)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	if current.PlanChecksum != checksum {
		return jobs.Outcome{}, Safe(ErrStalePreview)
	}
	// Last point where a cancel leaves everything untouched.
	if err := ctx.Err(); err != nil {
		return jobs.Outcome{}, err
	}

	r.Step("backing up and applying")
	rep, err := a.deps.Port.Apply(ctx, slug, checksum)
	if err != nil {
		if rep.PlanChecksum != "" && rep.PlanChecksum != checksum {
			return jobs.Outcome{}, Safe(ErrStalePreview)
		}
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	if rep.Backup == nil {
		// The use case found nothing to change and took no backup.
		summary := fmt.Sprintf("Nothing to apply: %s is already reconciled (status %s).", slug, rep.Status)
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: reconcileReport(a.Name(), rep)}}, nil
	}
	countReconcile(r, rep)
	summary := fmt.Sprintf("Applied reconciliation for %s (status %s): %s; %s. Backup %s written to %s; restore token %s (artists reconcile --restore).",
		slug, rep.Status, associationsLine(rep, false), syncStateLine(rep),
		filepath.Base(rep.Backup.Path), a.deps.BackupName, rep.RetiredAt)
	if rep.Status == artists.StatusAwaitingResync {
		summary += " Run a sync of " + slug + " to finish."
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: reconcileReport(a.Name(), rep)}}, nil
}

func countReconcile(r jobs.Reporter, rep artists.ReconcileReport) {
	r.Count("unlinked", rep.Orphan.Albums+rep.Orphan.Tracks)
	r.Count("kept", rep.Supported.Albums+rep.Supported.Tracks+rep.Unverifiable.Albums+rep.Unverifiable.Tracks)
	r.Count("checkpoints_invalidated", len(rep.StaleCheckpoints))
}

// reconcileReport lists every association, identity and sync state change of
// the plan, sorted.
func reconcileReport(name string, rep artists.ReconcileReport) *jobs.Report {
	slug := rep.Slug
	var rows [][]string
	for _, item := range rep.Items {
		label := strings.TrimSpace(item.SpotifyID + " " + item.Name)
		from := "active (" + string(item.Class) + ")"
		to := "active"
		if item.Class == artists.ReconcileOrphan {
			to = "unlinked"
			if len(item.SharedWith) > 0 {
				to = fmt.Sprintf("unlinked (%s kept: shared with %s)", item.Kind, strings.Join(item.SharedWith, ", "))
			}
		}
		rows = append(rows, []string{string(item.Kind), slug, label, from, to})
	}
	if rep.Identity.Drift {
		for _, id := range rep.Identity.Removed {
			rows = append(rows, []string{"spotify_id", slug, id, "linked", "unlinked"})
		}
		for _, id := range rep.Identity.Added {
			rows = append(rows, []string{"spotify_id", slug, id, "unlinked", "linked"})
		}
	}
	for _, cp := range rep.StaleCheckpoints {
		rows = append(rows, []string{"checkpoint", slug, fmt.Sprintf("sync run %d", cp.RunID), cp.Status, "invalidated"})
	}
	if rep.MetadataStale {
		rows = append(rows, []string{"metadata_cache", slug, "metadata refresh", "stale", "invalidated"})
	}
	if rep.ResyncRequired {
		rows = append(rows, []string{"resync", slug, idList(rep.Identity.Current), "", "required"})
	}
	out := &jobs.Report{Name: name, Columns: reconcileColumns, Rows: rows}
	SortRows(out)
	return out
}

func associationsLine(rep artists.ReconcileReport, dryRun bool) string {
	verb := "unlinked"
	if dryRun {
		verb = "would be unlinked"
	}
	kept := artists.ReconcileCounts{
		Albums: rep.Supported.Albums + rep.Unverifiable.Albums,
		Tracks: rep.Supported.Tracks + rep.Unverifiable.Tracks,
	}
	line := fmt.Sprintf("%s %s", countsPhrase(rep.Orphan), verb)
	if shared := rep.SharedOrphans.Albums + rep.SharedOrphans.Tracks; shared > 0 {
		line += fmt.Sprintf(" (%d shared with other artists stay in the catalog)", shared)
	}
	return line + fmt.Sprintf(", %s kept", countsPhrase(kept))
}

func syncStateLine(rep artists.ReconcileReport) string {
	return fmt.Sprintf("%d sync checkpoints invalidated, metadata cache invalidated: %s, replacement sync required: %s",
		len(rep.StaleCheckpoints), yesNo(rep.MetadataStale), yesNo(rep.ResyncRequired))
}

func countsPhrase(c artists.ReconcileCounts) string {
	return plural(c.Albums, "album") + " and " + plural(c.Tracks, "track")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func idList(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}
