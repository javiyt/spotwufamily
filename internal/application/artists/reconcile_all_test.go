package artists_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

// bulkStore is a per-artist in-memory BulkReconcileStore. ApplyReconciliations
// is all-or-nothing, as the SQLite adapter's single transaction.
type bulkStore struct {
	associations map[string][]artists.Association
	impact       map[string]artists.ImpactState
	backups      []artists.BackupRequest
	batches      [][]artists.ApplyRequest
	backupErr    error
	// failSlug makes ApplyReconciliations fail on that artist.
	failSlug    string
	cleared     int
	clearCtxErr error
}

func (s *bulkStore) ActiveAssociations(_ context.Context, slug string) ([]artists.Association, error) {
	return slices.Clone(s.associations[slug]), nil
}

func (s *bulkStore) Impact(_ context.Context, slug string) (artists.ImpactState, error) {
	return s.impact[slug], nil
}

func (s *bulkStore) PendingReconciliations(context.Context) ([]artists.PendingReconciliation, error) {
	return nil, nil
}

func (s *bulkStore) BackupDatabase(_ context.Context, r artists.BackupRequest) (artists.BackupInfo, error) {
	if s.backupErr != nil {
		return artists.BackupInfo{}, s.backupErr
	}
	s.backups = append(s.backups, r)
	return artists.BackupInfo{Path: r.Dir + "/" + r.Name + ".db", SHA256: "sha"}, nil
}

func (s *bulkStore) ApplyReconciliation(context.Context, artists.ApplyRequest) error {
	return errors.New("the bulk apply never applies artists one by one")
}

func (s *bulkStore) ApplyReconciliations(_ context.Context, reqs []artists.ApplyRequest) error {
	for _, r := range reqs {
		if r.Slug == s.failSlug {
			return &artists.BulkApplyError{Slug: r.Slug, Err: errors.New("disk I/O error at /secret/catalog.db")}
		}
	}
	s.batches = append(s.batches, reqs)
	for _, r := range reqs {
		retired := append(slices.Clone(r.AlbumIDs), r.TrackIDs...)
		s.associations[r.Slug] = slices.DeleteFunc(s.associations[r.Slug], func(a artists.Association) bool {
			return slices.Contains(retired, a.SpotifyID)
		})
		s.impact[r.Slug] = artists.ImpactState{Baseline: artists.MappingBaseline{SpotifyIDs: r.TargetIDs, Source: artists.BaselineSnapshot}}
	}
	return nil
}

func (s *bulkStore) RestoreAssociations(context.Context, string, string) (int, int, error) {
	return 0, 0, errors.New("not used")
}

func (s *bulkStore) ClearProvenance(ctx context.Context) error {
	s.cleared++
	s.clearCtxErr = ctx.Err()
	return nil
}

func bulkArtist(slug, id string) catalog.Artist {
	return catalog.Artist{Slug: slug, Enabled: true, SpotifyID: id,
		SpotifyProfiles: []catalog.SpotifyProfile{{ID: id, Status: catalog.ProfileVerified}}}
}

// bulkFixture has three artists: a and b each carry one homonym album; c is
// clean.
func bulkFixture() (*bulkStore, reconcileCatalog) {
	store := &bulkStore{
		associations: map[string][]artists.Association{
			"a": {
				{Kind: artists.AssociationAlbum, SpotifyID: "a-ok", CreditedArtistIDs: []string{"A"}},
				{Kind: artists.AssociationAlbum, SpotifyID: "a-bad", CreditedArtistIDs: []string{"homonym"}},
			},
			"b": {{Kind: artists.AssociationTrack, SpotifyID: "b-bad", CreditedArtistIDs: []string{"homonym"}}},
			"c": {{Kind: artists.AssociationAlbum, SpotifyID: "c-ok", CreditedArtistIDs: []string{"C"}}},
		},
		impact: map[string]artists.ImpactState{},
	}
	for _, slug := range []string{"a", "b", "c"} {
		id := map[string]string{"a": "A", "b": "B", "c": "C"}[slug]
		store.impact[slug] = artists.ImpactState{
			Baseline:    artists.MappingBaseline{SpotifyIDs: []string{id}, Source: artists.BaselineSnapshot},
			Checkpoints: []artists.Checkpoint{{RunID: 1, Status: "completed", SpotifyIDs: `["` + id + `"]`}},
		}
	}
	cat := reconcileCatalog{c: catalog.EditorialCatalog{Artists: []catalog.Artist{
		bulkArtist("c", "C"), bulkArtist("a", "A"), bulkArtist("b", "B"),
	}}}
	return store, cat
}

