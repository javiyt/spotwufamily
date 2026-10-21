package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// RefreshPort refreshes artist genres, images and links from Spotify. A real
// run saves the catalog and records per-artist TTL checkpoints in the
// database; force ignores the checkpoints. A dry run writes nothing.
type RefreshPort interface {
	Run(ctx context.Context, dryRun, force bool, progress func(artists.RefreshGenresProgress)) (artists.RefreshGenresReport, error)
}

// RefreshDeps are the ports the metadata refresh actions need. Locker and
// Backuper are only used by the real (non dry-run) refresh.
type RefreshDeps struct {
	Runner      RefreshPort
	Digest      CatalogDigest
	Credentials CredentialsChecker
	Locker      Locker
	Backuper    Backuper
}

// refreshTypedPhrase confirms a forced refresh, which ignores checkpoints.
const refreshTypedPhrase = "refresh metadata"

var refreshColumns = []string{"slug", "outcome", "detail"}

type refreshAction struct {
	deps   RefreshDeps
	dryRun bool
}

// NewRefreshMetadataPreview returns refresh-metadata-preview: a dry run that
// reads Spotify but writes neither the catalog nor the database.
func NewRefreshMetadataPreview(d RefreshDeps) Action { return &refreshAction{deps: d, dryRun: true} }

// NewRefreshMetadata returns refresh-metadata. It takes the cross-process
// database lock and backs up the database before running; force=true ignores
// the TTL checkpoints and needs a typed confirmation.
func NewRefreshMetadata(d RefreshDeps) Action { return &refreshAction{deps: d} }

func (a *refreshAction) Name() string {
	if a.dryRun {
		return "refresh-metadata-preview"
	}
	return "refresh-metadata"
}

func (a *refreshAction) Describe() Description {
	title := "Refresh artist metadata"
	if a.dryRun {
		title = "Refresh artist metadata (dry run)"
	}
	return Description{Title: title, UsesNetwork: true}
}

func (a *refreshAction) Available() (bool, string) {
	if !a.deps.Credentials() {
		return false, spotifyUnavailableReason
	}
	return true, ""
}

func (a *refreshAction) Validate(p Params) error {
	if err := RejectUnknown(p, "force"); err != nil {
		return err
	}
	return validBool(p, "force")
}

func (a *refreshAction) Preview(ctx context.Context, p Params) (Preview, error) {
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return Preview{}, err
	}
	force := boolParam(p, "force")
	checkpoints := "artists refreshed within the TTL are skipped"
	if force {
		checkpoints = "ignored (every artist is fetched again)"
	}
	writes := "catalog metadata (previous file kept as .bak) and refresh checkpoints; database backed up first"
	if a.dryRun {
		writes = "none (dry run)"
	}
	pv := Preview{
		Title: a.Describe().Title,
		Scope: []ScopeLine{
			{Label: "Artists", Value: "all artists with Spotify IDs"},
			{Label: "Checkpoints", Value: checkpoints},
			{Label: "Writes", Value: writes},
		},
		UsesNetwork: true,
		Fingerprint: a.fingerprint(digest, force),
	}
	if force {
		pv.Warnings = append(pv.Warnings, "Force ignores refresh checkpoints and uses more Spotify quota.")
		if !a.dryRun {
			pv.Destructive = true
			pv.RequiresTyped = refreshTypedPhrase
		}
	}
	return pv, nil
}

func (a *refreshAction) fingerprint(digest string, force bool) string {
	return fingerprint(a.Name(), digest, yesNo(force))
}

// Spec binds a real refresh to the fingerprint the user confirmed (failing
// closed without one); the dry run needs no confirmation.
func (a *refreshAction) Spec(ctx context.Context, p Params) (jobs.Spec, error) {
	if !a.deps.Credentials() {
		return jobs.Spec{}, ErrCredentialsMissing
	}
	var verified string
	if !a.dryRun {
		fp, err := verifiedFingerprint(ctx)
		if err != nil {
			return jobs.Spec{}, err
		}
		verified = fp
	}
	resources := []string{ResourceDatabase}
	if !a.dryRun {
		// A real refresh also rewrites the catalog YAML.
		resources = append(resources, ResourceCatalogYAML)
	}
	force := boolParam(p, "force")
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: resources,
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, force, verified)
		},
	}, nil
}

