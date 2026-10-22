package bootstrap

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func readApplied(t *testing.T, dbPath string) (artists.AppliedCatalog, bool) {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	applied, ok, err := database.AppliedCatalog(context.Background())
	require.NoError(t, err)
	return applied, ok
}

func readCheckpoint(t *testing.T, dbPath, stage string) (artists.StageCheckpoint, bool) {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	cp, ok, err := database.Checkpoint(context.Background(), stage)
	require.NoError(t, err)
	return cp, ok
}

func writeApplied(t *testing.T, dbPath string, applied artists.AppliedCatalog) {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	applied.AppliedAt = "2026-01-01T00:00:00Z"
	require.NoError(t, database.RecordApplied(context.Background(), applied))
}

func freshnessOf(t *testing.T, cfg MaintenanceActionsConfig) maintenance.Freshness {
	t.Helper()
	src := NewFreshnessSource(FreshnessConfig{
		CatalogPath: cfg.CatalogPath, LedgerPath: cfg.LedgerPath, DBPath: cfg.DBPath, SnapshotPath: cfg.SnapshotPath,
		ExportOutputDir: cfg.ExportOutputDir, ExportStaticDir: cfg.ExportStaticDir, ExportContentDir: cfg.ExportContentDir,
	})
	f, err := maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(src)).Freshness(context.Background())
	require.NoError(t, err)
	return f
}

func dbDigest(t *testing.T, dbPath string) string {
	t.Helper()
	return sum(databaseSnapshot(t, dbPath))
}

func TestSyncActionRecordsAppliedCatalog(t *testing.T) {
	cfg, paths := newSyncActionsConfig(t, credentials, withSyncFetcher(okSyncFetcher{}))
	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)

	applied, ok := readApplied(t, paths.db)
	require.True(t, ok, "a successful whole-catalog sync records what it applied")
	require.Equal(t, currentFingerprint(t, paths.catalog).Digest, applied.Digest)
	cp, ok := readCheckpoint(t, paths.db, artists.StageSync)
	require.True(t, ok)
	require.Equal(t, applied.Digest, cp.CatalogDigest)
	require.Equal(t, dbDigest(t, paths.db), cp.DBDigest)

	f := freshnessOf(t, cfg)
	require.False(t, f.SeedNeeded || f.SeedUnknown, "%+v", f)
}

// editingFetcher edits the catalog while the sync runs.
type editingFetcher struct {
	okSyncFetcher
	path string
	t    *testing.T
}

func (f editingFetcher) GetArtist(ctx context.Context, id string) (catalog.ArtistCandidate, error) {
	body, err := os.ReadFile(f.path)
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(f.path, bytes.Replace(body, []byte(`notes: ""`), []byte(`notes: "edited mid-sync"`), 1), 0o600))
	return f.okSyncFetcher.GetArtist(ctx, id)
}

func TestSyncActionRecordsTheCatalogItReadNotALaterOne(t *testing.T) {
	paths := newMaintenancePaths(t)
	before := currentFingerprint(t, paths.catalog)
	cfg, _ := newSyncActionsConfig(t, credentials)
	cfg.CatalogPath, cfg.DBPath, cfg.SnapshotPath = paths.catalog, paths.db, paths.snapshot
	cfg.Options = []MaintenanceOption{withSyncFetcher(editingFetcher{path: paths.catalog, t: t})}

	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)

	applied, ok := readApplied(t, paths.db)
	require.True(t, ok)
	require.Equal(t, before.Digest, applied.Digest, "the catalog the run read, not the one edited during it")
	require.NotEqual(t, before.Digest, currentFingerprint(t, paths.catalog).Digest)
	require.True(t, freshnessOf(t, cfg).SeedNeeded, "the mid-sync edit is still pending")
}

func TestSyncActionRateLimitRecordsNothing(t *testing.T) {
	cfg, paths := newSyncActionsConfig(t, credentials, withSyncFetcher(abortingFetcher{}))
	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusPartial, out.Status)
	_, ok := readApplied(t, paths.db)
	require.False(t, ok)
	_, ok = readCheckpoint(t, paths.db, artists.StageSync)
	require.False(t, ok)
	require.True(t, freshnessOf(t, cfg).SeedUnknown)
}

