package maintenance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// SyncRunResult is one catalog sync's report plus the snapshot write outcome.
type SyncRunResult struct {
	Report catalogsync.Report
	// SnapshotErr is the failure to write the (full or partial) snapshot.
	SnapshotErr error
}

// SyncPort runs one catalog sync: the whole catalog when o.ArtistSlug is
// empty, otherwise that artist. A real run writes only the database
// (records, checkpoints, pending reconciliations) and the snapshot file; it
// returns the sync error unchanged (*catalogsync.PartialSyncError on a rate
// limit abort). A dry run contacts neither Spotify nor the database.
type SyncPort interface {
	Run(ctx context.Context, o catalogsync.Options) (SyncRunResult, error)
}

// ResumableRun identifies the partial sync run a resume would continue.
type ResumableRun struct {
	ID             int64
	CompletedSlugs []string
}

// SyncDeps are the ports the sync actions need. Locker and Backuper are only
// used by the real sync; Credentials only gates it.
type SyncDeps struct {
	Runner SyncPort
	Digest CatalogDigest
	// Syncable lists, in catalog order, the slugs of enabled artists with a
	// Spotify profile the catalog's sync policy allows (verified by default).
	Syncable func(ctx context.Context) ([]string, error)
	// Resumable returns the partial run a resume would continue, if any.
	Resumable   func(ctx context.Context) (ResumableRun, bool, error)
	Credentials CredentialsChecker
	Locker      Locker
	Backuper    Backuper
}

// Sync scopes.
const (
	syncScopeArtist   = "artist"
	syncScopeSelected = "selected"
	syncScopeAll      = "all"
)

// albumPagesEstimate is the assumed number of artist-album pages (50 albums
// each) Spotify returns per artist. With one artist lookup it gives the
// documented estimate of artists × (1 + albumPagesEstimate) requests. It is
// only an estimate: album and track details come from the local album cache
// when present, but a first sync of an artist fetches them too.
const albumPagesEstimate = 2

// syncTypedThreshold is the largest number of artists synced without a typed
// confirmation (scope=all always needs one).
const syncTypedThreshold = 5

const syncQuotaCaveat = "Spotify request counts are an estimate: artists synced recently are skipped, " +
	"but first syncs also fetch album and track details. A Spotify rate limit stops the sync; " +
	"progress is kept and it can be resumed."

var syncColumns = []string{"slug", "outcome", "detail"}

type syncAction struct {
	deps   SyncDeps
	dryRun bool
}

// NewSyncDryRun returns sync-dry-run: it plans the sync from the catalog
// without contacting Spotify or writing anything.
func NewSyncDryRun(d SyncDeps) Action { return &syncAction{deps: d, dryRun: true} }

// NewSync returns sync. It takes the cross-process database lock, backs up the
// database and syncs the artists in scope from Spotify; syncing every artist
// or more than five needs a typed confirmation.
func NewSync(d SyncDeps) Action { return &syncAction{deps: d} }

func (a *syncAction) Name() string {
	if a.dryRun {
		return "sync-dry-run"
	}
	return "sync"
}

func (a *syncAction) Describe() Description {
	if a.dryRun {
		return Description{Title: "Sync catalog from Spotify (dry run)"}
	}
	return Description{Title: "Sync catalog from Spotify", UsesNetwork: true}
}

// Available reports the real sync as unavailable without credentials; the
// dry run never contacts Spotify.
func (a *syncAction) Available() (bool, string) {
	if !a.dryRun && !a.deps.Credentials() {
		return false, spotifyUnavailableReason
	}
	return true, ""
}

func syncInvalid(msg string) error { return &SafeError{Code: "validation", Message: msg} }

func (a *syncAction) Validate(p Params) error {
	if err := RejectUnknown(p, "scope", "artists", "resume"); err != nil {
		return err
	}
	if err := validBool(p, "resume"); err != nil {
		return err
	}
	_, err := parseSyncScope(p)
	return err
}