func (a *refreshAction) run(ctx context.Context, r jobs.Reporter, force bool, verified string) (jobs.Outcome, error) {
	if !a.dryRun {
		r.Step("locking database")
		release, err := a.deps.Locker.TryLock()
		if err != nil {
			return jobs.Outcome{}, Safe(err)
		}
		defer release()
		// Re-check under the lock: the catalog or ledger may have changed
		// since the user confirmed the preview.
		r.Step("checking preview")
		digest, err := a.deps.Digest(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return jobs.Outcome{}, ctx.Err()
			}
			return jobs.Outcome{}, Safe(err)
		}
		if a.fingerprint(digest, force) != verified {
			return jobs.Outcome{}, Safe(ErrStalePreview)
		}
		r.Step("backing up database")
		if err := a.deps.Backuper.Backup(ctx); err != nil {
			if ctx.Err() != nil {
				return jobs.Outcome{}, ctx.Err()
			}
			return jobs.Outcome{}, Safe(err)
		}
	}

	// A Spotify rate limit stops the run: further requests would only fail.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu   sync.Mutex
		rows [][]string
	)
	r.Step("refreshing")
	report, err := a.deps.Runner.Run(runCtx, a.dryRun, force, func(e artists.RefreshGenresProgress) {
		if e.ArtistTotal > 0 {
			r.Progress(e.ArtistIndex, e.ArtistTotal)
		}
		outcome := ""
		switch e.Stage {
		case "artist_updated":
			outcome = "updated"
		case "artist_unchanged":
			outcome = "unchanged"
		case "artist_skipped":
			outcome = "skipped_recent"
		case "spotify_failed":
			if _, limited := artists.RetryAfterOf(e.Err); limited {
				stop()
			}
		}
		if outcome != "" {
			r.Count(outcome, 1)
			mu.Lock()
			rows = append(rows, []string{e.ArtistSlug, outcome, ""})
			mu.Unlock()
		}
	})
	resumable := !a.dryRun
	if ctx.Err() != nil {
		return jobs.Outcome{Resumable: resumable}, ctx.Err()
	}

	var wait time.Duration
	failed := map[string]bool{}
	for _, item := range report.Errors {
		if d, ok := artists.RetryAfterOf(item.Err); ok && d > wait {
			wait = d
		}
		if errors.Is(item.Err, context.Canceled) {
			continue // not attempted after the rate limit stopped the run
		}
		failed[item.Slug] = true
		rows = append(rows, []string{item.Slug, "failed", item.ID + ": " + Safe(item.Err).Error()})
	}
	if d, ok := artists.RetryAfterOf(err); ok && d > wait {
		wait = d
	}
	r.Count("failed", len(failed))
	// Only a run that finished (possibly with per-artist errors) is partial;
	// any other error aborted it, and its counts may include unsaved updates.
	if _, limited := artists.RetryAfterOf(err); err != nil && !limited && !errors.Is(err, artists.ErrRefreshCompletedWithErrors) {
		return jobs.Outcome{}, Safe(err)
	}

	rep := &jobs.Report{Name: a.Name(), Columns: refreshColumns, Rows: rows}
	SortRows(rep)
	counts := fmt.Sprintf("%d updated, %d unchanged, %d skipped (recently refreshed), %d failed.",
		report.Updated, report.Unchanged, report.SkippedRecent, len(failed))
	if a.dryRun {
		counts = "Dry run: " + counts
	}
	switch {
	case wait > 0:
		return jobs.Outcome{
			Status:    jobs.StatusPartial,
			Result:    jobs.Result{Summary: Safe(rateLimited(wait)).Error() + " " + counts, Report: rep},
			Resumable: resumable,
		}, nil
	case err != nil:
		return jobs.Outcome{
			Status: jobs.StatusPartial,
			Result: jobs.Result{Summary: "Some artists could not be refreshed. " + counts, Report: rep},
		}, nil
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: counts, Report: rep}}, nil
}

// rateLimited carries a Retry-After so Safe renders the seconds to wait.
type rateLimited time.Duration

func (e rateLimited) Error() string             { return "rate limited" }
func (e rateLimited) RetryAfter() time.Duration { return time.Duration(e) }
