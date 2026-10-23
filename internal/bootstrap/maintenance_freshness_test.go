package bootstrap

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/artists"

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
	require.False(t, ok, "no applied catalog recorded")
	_, applied := src.AppliedChanges()
	require.False(t, applied)
	_, ok = src.PinnedContentSHA()
	require.False(t, ok)
	_, ok = src.LocalContentSHA()
	require.False(t, ok)

	// Snapshot written from the database matches its digest.
	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, cfg.SnapshotPath))
	// Record provenance for the current catalog: that is what "seeded" means.
	current := currentFingerprint(t, cfg.CatalogPath)
	require.NoError(t, database.RecordApplied(ctx, current))
	require.NoError(t, database.Close())
	// Recording provenance is excluded from snapshots, so it does not move
	// the database digest; refresh the snapshot anyway.
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
	changes, applied := src.AppliedChanges()
	require.True(t, applied)
	require.Empty(t, changes)

	// Catalog edited after the seed.
	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.CatalogPath, append(body, []byte("\n# edit\n")...), 0o600))
	require.NoError(t, os.Chtimes(cfg.CatalogPath, time.Now().Add(3*time.Hour), time.Now().Add(3*time.Hour)))
	seeded, ok = src.SeededDigest()
	require.True(t, ok)
	require.Equal(t, current.Digest, seeded)

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

func TestFreshnessSourceCorruptDatabaseIsUnreadableNotOffline(t *testing.T) {
	cfg := freshnessCfg(t)
	src := NewFreshnessSource(cfg)
	require.Equal(t, maintenance.DatabaseAbsent, src.DatabaseState())
	require.NoError(t, os.WriteFile(cfg.DBPath, []byte("not a sqlite database, just garbage bytes"), 0o600))
	require.Equal(t, maintenance.DatabaseUnreadable, src.DatabaseState())
	f, err := maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(src)).Freshness(context.Background())
	require.NoError(t, err)
	require.True(t, f.Unreadable)
	require.False(t, f.Offline)
	require.Contains(t, f.Reasons, "Database could not be read")
}

func currentFingerprint(t *testing.T, catalogPath string) artists.AppliedCatalog {
	t.Helper()
	c, err := yamlcatalog.NewStore().Load(context.Background(), catalogPath)
	require.NoError(t, err)
	applied := artists.FingerprintCatalog(c)
	applied.AppliedAt = "2026-01-01T00:00:00Z"
	return applied
}

func recordApplied(t *testing.T, cfg FreshnessConfig) {
	t.Helper()
	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.RecordApplied(context.Background(), currentFingerprint(t, cfg.CatalogPath)))
	require.NoError(t, database.Close())
}

func TestSeededDigestIgnoresMtime(t *testing.T) {
	cfg := freshnessCfg(t)
	migrateDB(t, cfg.DBPath)
	recordApplied(t, cfg)
	src := NewFreshnessSource(cfg)
	svc := maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(src))
	f, err := svc.Freshness(context.Background())
	require.NoError(t, err)
	require.False(t, f.SeedNeeded || f.SeedUnknown)

	// Edit an editorial field while preserving the file mtime.
	info, err := os.Stat(cfg.CatalogPath)
	require.NoError(t, err)
	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	edited := bytes.Replace(body, []byte("enabled: true"), []byte("enabled: false"), 1)
	require.NotEqual(t, body, edited, "test catalog must contain an enabled flag")
	require.NoError(t, os.WriteFile(cfg.CatalogPath, edited, 0o600))
	require.NoError(t, os.Chtimes(cfg.CatalogPath, info.ModTime(), info.ModTime()))

	f, err = svc.Freshness(context.Background())
	require.NoError(t, err)
	require.True(t, f.SeedNeeded)
	require.NotEmpty(t, f.Pending)
}

func TestLegacyDBIsUnknown(t *testing.T) {
	cfg := freshnessCfg(t)
	migrateDB(t, cfg.DBPath)
	f, err := maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(NewFreshnessSource(cfg))).
		Freshness(context.Background())
	require.NoError(t, err)
	require.True(t, f.SeedUnknown)
	require.False(t, f.SeedNeeded)
	require.Contains(t, f.Reasons, "Catalog state of the database is unknown; run a check")
}