var bulkNow = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

func TestReconcileAllPlanListsEveryArtistAndChecksumsTheChanges(t *testing.T) {
	store, cat := bulkFixture()
	use := artists.NewReconcileAll(cat, store)
	plan, err := use.Plan(context.Background(), "artists.yaml")
	require.NoError(t, err)

	var slugs []string
	for _, r := range plan.Reports {
		slugs = append(slugs, r.Slug)
	}
	require.Equal(t, []string{"a", "b", "c"}, slugs, "every catalog artist, sorted")
	var changed []string
	for _, r := range plan.Changes() {
		changed = append(changed, r.Slug)
	}
	require.Equal(t, []string{"a", "b"}, changed)
	require.NotEmpty(t, plan.Catalog.Digest)
	require.Equal(t, artists.BulkPlanChecksum(plan.Changes(), plan.Catalog.Digest), plan.Checksum)
	require.Empty(t, store.backups)
	require.Empty(t, store.batches)

	again, err := use.Plan(context.Background(), "artists.yaml")
	require.NoError(t, err)
	require.Equal(t, plan.Checksum, again.Checksum, "deterministic")

	// The checksum covers the changed artists' plans and the catalog digest.
	require.NotEqual(t, plan.Checksum, artists.BulkPlanChecksum(plan.Changes(), "other-digest"))
	require.NotEqual(t, plan.Checksum, artists.BulkPlanChecksum(plan.Changes()[:1], plan.Catalog.Digest))
}

func TestReconcileAllApplyTakesOneBackupAndOneTransaction(t *testing.T) {
	store, cat := bulkFixture()
	use := artists.NewReconcileAll(cat, store)
	ctx := context.Background()
	plan, err := use.Plan(ctx, "artists.yaml")
	require.NoError(t, err)

	reports, err := use.Apply(ctx, artists.ReconcileAllInput{CatalogPath: "artists.yaml", ConfirmChecksum: plan.Checksum, BackupDir: "bk", Now: bulkNow})
	require.NoError(t, err)
	require.Len(t, store.backups, 1, "one backup for the whole run")
	require.Equal(t, "bk", store.backups[0].Dir)
	require.Contains(t, string(store.backups[0].Manifest), plan.Checksum)
	require.Len(t, store.batches, 1, "one transaction")
	require.Len(t, store.batches[0], 2)
	require.Equal(t, "a", store.batches[0][0].Slug)
	require.Equal(t, []string{"a-bad"}, store.batches[0][0].AlbumIDs)
	require.Equal(t, "b", store.batches[0][1].Slug)
	require.Equal(t, []string{"b-bad"}, store.batches[0][1].TrackIDs)
	require.Equal(t, store.batches[0][0].Token, store.batches[0][1].Token, "one restore token for the run")

	require.Len(t, reports, 2)
	for _, r := range reports {
		require.Equal(t, "applied", r.Status)
		require.NotNil(t, r.Backup)
		require.Equal(t, "2026-10-22T09:00:00Z", r.RetiredAt)
		require.False(t, r.DryRun)
		require.NotEmpty(t, r.Catalog.Digest)
	}
	require.Zero(t, store.cleared)

	after, err := use.Plan(ctx, "artists.yaml")
	require.NoError(t, err)
	require.Empty(t, after.Changes())
}

