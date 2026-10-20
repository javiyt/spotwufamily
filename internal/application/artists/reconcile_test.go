package artists_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type reconcileCatalog struct{ c catalog.EditorialCatalog }

func (r reconcileCatalog) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return r.c, nil
}

func (r reconcileCatalog) Save(context.Context, string, catalog.EditorialCatalog) error { return nil }

type memoryReconcileStore struct {
	associations []artists.Association
	impact       artists.ImpactState
	applied      []artists.ApplyRequest
	backups      []artists.BackupRequest
	backupErr    error
	applyErr     error
}

func (m *memoryReconcileStore) ActiveAssociations(context.Context, string) ([]artists.Association, error) {
	return m.associations, nil
}

func (m *memoryReconcileStore) Impact(context.Context, string) (artists.ImpactState, error) {
	return m.impact, nil
}

func (m *memoryReconcileStore) PendingReconciliations(context.Context) ([]artists.PendingReconciliation, error) {
	return nil, nil
}

func (m *memoryReconcileStore) BackupDatabase(_ context.Context, request artists.BackupRequest) (artists.BackupInfo, error) {
	m.backups = append(m.backups, request)
	return artists.BackupInfo{Path: request.Dir + "/" + request.Name + ".db", SHA256: "backup-sha", SnapshotChecksum: "snapshot-sha"}, m.backupErr
}

func (m *memoryReconcileStore) ApplyReconciliation(_ context.Context, request artists.ApplyRequest) error {
	m.applied = append(m.applied, request)
	return m.applyErr
}

func (m *memoryReconcileStore) RestoreAssociations(context.Context, string, string) (int, int, error) {
	return 0, 0, nil
}

func driftedFixture() (*memoryReconcileStore, reconcileCatalog) {
	store := &memoryReconcileStore{
		associations: []artists.Association{
			{Kind: artists.AssociationAlbum, SpotifyID: "wu", CreditedArtistIDs: []string{"linked"}},
			{Kind: artists.AssociationAlbum, SpotifyID: "homonym", CreditedArtistIDs: []string{"rejected"}, SharedWith: []string{"other"}},
			{Kind: artists.AssociationTrack, SpotifyID: "nocredits"},
		},
		impact: artists.ImpactState{
			Baseline: artists.MappingBaseline{SpotifyIDs: []string{"rejected"}, Source: artists.BaselineSnapshot},
			Checkpoints: []artists.Checkpoint{
				{RunID: 3, Status: "completed", SpotifyIDs: `["rejected"]`},
			},
		},
	}
	cat := reconcileCatalog{c: catalog.EditorialCatalog{Artists: []catalog.Artist{{Slug: "a", Enabled: true, SpotifyID: "linked", SpotifyProfiles: []catalog.SpotifyProfile{{ID: "linked", Status: catalog.ProfileVerified}}}}}}

	return store, cat
}

func TestReconcileDryRunDiffsIdentityAndProposesReversibleDecisions(t *testing.T) {
	store, cat := driftedFixture()

	report, err := artists.NewReconcileArtist(cat, store).Run(context.Background(), artists.ReconcileInput{Slug: "a"})
	require.NoError(t, err)
	require.True(t, report.DryRun)
	require.Equal(t, artists.IdentityDiff{
		BaselineSource: artists.BaselineSnapshot,
		Previous:       []string{"rejected"}, Current: []string{"linked"},
		Added: []string{"linked"}, Removed: []string{"rejected"}, Kept: []string{}, Drift: true,
	}, report.Identity)
	require.Equal(t, artists.ReconcileCounts{Albums: 1}, report.SharedOrphans)
	require.Equal(t, artists.ReconcileCounts{Tracks: 1}, report.Unverifiable)
	require.Equal(t, []string{"rejected"}, report.CreditedArtistIDs)
	require.Len(t, report.StaleCheckpoints, 1)
	require.True(t, report.ResyncRequired)
	require.NotEmpty(t, report.PlanChecksum)

	actions := map[string]int{}
	for _, d := range report.Decisions {
		actions[d.Action]++
	}
	require.Equal(t, 1, actions[artists.ActionUnlink])
	require.Equal(t, 1, actions[artists.ActionPreserveShared])
	require.Equal(t, 1, actions[artists.ActionKeep])
	require.Equal(t, 1, actions[artists.ActionInvalidateCheckpt])
	require.Equal(t, 1, actions[artists.ActionResync])
	require.Empty(t, store.applied)
	require.Empty(t, store.backups, "a dry run takes no backup and writes nothing")
}

func TestReconcileReportsAreDeterministic(t *testing.T) {
	store, cat := driftedFixture()
	use := artists.NewReconcileArtist(cat, store)
	first, err := use.Run(context.Background(), artists.ReconcileInput{Slug: "a"})
	require.NoError(t, err)
	second, err := use.Run(context.Background(), artists.ReconcileInput{Slug: "a"})
	require.NoError(t, err)

	a, err := artists.FormatReconcileJSON(first)
	require.NoError(t, err)
	b, err := artists.FormatReconcileJSON(second)
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
	require.Equal(t, string(artists.FormatReconcileMarkdown(first)), string(artists.FormatReconcileMarkdown(second)))
	require.Contains(t, string(artists.FormatReconcileMarkdown(first)), "- rejected\n+ linked")
}