func TestReconcileThenSyncMergesTheArtist(t *testing.T) {
	cfg, paths := newSyncActionsConfig(t, credentials, withSyncFetcher(okSyncFetcher{}))
	seedHomonymAlbum(t, paths.db)
	// Applied before: the artist pointed at a homonym profile.
	c, err := yamlcatalog.NewStore().Load(context.Background(), paths.catalog)
	require.NoError(t, err)
	c.Artists[0].SpotifyID = "homonym"
	c.Artists[0].SpotifyIDs = nil
	old := artists.FingerprintCatalog(c)
	writeApplied(t, paths.db, old)
	require.True(t, freshnessOf(t, cfg).SeedNeeded)

	p := maintenance.Params{"artist": "wu-tang-clan"}
	out, err := runSpec(t, reconcileActionsByName(cfg)["reconcile-apply"], p)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	applied, _ := readApplied(t, paths.db)
	require.Equal(t, old.Digest, applied.Digest, "awaiting its resync, the artist is not applied yet")

	out, err = runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "artist", "artists": "wu-tang-clan"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	applied, _ = readApplied(t, paths.db)
	require.Equal(t, currentFingerprint(t, paths.catalog).Digest, applied.Digest)
	require.False(t, freshnessOf(t, cfg).SeedNeeded)

	// A reconcile with nothing left to do checkpoints itself.
	out, err = runSpec(t, reconcileActionsByName(cfg)["reconcile-apply"], p)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	cp, ok := readCheckpoint(t, paths.db, artists.StageReconcile)
	require.True(t, ok, out.Result.Summary)
	require.Equal(t, applied.Digest, cp.CatalogDigest)
	require.Equal(t, dbDigest(t, paths.db), cp.DBDigest)
}

func TestExportAndSnapshotNeverMarkCatalogFresh(t *testing.T) {
	t.Run("legacy database stays unknown", func(t *testing.T) {
		cfg, _ := newPrepConfig(t)
		migrateDB(t, cfg.DBPath)
		for _, name := range []string{"db-snapshot", "export", "refresh-export"} {
			out, err := runSpec(t, prepActionsByName(cfg)[name], maintenance.Params{})
			require.NoError(t, err)
			require.Equal(t, jobs.StatusSucceeded, out.Status, name+": "+out.Result.Summary)
		}
		_, ok := readApplied(t, cfg.DBPath)
		require.False(t, ok)
		f := freshnessOf(t, cfg)
		require.True(t, f.SeedUnknown)
		require.False(t, f.SeedNeeded)

		for _, stage := range []string{artists.StageSnapshot, artists.StageExport} {
			cp, ok := readCheckpoint(t, cfg.DBPath, stage)
			require.True(t, ok, stage)
			require.Equal(t, currentFingerprint(t, cfg.CatalogPath).Digest, cp.CatalogDigest)
			require.Equal(t, dbDigest(t, cfg.DBPath), cp.DBDigest)
		}
	})
	t.Run("edited catalog stays stale", func(t *testing.T) {
		cfg, _ := newPrepConfig(t)
		migrateDB(t, cfg.DBPath)
		writeApplied(t, cfg.DBPath, currentFingerprint(t, cfg.CatalogPath))
		body, err := os.ReadFile(cfg.CatalogPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(cfg.CatalogPath, bytes.Replace(body, []byte("enabled: true"), []byte("enabled: false"), 1), 0o600))
		require.True(t, freshnessOf(t, cfg).SeedNeeded)

		for _, name := range []string{"export", "db-snapshot", "refresh-export"} {
			out, err := runSpec(t, prepActionsByName(cfg)[name], maintenance.Params{})
			require.NoError(t, err)
			require.Equal(t, jobs.StatusSucceeded, out.Status, name+": "+out.Result.Summary)
		}
		require.True(t, freshnessOf(t, cfg).SeedNeeded, "export and snapshot without sync cannot make content fresh")
	})
}

func TestReleaseActionsCheckpoint(t *testing.T) {
	r := readyReleaser()
	rcfg := releaseConfig(t, r)
	paths := newMaintenancePaths(t)
	rcfg.CatalogPath = paths.catalog
	migrateDB(t, rcfg.DBPath)
	actions, err := NewReleaseActions(rcfg)
	require.NoError(t, err)

	out, err := runSpec(t, actions[0], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	cp, ok := readCheckpoint(t, rcfg.DBPath, artists.StageReleasePrepare)
	require.True(t, ok)
	require.Equal(t, currentFingerprint(t, paths.catalog).Digest, cp.CatalogDigest)
	require.Equal(t, dbDigest(t, rcfg.DBPath), cp.DBDigest)
	_, ok = readCheckpoint(t, rcfg.DBPath, artists.StageRelease)
	require.False(t, ok)
	_, ok = readApplied(t, rcfg.DBPath)
	require.False(t, ok)
}