func TestReconcileAllApplyRefusesChangedPlan(t *testing.T) {
	store, cat := bulkFixture()
	use := artists.NewReconcileAll(cat, store)
	ctx := context.Background()
	plan, err := use.Plan(ctx, "artists.yaml")
	require.NoError(t, err)
	store.associations["c"] = append(store.associations["c"],
		artists.Association{Kind: artists.AssociationAlbum, SpotifyID: "c-bad", CreditedArtistIDs: []string{"homonym"}})

	_, err = use.Apply(ctx, artists.ReconcileAllInput{CatalogPath: "artists.yaml", ConfirmChecksum: plan.Checksum, BackupDir: "bk", Now: bulkNow})
	require.ErrorIs(t, err, artists.ErrBulkPlanChanged)
	_, err = use.Apply(ctx, artists.ReconcileAllInput{CatalogPath: "artists.yaml", BackupDir: "bk", Now: bulkNow})
	require.ErrorIs(t, err, artists.ErrBulkPlanChanged, "an unconfirmed apply is refused")
	require.Empty(t, store.backups)
	require.Empty(t, store.batches)
}

func TestReconcileAllApplyCleanPlanTakesNoBackup(t *testing.T) {
	store, cat := bulkFixture()
	delete(store.associations, "a")
	delete(store.associations, "b")
	use := artists.NewReconcileAll(cat, store)
	ctx := context.Background()
	plan, err := use.Plan(ctx, "artists.yaml")
	require.NoError(t, err)
	require.Empty(t, plan.Changes())

	reports, err := use.Apply(ctx, artists.ReconcileAllInput{CatalogPath: "artists.yaml", ConfirmChecksum: plan.Checksum, BackupDir: "bk", Now: bulkNow})
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Empty(t, store.backups)
	require.Empty(t, store.batches)
}

func TestReconcileAllApplyFailureChangesNothingAndClearsProvenance(t *testing.T) {
	store, cat := bulkFixture()
	store.failSlug = "b"
	use := artists.NewReconcileAll(cat, store)
	ctx, cancel := context.WithCancel(context.Background())
	plan, err := use.Plan(ctx, "artists.yaml")
	require.NoError(t, err)

	reports, err := use.Apply(ctx, artists.ReconcileAllInput{CatalogPath: "artists.yaml", ConfirmChecksum: plan.Checksum, BackupDir: "bk", Now: bulkNow})
	var be *artists.BulkApplyError
	require.ErrorAs(t, err, &be)
	require.Equal(t, "b", be.Slug)
	require.Len(t, reports, 2, "the planned artists are returned so outcomes can be reported")
	require.Len(t, store.backups, 1)
	require.Empty(t, store.batches)
	require.Equal(t, 1, store.cleared, "a rolled back run reads as unknown")

	// A cancelled run clears provenance even though ctx is done.
	store.failSlug = ""
	store.backups = nil
	cancelling := &cancelOnApply{bulkStore: store, cancel: cancel}
	_, err = artists.NewReconcileAll(cat, cancelling).Apply(ctx, artists.ReconcileAllInput{CatalogPath: "artists.yaml", ConfirmChecksum: plan.Checksum, BackupDir: "bk", Now: bulkNow})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 2, store.cleared)
	require.NoError(t, store.clearCtxErr, "clearing outlives the cancelled job context")
}

func TestReconcileAllBackupFailureAppliesNothing(t *testing.T) {
	store, cat := bulkFixture()
	use := artists.NewReconcileAll(cat, store)
	plan, err := use.Plan(context.Background(), "artists.yaml")
	require.NoError(t, err)
	store.backupErr = errors.New("disk full")

	_, err = use.Apply(context.Background(), artists.ReconcileAllInput{CatalogPath: "artists.yaml", ConfirmChecksum: plan.Checksum, BackupDir: "bk", Now: bulkNow})
	require.ErrorContains(t, err, "backup")
	var be *artists.BulkApplyError
	require.False(t, errors.As(err, &be), "nothing was attempted")
	require.Empty(t, store.batches)
	require.Zero(t, store.cleared, "nothing was written, nothing to clear")
}

// cancelOnApply cancels the job context while the transaction runs.
type cancelOnApply struct {
	*bulkStore
	cancel context.CancelFunc
}

func (c *cancelOnApply) ApplyReconciliations(ctx context.Context, reqs []artists.ApplyRequest) error {
	c.cancel()
	return &artists.BulkApplyError{Slug: reqs[0].Slug, Err: ctx.Err()}
}