// parseSyncScope checks scope and artists and returns the requested slugs
// (nil for scope=all).
func parseSyncScope(p Params) ([]string, error) {
	scope := p["scope"]
	var slugs []string
	for _, s := range strings.Split(p["artists"], ",") {
		if s = strings.TrimSpace(s); s != "" {
			slugs = append(slugs, s)
		}
	}
	switch scope {
	case syncScopeAll:
		if strings.TrimSpace(p["artists"]) != "" {
			return nil, syncInvalid("Artists cannot be listed when syncing all artists.")
		}
		return nil, nil
	case syncScopeArtist, syncScopeSelected:
	case "":
		return nil, syncInvalid("Scope is required: artist, selected or all.")
	default:
		return nil, syncInvalid("Scope must be artist, selected or all.")
	}
	if len(slugs) == 0 {
		return nil, syncInvalid("List at least one artist slug.")
	}
	if scope == syncScopeArtist && len(slugs) != 1 {
		return nil, syncInvalid("Scope artist takes exactly one artist slug.")
	}
	seen := map[string]bool{}
	for _, s := range slugs {
		if !validSlug(s) {
			return nil, syncInvalid("Artist " + quoteParam(s) + " is not a valid slug.")
		}
		if seen[s] {
			return nil, syncInvalid("Artist " + quoteParam(s) + " is listed more than once.")
		}
		seen[s] = true
	}
	return slugs, nil
}

// syncPlan is the state a preview was computed from.
type syncPlan struct {
	scope       string
	slugs       []string
	resume      bool
	run         ResumableRun
	hasRun      bool
	fingerprint string
}

func (a *syncAction) plan(ctx context.Context, p Params) (syncPlan, error) {
	requested, err := parseSyncScope(p)
	if err != nil {
		return syncPlan{}, err
	}
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return syncPlan{}, err
	}
	syncable, err := a.deps.Syncable(ctx)
	if err != nil {
		return syncPlan{}, err
	}
	pl := syncPlan{scope: p["scope"], resume: boolParam(p, "resume"), slugs: syncable}
	if requested != nil {
		for _, s := range requested {
			if !slices.Contains(syncable, s) {
				return syncPlan{}, syncInvalid("Artist " + quoteParam(s) +
					" is not an enabled artist with a verified Spotify profile.")
			}
		}
		pl.slugs = requested
	}
	runIdentity := ""
	if pl.resume {
		pl.run, pl.hasRun, err = a.deps.Resumable(ctx)
		if err != nil {
			return syncPlan{}, err
		}
		if pl.hasRun {
			completed := slices.Clone(pl.run.CompletedSlugs)
			slices.Sort(completed)
			runIdentity = strconv.FormatInt(pl.run.ID, 10) + ":" + strings.Join(completed, ",")
		}
	}
	pl.fingerprint = fingerprint(a.Name(), digest, pl.scope, strings.Join(pl.slugs, ","), yesNo(pl.resume), runIdentity)
	return pl, nil
}

func (a *syncAction) Preview(ctx context.Context, p Params) (Preview, error) {
	pl, err := a.plan(ctx, p)
	if err != nil {
		return Preview{}, err
	}
	n := len(pl.slugs)
	artistsLine := fmt.Sprintf("%d (%s)", n, joinLimited(pl.slugs, 10))
	if pl.scope == syncScopeAll {
		artistsLine = fmt.Sprintf("%d (all enabled artists with a verified Spotify profile)", n)
	}
	resume := "no (starts a new run)"
	if pl.resume {
		resume = "requested, but there is no partial run to resume"
		if pl.hasRun {
			resume = fmt.Sprintf("continues partial run #%d (%d artists already completed)", pl.run.ID, len(pl.run.CompletedSlugs))
		}
	}
	writes := "database (records, checkpoints and snapshot); database backed up first"
	requests := fmt.Sprintf("about %d (estimate)", n*(1+albumPagesEstimate))
	if a.dryRun {
		writes = "none (dry run)"
		requests = fmt.Sprintf("none now; a real sync makes about %d (estimate)", n*(1+albumPagesEstimate))
	}
	pv := Preview{
		Title: a.Describe().Title,
		Scope: []ScopeLine{
			{Label: "Artists", Value: artistsLine},
			{Label: "Spotify requests", Value: requests},
			{Label: "Resume", Value: resume},
			{Label: "Writes", Value: writes},
		},
		Warnings:    []string{syncQuotaCaveat},
		UsesNetwork: !a.dryRun,
		Fingerprint: pl.fingerprint,
	}
	if !a.dryRun && (pl.scope == syncScopeAll || n > syncTypedThreshold) {
		pv.Destructive = true
		pv.RequiresTyped = fmt.Sprintf("sync %d artists", n)
	}
	return pv, nil
}

