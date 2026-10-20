package sqlite_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

var reconcileNow = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// replacementCatalog re-points streetlife to the verified Wu profile while the
// database was synced with the jazz homonym.
func replacementCatalog() fixedCatalog {
	return fixedCatalog{c: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{
			Slug: "streetlife", Name: "Streetlife", SpotifyID: streetlifeWuID, Enabled: true,
			SpotifyProfiles: []catalog.SpotifyProfile{{ID: streetlifeWuID, Status: catalog.ProfileVerified}},
		},
		{Slug: "method-man", Name: "Method Man", SpotifyID: methodManID, Enabled: true},
	}}}
}

// seedDriftedFixture adds the last-synced mapping (old homonym ID), a completed
// sync checkpoint bound to it and a metadata refresh cache on top of the
// homonym fixture.
func seedDriftedFixture(t *testing.T, ctx context.Context, dbPath string) (*sqliteadapter.Database, func(string) int) {
	t.Helper()
	store, count := seedHomonymFixture(t, ctx, dbPath)
	database := store.(*sqliteadapter.Database)
	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artist_spotify_ids(artist_slug, spotify_id, position, primary_id) VALUES ('streetlife', '`+streetlifeOriginalsID+`', 0, 1);
INSERT INTO artist_mapping_snapshots(artist_slug, spotify_ids, checksum, recorded_at, source) VALUES ('streetlife', '["`+streetlifeOriginalsID+`"]', 'x', '2026-10-01T00:00:00Z', 'sync');
INSERT INTO sync_runs(id, started_at, status, market) VALUES (7, '2026-10-01T00:00:00Z', 'partial', 'ES');
INSERT INTO sync_run_artists(sync_run_id, artist_slug, spotify_ids, status, finished_at) VALUES (7, 'streetlife', '["`+streetlifeOriginalsID+`"]', 'completed', '2026-10-01T00:10:00Z');
INSERT INTO artist_metadata_refreshes(artist_slug, refreshed_at, spotify_ids) VALUES ('streetlife', '2026-10-01T00:00:00Z', '`+streetlifeOriginalsID+`');`)
	require.NoError(t, err)

	return database, count
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func TestReconcileDryRunIsWriteFreeAndReportsEveryImpact(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	database, _ := seedDriftedFixture(t, ctx, dbPath)
	before := fileDigest(t, dbPath)

	report, err := artists.NewReconcileArtist(replacementCatalog(), database).Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	require.Equal(t, before, fileDigest(t, dbPath), "a dry run must not write to the database file")

	require.Equal(t, artists.BaselineSnapshot, report.Identity.BaselineSource)
	require.Equal(t, []string{streetlifeOriginalsID}, report.Identity.Removed)
	require.Equal(t, []string{streetlifeWuID}, report.Identity.Added)
	require.Equal(t, []int64{7}, []int64{report.StaleCheckpoints[0].RunID})
	require.True(t, report.MetadataStale)
	require.True(t, report.ResyncRequired)
	require.Equal(t, artists.ReconcileCounts{Albums: 1}, report.SharedOrphans)
	require.Contains(t, report.CreditedArtistIDs, streetlifeOriginalsID)
	require.Equal(t, artists.StatusDrifted, report.Status)
}

func TestReconcileApplyUnlinksOnlyOwnEdgesAndKeepsSharedHistory(t *testing.T) {
	ctx := context.Background()
	database, count := seedDriftedFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	use := artists.NewReconcileArtist(replacementCatalog(), database)
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)

	applied, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: t.TempDir(), Now: reconcileNow})
	require.NoError(t, err)
	require.Equal(t, artists.StatusAwaitingResync, applied.Status)

	require.Equal(t, 2, count(`SELECT COUNT(*) FROM artist_albums WHERE configured_artist_slug = 'method-man' AND active = 1`), "the shared compilation stays with its other artist")
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_tracks WHERE configured_artist_slug = 'method-man' AND active = 1`))
	for table, want := range map[string]int{"albums": 4, "tracks": 4, "artists": 4, "album_artists": 4, "track_artists": 4, "album_tracks": 4} {
		require.Equal(t, want, count(`SELECT COUNT(*) FROM `+table), "%s rows must never be removed", table)
	}
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM sync_run_artists WHERE status = 'invalidated@2026-10-20T09:00:00Z'`))
	require.Equal(t, 0, count(`SELECT COUNT(*) FROM artist_metadata_refreshes`))
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_mapping_snapshots WHERE spotify_ids LIKE '%`+streetlifeOriginalsID+`%'`), "the snapshot advances only after the resync")
}

