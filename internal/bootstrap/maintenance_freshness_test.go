package bootstrap

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

func freshnessCfg(t *testing.T) FreshnessConfig {
	t.Helper()
	acfg, _ := newPrepConfig(t)
	return FreshnessConfig{
		CatalogPath: acfg.CatalogPath, LedgerPath: acfg.LedgerPath, DBPath: acfg.DBPath,
		SnapshotPath:    acfg.SnapshotPath,
		ExportOutputDir: acfg.ExportOutputDir, ExportStaticDir: acfg.ExportStaticDir,
		ExportContentDir: acfg.ExportContentDir,
	}
}

func TestFreshnessSourceOfflineWithoutDatabase(t *testing.T) {
	cfg := freshnessCfg(t)
	f, err := maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(NewFreshnessSource(cfg))).
		Freshness(context.Background())
	require.NoError(t, err)
	require.True(t, f.Offline)
	require.False(t, f.SeedNeeded || f.SnapshotNeeded || f.ExportNeeded)
	_, err = os.Stat(cfg.DBPath)
	require.True(t, os.IsNotExist(err), "must not create the database")
}

func TestFreshnessSourceAgainstRealDatabase(t *testing.T) {
	cfg := freshnessCfg(t)
	migrateDB(t, cfg.DBPath)
	src := NewFreshnessSource(cfg)
	ctx := context.Background()

	db, ok := src.DatabaseDigest()
	require.True(t, ok)
	_, ok = src.SnapshotDigest()
	require.False(t, ok, "no snapshot yet")
	_, ok = src.SeededDigest()
	require.False(t, ok, "no completed sync")
	_, ok = src.PinnedContentSHA()
	require.False(t, ok)
	_, ok = src.LocalContentSHA()
	require.False(t, ok)

	// Snapshot written from the database matches its digest.
	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, cfg.SnapshotPath))
	_, err = database.DB().ExecContext(ctx, `INSERT INTO sync_runs(started_at, finished_at, status, market) VALUES (?, ?, 'completed', 'ES')`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339), time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339))
	require.NoError(t, err)
	require.NoError(t, database.Close())
	// The sync row changed the DB; refresh the snapshot too.
	database, err = sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, cfg.SnapshotPath))
	require.NoError(t, database.Close())

	db, _ = src.DatabaseDigest()
	snap, ok := src.SnapshotDigest()
	require.True(t, ok)
	require.Equal(t, db, snap)

	seeded, ok := src.SeededDigest()
	require.True(t, ok)
	require.Equal(t, src.CatalogDigest(), seeded)

	// Catalog edited after the seed.
	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.CatalogPath, append(body, []byte("\n# edit\n")...), 0o600))
	require.NoError(t, os.Chtimes(cfg.CatalogPath, time.Now().Add(3*time.Hour), time.Now().Add(3*time.Hour)))
	seeded, ok = src.SeededDigest()
	require.True(t, ok)
	require.NotEqual(t, src.CatalogDigest(), seeded)

	// Database changes after the snapshot.
	database, err = sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	_, err = database.DB().ExecContext(ctx, `INSERT INTO sync_runs(started_at, status, market) VALUES ('2026-01-01T00:00:00Z', 'failed', 'ES')`)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	newDB, _ := src.DatabaseDigest()
	require.NotEqual(t, db, newDB)
	snap, _ = src.SnapshotDigest()
	require.NotEqual(t, newDB, snap)

	// No export on disk: stale marker, never equal to the database digest.
	exp, ok := src.ExportDigest()
	require.True(t, ok)
	require.NotEqual(t, newDB, exp)
}
