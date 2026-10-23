package maintenance

import (
	"context"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

// Provenance records, after an action succeeded, the catalog and database
// digests it ran against. It is optional in every deps struct: nil records
// nothing. Only sync and reconcile-apply record the applied catalog; the
// snapshot, export and release stages only checkpoint themselves, so they can
// never make the database look up to date with the catalog.
type Provenance interface {
	artists.ProvenanceStore
	// DatabaseDigest hashes the deterministic snapshot of the database as it
	// is now (provenance tables excluded).
	DatabaseDigest(ctx context.Context) (string, error)
	// CatalogDigest is the fingerprint digest of the editorial catalog as it
	// is now.
	CatalogDigest(ctx context.Context) (string, error)
	// PendingReconciliations lists the slugs whose reconciliation is not
	// completed (drifted or awaiting their resync). A sync seeds their new
	// Spotify IDs but leaves their orphaned associations in place, so they
	// are never recorded as applied.
	PendingReconciliations(ctx context.Context) ([]string, error)
}

// provenanceWarning is appended to a successful summary whose provenance
// could not be recorded. Not recording is the safe direction: the stage is
// shown as not done.
const provenanceWarning = " The update status could not be recorded; this step may still be shown as not done."

func provenanceNow() string { return time.Now().UTC().Format(time.RFC3339) }

// recordCheckpoint checkpoints stage against catalogDigest and the current
// database digest.
func recordCheckpoint(ctx context.Context, p Provenance, stage, catalogDigest string) error {
	if p == nil {
		return nil
	}
	db, err := p.DatabaseDigest(ctx)
	if err != nil {
		return err
	}
	return p.RecordCheckpoint(ctx, artists.StageCheckpoint{
		Stage: stage, CatalogDigest: catalogDigest, DBDigest: db, At: provenanceNow(),
	})
}

// recordApplied records applied as the catalog the database now reflects
// and checkpoints stage against its digest.
func recordApplied(ctx context.Context, p Provenance, stage string, applied artists.AppliedCatalog) error {
	applied.AppliedAt = provenanceNow()
	if err := p.RecordApplied(ctx, applied); err != nil {
		return err
	}
	return recordCheckpoint(ctx, p, stage, applied.Digest)
}

// appliedRun is the catalog one sync or reconcile run read, and the artists
// it applied from it.
type appliedRun struct {
	slug string
	used artists.AppliedCatalog
}

// mergeApplied folds the runs into the previously applied catalog and
// records the result. Artists with a pending reconciliation keep their
// previous fingerprint; when no run is left nothing is recorded. Without a
// previous record nothing is recorded: a partial application cannot vouch
// for the artists it did not touch, so the database stays unknown until a
// whole-catalog run.
func mergeApplied(ctx context.Context, p Provenance, stage string, runs []appliedRun,
	merge func(prev, used artists.AppliedCatalog, slug string) (artists.AppliedCatalog, bool)) error {
	if p == nil || len(runs) == 0 {
		return nil
	}
	pending, err := pendingSet(ctx, p)
	if err != nil {
		return err
	}
	prev, ok, err := p.AppliedCatalog(ctx)
	if err != nil || !ok {
		return err
	}
	merged := false
	for _, run := range runs {
		if pending[run.slug] {
			continue
		}
		next, ok := merge(prev, run.used, run.slug)
		if !ok {
			return nil
		}
		prev, merged = next, true
	}
	if !merged {
		return nil
	}
	return recordApplied(ctx, p, stage, prev)
}

func mergeSynced(prev, used artists.AppliedCatalog, slug string) (artists.AppliedCatalog, bool) {
	return artists.MergeApplied(prev, used, []string{slug}), true
}

// recordWholeCatalog records what a successful whole-catalog run applied.
// Without pending reconciliations that is the used catalog. Otherwise the
// pending artists keep their previous fingerprint, and without a previous
// record nothing is recorded (the database stays unknown).
func recordWholeCatalog(ctx context.Context, p Provenance, stage string, used artists.AppliedCatalog) error {
	pending, err := pendingSet(ctx, p)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return recordApplied(ctx, p, stage, used)
	}
	prev, ok, err := p.AppliedCatalog(ctx)
	if err != nil || !ok {
		return err
	}
	var slugs []string
	for _, a := range used.Artists {
		if !pending[a.Slug] {
			slugs = append(slugs, a.Slug)
		}
	}
	// Artists removed from the catalog are dropped unless still pending.
	for _, a := range prev.Artists {
		if !pending[a.Slug] {
			slugs = append(slugs, a.Slug)
		}
	}
	return recordApplied(ctx, p, stage, artists.MergeApplied(prev, used, slugs))
}

func pendingSet(ctx context.Context, p Provenance) (map[string]bool, error) {
	slugs, err := p.PendingReconciliations(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		out[s] = true
	}
	return out, nil
}