func TestReconcileBackupHoldsPreChangeStateAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	database, _ := seedDriftedFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	use := artists.NewReconcileArtist(replacementCatalog(), database)
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	backupDir := t.TempDir()

	applied, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: backupDir, Now: reconcileNow})
	require.NoError(t, err)
	require.Equal(t, fileDigest(t, applied.Backup.Path), applied.Backup.SHA256)

	backup, err := sqliteadapter.Open(applied.Backup.Path)
	require.NoError(t, err)
	defer func() { require.NoError(t, backup.Close()) }()
	var active int
	require.NoError(t, backup.DB().QueryRow(`SELECT COUNT(*) FROM artist_albums WHERE configured_artist_slug = 'streetlife' AND active = 1`).Scan(&active))
	require.Equal(t, 3, active, "the backup predates the change")

	var manifest struct {
		DatabaseSHA256 string          `json:"database_sha256"`
		SnapshotSHA256 string          `json:"snapshot_sha256"`
		Plan           json.RawMessage `json:"plan"`
	}
	data, err := os.ReadFile(applied.Backup.ManifestPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, applied.Backup.SHA256, manifest.DatabaseSHA256)
	require.Equal(t, applied.Backup.SnapshotChecksum, manifest.SnapshotSHA256)
	require.Contains(t, string(manifest.Plan), dry.PlanChecksum)

	_, err = database.BackupDatabase(ctx, artists.BackupRequest{Dir: backupDir, Name: filepath.Base(applied.Backup.Path[:len(applied.Backup.Path)-3])})
	require.ErrorContains(t, err, "refusing to overwrite")
}

func TestReconcileApplyRollsBackEverythingOnTransactionFailure(t *testing.T) {
	ctx := context.Background()
	database, count := seedDriftedFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	// Fail the checkpoint invalidation, the step after the associations were retired.
	_, err := database.DB().ExecContext(ctx, `
CREATE TRIGGER fail_checkpoint_invalidation BEFORE UPDATE ON sync_run_artists
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	require.NoError(t, err)
	use := artists.NewReconcileArtist(replacementCatalog(), database)
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)

	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: t.TempDir(), Now: reconcileNow})
	require.ErrorContains(t, err, "injected failure")
	require.ErrorContains(t, err, "database unchanged")
	require.Equal(t, 5, count(`SELECT COUNT(*) FROM artist_albums WHERE active = 1`), "retired associations were rolled back")
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_metadata_refreshes`))
	require.Equal(t, 0, count(`SELECT COUNT(*) FROM artist_reconciliations`))
}