// Spec plans the sync now (the service has just verified the preview against
// this state) and re-checks the plan's fingerprint inside the job once the
// lock is held, so a change while the job was queued refuses the run.
func (a *syncAction) Spec(ctx context.Context, p Params) (jobs.Spec, error) {
	if !a.dryRun && !a.deps.Credentials() {
		return jobs.Spec{}, ErrCredentialsMissing
	}
	pl, err := a.plan(ctx, p)
	if err != nil {
		return jobs.Spec{}, err
	}
	var resources []string
	if !a.dryRun {
		// Sync writes the database and the snapshot; it only reads the catalog.
		resources = []string{ResourceDatabase}
	}
	params := maps.Clone(p)
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: resources,
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			if a.dryRun {
				return a.runDry(ctx, r, pl)
			}
			return a.run(ctx, r, params, pl)
		},
	}, nil
}

func (a *syncAction) run(ctx context.Context, r jobs.Reporter, p Params, pl syncPlan) (jobs.Outcome, error) {
	r.Step("locking database")
	release, err := a.deps.Locker.TryLock()
	if err != nil {
		return jobs.Outcome{}, Safe(err)
	}
	defer release()

	r.Step("checking preview")
	current, err := a.plan(ctx, p)
	if err != nil {
		if ctx.Err() != nil {
			return jobs.Outcome{}, ctx.Err()
		}
		return jobs.Outcome{}, Safe(err)
	}
	if current.fingerprint != pl.fingerprint {
		return jobs.Outcome{}, Safe(ErrStalePreview)
	}

	// The backuper migrates the database before backing it up, so a new or
	// un-migrated database is backed up (and verified) like any other.
	r.Step("backing up database")
	if err := a.deps.Backuper.Backup(ctx); err != nil {
		if ctx.Err() != nil {
			return jobs.Outcome{}, ctx.Err()
		}
		return jobs.Outcome{}, Safe(err)
	}

	t := &syncTally{r: r}
	targets := []string{""} // the whole catalog in one run
	if pl.scope != syncScopeAll {
		targets = pl.slugs
	}
	var (
		limitErr    error
		otherErr    error
		snapshotErr error
	)
	for i, slug := range targets {
		if ctx.Err() != nil {
			break
		}
		perSlug := slug != ""
		if perSlug {
			r.Step(slug)
		} else {
			r.Step("syncing")
		}
		res, err := a.deps.Runner.Run(ctx, catalogsync.Options{
			ArtistSlug: slug,
			Resume:     pl.resume,
			Progress:   t.progress(perSlug),
		})
		if res.SnapshotErr != nil {
			snapshotErr = res.SnapshotErr
		}
		if ctx.Err() != nil {
			break
		}
		limited := isRateLimit(err)
		t.add(res.Report, limited)
		if perSlug {
			r.Progress(i+1, len(targets))
		}
		switch {
		case err == nil:
		case limited:
			limitErr = err
			if perSlug {
				t.remaining = len(targets) - i
			} else {
				// The aborting artist is counted as failed but is still to do.
				failed := max(res.Report.ArtistsFailed-1, 0)
				t.remaining = max(res.Report.ArtistsPlanned-res.Report.ArtistsProcessed-failed, 0)
			}
		case isArtistFailures(err):
			otherErr = err
		default:
			return jobs.Outcome{}, Safe(err)
		}
		if limitErr != nil {
			break
		}
	}
	if ctx.Err() != nil {
		// Completed artists keep their checkpoints; a resume skips them.
		return jobs.Outcome{Resumable: true}, ctx.Err()
	}

	rep := &jobs.Report{Name: a.Name(), Columns: syncColumns, Rows: t.rowsSnapshot()}
	SortRows(rep)
	counts := t.summary()
	switch {
	case limitErr != nil:
		msg := rateLimitMessage(limitErr) + " " + counts
		if snapshotErr != nil {
			msg += " The partial snapshot could not be written."
		}
		return jobs.Outcome{Status: jobs.StatusPartial, Result: jobs.Result{Summary: msg, Report: rep}, Resumable: true}, nil
	case otherErr != nil:
		return jobs.Outcome{Status: jobs.StatusPartial, Result: jobs.Result{Summary: "Some artists could not be synced. " + counts, Report: rep}}, nil
	case snapshotErr != nil:
		return jobs.Outcome{Status: jobs.StatusPartial, Result: jobs.Result{Summary: counts + " The database snapshot could not be written.", Report: rep}}, nil
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: counts, Report: rep}}, nil
}

