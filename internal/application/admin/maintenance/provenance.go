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
// records the result. Without a previous record nothing is recorded: a
// partial application cannot vouch for the artists it did not touch, so the
// database stays unknown until a whole-catalog run.
func mergeApplied(ctx context.Context, p Provenance, stage string, runs []appliedRun,
	merge func(prev, used artists.AppliedCatalog, slug string) (artists.AppliedCatalog, bool)) error {
	if p == nil || len(runs) == 0 {
		return nil
	}
	prev, ok, err := p.AppliedCatalog(ctx)
	if err != nil || !ok {
		return err
	}
	for _, run := range runs {
		next, ok := merge(prev, run.used, run.slug)
		if !ok {
			return nil
		}
		prev = next
	}
	return recordApplied(ctx, p, stage, prev)
}

func mergeSynced(prev, used artists.AppliedCatalog, slug string) (artists.AppliedCatalog, bool) {
	return artists.MergeApplied(prev, used, []string{slug}), true
}