func TestReconcileRepeatApplyIsIdempotentAndResyncCompletesIt(t *testing.T) {
	ctx := context.Background()
	database, count := seedDriftedFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	use := artists.NewReconcileArtist(replacementCatalog(), database)
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	backupDir := t.TempDir()
	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: backupDir, Now: reconcileNow})
	require.NoError(t, err)

	again, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: backupDir, Now: reconcileNow.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, artists.StatusAwaitingResync, again.Status)
	require.Nil(t, again.Backup, "a repeated apply must not take another backup or change anything")
	entries, err := os.ReadDir(backupDir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "one database copy and its manifest")

	pending, err := database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, artists.StatusAwaitingResync, pending[0].Status)

	// A rate-limited resync records why it stopped and stays pending.
	require.NoError(t, database.RecordReconciliationError(ctx, "streetlife", "spotify rate limit"))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Equal(t, "spotify rate limit", pending[0].LastError)

	// A checkpoint for other IDs does not complete it; the replacement IDs do.
	streetlife := catalog.Artist{Slug: "streetlife", SpotifyIDs: []string{streetlifeWuID}}
	require.NoError(t, database.SaveArtistSyncCheckpoint(ctx, 7, catalog.Artist{Slug: "streetlife", SpotifyIDs: []string{"other"}}, "completed", catalogsync.SyncStats{}, reconcileNow))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, database.SaveArtistSyncCheckpoint(ctx, 7, streetlife, "failed", catalogsync.SyncStats{}, reconcileNow))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed sync leaves the warning in place")

	require.NoError(t, database.SaveArtistSyncCheckpoint(ctx, 7, streetlife, "completed", catalogsync.SyncStats{}, reconcileNow))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_mapping_snapshots WHERE spotify_ids = '["`+streetlifeWuID+`"]'`))

	final, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	require.Equal(t, artists.StatusClean, final.Status)
	require.False(t, final.Identity.Drift)
}

func TestReconcileRestoreRevertsAssociationsCheckpointsAndCache(t *testing.T) {
	ctx := context.Background()
	database, count := seedDriftedFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	use := artists.NewReconcileArtist(replacementCatalog(), database)
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	applied, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: t.TempDir(), Now: reconcileNow})
	require.NoError(t, err)

	restored, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", RestoreRetiredAt: applied.RetiredAt})
	require.NoError(t, err)
	require.Equal(t, &artists.ReconcileCounts{Albums: 2, Tracks: 2}, restored.Restored)
	require.Equal(t, 5, count(`SELECT COUNT(*) FROM artist_albums WHERE active = 1`))
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM sync_run_artists WHERE status = 'completed'`))
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_metadata_refreshes`))

	again, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	require.Equal(t, dry.PlanChecksum, again.PlanChecksum, "restoring returns to the state the plan was computed from")
}

func TestSeedingChangedMappingFlagsDriftUntilReconciled(t *testing.T) {
	ctx := context.Background()
	database := openMigratedDatabase(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	defer func() { require.NoError(t, database.Close()) }()
	artist := func(id string) []catalog.Artist {
		return []catalog.Artist{{Slug: "streetlife", Name: "Streetlife", Category: "affiliate_artist", SpotifyID: id}}
	}

	require.NoError(t, database.SaveConfiguredArtists(ctx, artist(streetlifeOriginalsID)))
	pending, err := database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Empty(t, pending, "the first seed only records the baseline")

	require.NoError(t, database.SaveConfiguredArtists(ctx, artist(streetlifeWuID)))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, artists.StatusDrifted, pending[0].Status)
	require.Equal(t, []string{streetlifeOriginalsID}, pending[0].PreviousIDs)
	require.Equal(t, []string{streetlifeWuID}, pending[0].TargetIDs)

	require.NoError(t, database.SaveConfiguredArtists(ctx, artist(streetlifeWuID)))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "reseeding does not hide the drift")

	require.NoError(t, database.SaveConfiguredArtists(ctx, artist(streetlifeOriginalsID)))
	pending, err = database.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Empty(t, pending, "reverting the YAML restores consistency")
}

func TestReconciliationStateSurvivesSnapshotRebuild(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, _ := seedDriftedFixture(t, ctx, filepath.Join(dir, "catalog.db"))
	use := artists.NewReconcileArtist(replacementCatalog(), database)
	dry, err := use.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	_, err = use.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, ConfirmChecksum: dry.PlanChecksum, BackupDir: filepath.Join(dir, "backups"), Now: reconcileNow})
	require.NoError(t, err)

	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql.gz")
	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	_, err = database.Verify(ctx, migrations)
	require.NoError(t, err)

	rebuilt := openMigratedDatabase(t, ctx, filepath.Join(dir, "rebuilt.db"))
	defer func() { require.NoError(t, rebuilt.Close()) }()
	require.NoError(t, sqliteadapter.RestoreSnapshot(ctx, rebuilt.DB(), snapshotPath))
	_, err = rebuilt.Verify(ctx, migrations)
	require.NoError(t, err)

	original, err := database.Snapshot(ctx)
	require.NoError(t, err)
	again, err := rebuilt.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, string(original), string(again), "the rebuilt database is logically identical")
	pending, err := rebuilt.PendingReconciliations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}