func (a *syncAction) runDry(ctx context.Context, r jobs.Reporter, pl syncPlan) (jobs.Outcome, error) {
	targets := []string{""}
	if pl.scope != syncScopeAll {
		targets = pl.slugs
	}
	planned := 0
	for i, slug := range targets {
		if slug != "" {
			r.Step(slug)
		} else {
			r.Step("planning")
		}
		res, err := a.deps.Runner.Run(ctx, catalogsync.Options{ArtistSlug: slug, DryRun: true, Resume: pl.resume})
		if ctx.Err() != nil {
			return jobs.Outcome{}, ctx.Err()
		}
		if err != nil {
			return jobs.Outcome{}, Safe(err)
		}
		planned += res.Report.ArtistsPlanned
		r.Progress(i+1, len(targets))
	}
	r.Count("planned", planned)
	rows := make([][]string, 0, len(pl.slugs))
	for _, s := range pl.slugs {
		rows = append(rows, []string{s, "planned", ""})
	}
	rep := &jobs.Report{Name: a.Name(), Columns: syncColumns, Rows: rows}
	SortRows(rep)
	summary := fmt.Sprintf("Dry run: %d artists would be synced, about %d Spotify requests (estimate).",
		planned, planned*(1+albumPagesEstimate))
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: rep}}, nil
}

// isRateLimit reports a Spotify rate limit or quota abort.
func isRateLimit(err error) bool {
	var partial *catalogsync.PartialSyncError
	if errors.As(err, &partial) {
		return true
	}
	_, limited := artists.RetryAfterOf(err)
	return limited
}

// isArtistFailures reports a run that finished with per-artist errors.
func isArtistFailures(err error) bool {
	var failed *catalogsync.ArtistSyncsFailedError
	return errors.As(err, &failed)
}

func rateLimitMessage(err error) string {
	if wait, ok := artists.RetryAfterOf(err); ok {
		return fmt.Sprintf("Spotify rate limit reached; retry after %ds.", int(math.Ceil(wait.Seconds())))
	}
	return "Spotify rate limit reached; retry later."
}

// syncTally accumulates per-artist outcomes across sync runs.
type syncTally struct {
	r jobs.Reporter

	mu        sync.Mutex
	rows      [][]string
	synced    int
	failed    int
	skipped   int
	remaining int
}

// progress maps catalogsync events to reporter counts and report rows. A
// whole-catalog run also drives Progress; per-slug runs report it per slug.
func (t *syncTally) progress(perSlug bool) func(catalogsync.ProgressEvent) {
	return func(e catalogsync.ProgressEvent) {
		var row []string
		switch e.Stage {
		case catalogsync.ProgressArtistFinished:
			t.r.Count("synced", 1)
			t.r.Count("albums", e.Stats.AlbumsUpserted)
			t.r.Count("tracks", e.Stats.TracksUpserted)
			row = []string{e.ArtistSlug, "synced", fmt.Sprintf("%d albums, %d tracks", e.Stats.AlbumsUpserted, e.Stats.TracksUpserted)}
		case catalogsync.ProgressArtistFailed:
			outcome := "failed"
			if isRateLimit(e.Err) {
				outcome = "rate_limited"
			} else {
				t.r.Count("failed", 1)
			}
			row = []string{e.ArtistSlug, outcome, Safe(e.Err).Error()}
		default:
			return
		}
		if !perSlug && e.ArtistTotal > 0 {
			t.r.Progress(e.ArtistIndex, e.ArtistTotal)
		}
		t.mu.Lock()
		t.rows = append(t.rows, row)
		t.mu.Unlock()
	}
}

// add folds one run's report in; an aborting artist is remaining, not failed.
func (t *syncTally) add(rep catalogsync.Report, limited bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	failed := rep.ArtistsFailed
	if limited && failed > 0 {
		failed--
	}
	t.synced += rep.ArtistsProcessed
	t.failed += failed
	t.skipped += rep.ArtistsFreshSkipped + rep.ArtistsResumed
	if rep.ArtistsFreshSkipped+rep.ArtistsResumed > 0 {
		t.r.Count("skipped", rep.ArtistsFreshSkipped+rep.ArtistsResumed)
	}
}

func (t *syncTally) rowsSnapshot() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.rows)
}

func (t *syncTally) summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := fmt.Sprintf("%d artists synced, %d skipped (recently synced or already completed), %d failed",
		t.synced, t.skipped, t.failed)
	if t.remaining > 0 {
		s += fmt.Sprintf(", %d remaining", t.remaining)
	}
	return s + "."
}