func TestReconcileApplyRequiresConfirmedPlanAndBackup(t *testing.T) {
	store, cat := driftedFixture()
	use := artists.NewReconcileArtist(cat, store)
	ctx := context.Background()
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "a"})
	require.NoError(t, err)

	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "a", Apply: true, BackupDir: "b"})
	require.ErrorContains(t, err, "--confirm "+dry.PlanChecksum)
	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: "stale", BackupDir: "b"})
	require.ErrorContains(t, err, "plan changed since review")
	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: dry.PlanChecksum})
	require.ErrorContains(t, err, "backup directory")
	require.Empty(t, store.backups)
	require.Empty(t, store.applied)

	applied, err := use.Run(ctx, artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: "b", Now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Equal(t, artists.StatusAwaitingResync, applied.Status)
	require.Equal(t, "2026-10-20T09:00:00Z", applied.RetiredAt)
	require.Len(t, store.backups, 1)
	require.Contains(t, string(store.backups[0].Manifest), dry.PlanChecksum)
	require.Len(t, store.applied, 1)
	require.Equal(t, []string{"homonym"}, store.applied[0].AlbumIDs)
	require.Empty(t, store.applied[0].TrackIDs, "unverifiable associations are kept")
	require.Equal(t, `["linked"]`, store.applied[0].ResyncFingerprint)
	require.Equal(t, "backup-sha", store.applied[0].Backup.SHA256)
}

func TestReconcileApplyDoesNotChangeAnythingWhenBackupOrApplyFails(t *testing.T) {
	store, cat := driftedFixture()
	use := artists.NewReconcileArtist(cat, store)
	ctx := context.Background()
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "a"})
	require.NoError(t, err)

	store.backupErr = errors.New("disk full")
	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: "b"})
	require.ErrorContains(t, err, "backup before reconcile")
	require.Empty(t, store.applied, "no change without a backup")

	store.backupErr = nil
	store.applyErr = errors.New("boom")
	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: "b"})
	require.ErrorContains(t, err, "database unchanged")
}

func TestReconcileApplyIsIdempotentOnceReconciled(t *testing.T) {
	store := &memoryReconcileStore{
		associations: []artists.Association{{Kind: artists.AssociationAlbum, SpotifyID: "wu", CreditedArtistIDs: []string{"linked"}}},
		impact: artists.ImpactState{
			Baseline:    artists.MappingBaseline{SpotifyIDs: []string{"linked"}, Source: artists.BaselineSnapshot},
			Checkpoints: []artists.Checkpoint{{RunID: 4, Status: "completed", SpotifyIDs: `["linked"]`}},
		},
	}
	cat := reconcileCatalog{c: catalog.EditorialCatalog{Artists: []catalog.Artist{{Slug: "a", Enabled: true, SpotifyID: "linked", SpotifyProfiles: []catalog.SpotifyProfile{{ID: "linked", Status: catalog.ProfileVerified}}}}}}

	report, err := artists.NewReconcileArtist(cat, store).Run(context.Background(), artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: "any-old-token", BackupDir: "b"})
	require.NoError(t, err)
	require.Equal(t, artists.StatusClean, report.Status)
	require.Empty(t, store.backups)
	require.Empty(t, store.applied)
}

func TestReconcileWaitingForResyncDoesNotRepeatTheApplication(t *testing.T) {
	store, cat := driftedFixture()
	store.associations = store.associations[:1]
	store.impact.Checkpoints = nil
	store.impact.Reconciliation = &artists.PendingReconciliation{Slug: "a", Status: artists.StatusAwaitingResync, TargetIDs: []string{"linked"}}

	report, err := artists.NewReconcileArtist(cat, store).Run(context.Background(), artists.ReconcileInput{Slug: "a", Apply: true, ConfirmChecksum: "x", BackupDir: "b"})
	require.NoError(t, err)
	require.Equal(t, artists.StatusAwaitingResync, report.Status)
	require.True(t, report.ResyncRequired)
	require.Empty(t, store.applied)
}

func TestReconcileClassifiesAndKeepsUnverifiable(t *testing.T) {
	store, cat := driftedFixture()

	dry, err := artists.NewReconcileArtist(cat, store).Run(context.Background(), artists.ReconcileInput{Slug: "a"})
	require.NoError(t, err)
	require.Equal(t, artists.ReconcileCounts{Tracks: 1}, dry.Unverifiable)
	require.Equal(t, artists.ReconcileCounts{Albums: 1, Tracks: 1}, dry.After)
}

func TestReconcileRejectsUnknownArtistAndConflictingModes(t *testing.T) {
	use := artists.NewReconcileArtist(reconcileCatalog{}, &memoryReconcileStore{})
	_, err := use.Run(context.Background(), artists.ReconcileInput{Slug: "missing"})
	require.ErrorContains(t, err, "not found")
	_, err = use.Run(context.Background(), artists.ReconcileInput{Slug: "a", Apply: true, RestoreRetiredAt: "x"})
	require.ErrorContains(t, err, "cannot be combined")
}

func TestProfileAuditReportsRejectedHistoryWithoutLinkedProfiles(t *testing.T) {
	cat := reconcileCatalog{c: catalog.EditorialCatalog{Artists: []catalog.Artist{{
		Slug: "k-g-b", Name: "K.G.B.",
		SpotifyProfiles: []catalog.SpotifyProfile{{
			ID: "2gKwx0Lm0JoOrN2mY9Z0PG", Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-10-20T08:20:21Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/x", Rationale: "mixed homonym"}},
		}},
	}}}}

	report, err := artists.NewAuditProfiles(cat, nil, nil).Run(context.Background(), artists.AuditProfilesOptions{ArtistSlug: "k-g-b", Offline: true})
	require.NoError(t, err)
	require.Empty(t, report.Profiles)
	require.Len(t, report.RejectedProfiles, 1)
	require.Equal(t, "2gKwx0Lm0JoOrN2mY9Z0PG", report.RejectedProfiles[0].SpotifyID)
	markdown := string(artists.FormatProfileAuditMarkdown(report))
	require.Contains(t, markdown, "Rejected profiles (history)")
	require.Contains(t, markdown, "mixed homonym")
}
