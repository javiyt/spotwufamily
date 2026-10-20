package bootstrap

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

func newPrepConfig(t *testing.T) (MaintenanceActionsConfig, string) {
	t.Helper()
	cfg, paths := newActionsConfig(t, envMap(nil))
	site := filepath.Join(filepath.Dir(paths.catalog), "site")
	cfg.SnapshotPath = paths.snapshot
	cfg.ExportOutputDir = filepath.Join(site, "data", "generated")
	cfg.ExportStaticDir = filepath.Join(site, "static")
	cfg.ExportContentDir = filepath.Join(site, "content", "generated")
	return cfg, filepath.Dir(paths.catalog)
}

func prepActionsByName(cfg MaintenanceActionsConfig) map[string]maintenance.Action {
	out := map[string]maintenance.Action{}
	for _, a := range NewPublishPrepActions(cfg) {
		out[a.Name()] = a
	}
	return out
}

func migrateDB(t *testing.T, path string) {
	t.Helper()
	database, err := openMigrated(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, database.Close())
}

func requireNoTempFiles(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		name := d.Name()
		if strings.Contains(name, ".tmp") || strings.HasPrefix(name, ".export-staging") {
			t.Errorf("leftover temporary file %s", path)
		}
		return nil
	}))
}

func TestPublishPrepActionsAreRegistered(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	var names []string
	for _, a := range NewPublishPrepActions(cfg) {
		names = append(names, a.Name())
	}
	require.Equal(t, []string{"db-verify", "db-snapshot", "export", "audit", "refresh-export"}, names)
}

func TestPublishPrepActionsUnavailableWithoutDatabase(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	for _, a := range NewPublishPrepActions(cfg) {
		ok, reason := a.Available()
		require.False(t, ok, a.Name())
		require.Equal(t, "No database found; run the init workflow first", reason)
	}
	require.NoFileExists(t, cfg.DBPath, "checking availability never creates the database")
}

func TestSnapshotActionKeepsPreviousVersion(t *testing.T) {
	cfg, dir := newPrepConfig(t)
	migrateDB(t, cfg.DBPath)
	require.NoError(t, os.WriteFile(cfg.SnapshotPath, []byte("previous snapshot bytes"), 0o600))
	require.NoError(t, os.Chmod(cfg.SnapshotPath, 0o600))
	snapshot := prepActionsByName(cfg)["db-snapshot"]

	out, err := runSpec(t, snapshot, maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	prev, err := os.ReadFile(cfg.SnapshotPath + ".prev")
	require.NoError(t, err)
	require.Equal(t, "previous snapshot bytes", string(prev))
	for _, path := range []string{cfg.SnapshotPath, cfg.SnapshotPath + ".prev"} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s keeps the snapshot's mode", filepath.Base(path))
	}

	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	want, err := database.Snapshot(context.Background())
	require.NoError(t, err)
	require.NoError(t, database.Close())
	got, err := sqliteadapter.ReadSnapshot(cfg.SnapshotPath)
	require.NoError(t, err)
	require.Equal(t, want, got)

	// An unchanged snapshot is not rewritten and .prev keeps its content.
	out, err = runSpec(t, snapshot, maintenance.Params{})
	require.NoError(t, err)
	require.Contains(t, out.Result.Summary, "unchanged")
	prev, err = os.ReadFile(cfg.SnapshotPath + ".prev")
	require.NoError(t, err)
	require.Equal(t, "previous snapshot bytes", string(prev))
	requireNoTempFiles(t, dir)
}

func TestSnapshotActionCancelledLeavesPreviousSnapshot(t *testing.T) {
	cfg, dir := newPrepConfig(t)
	migrateDB(t, cfg.DBPath)
	require.NoError(t, os.WriteFile(cfg.SnapshotPath, []byte("previous snapshot bytes"), 0o644))
	spec, err := prepActionsByName(cfg)["db-snapshot"].Spec(context.Background(), maintenance.Params{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = spec.Run(ctx, nopReporter{})
	require.ErrorIs(t, err, context.Canceled)
	got, err := os.ReadFile(cfg.SnapshotPath)
	require.NoError(t, err)
	require.Equal(t, "previous snapshot bytes", string(got))
	require.NoFileExists(t, cfg.SnapshotPath+".prev")
	requireNoTempFiles(t, dir)
}

func TestRefreshExportEndToEnd(t *testing.T) {
	cfg, dir := newPrepConfig(t)
	migrateDB(t, cfg.DBPath)
	actions := prepActionsByName(cfg)

	// Before anything was exported the audit reports what is missing.
	out, err := runSpec(t, actions["audit"], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusPartial, out.Status, out.Result.Summary)

	out, err = runSpec(t, actions["refresh-export"], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	require.FileExists(t, cfg.SnapshotPath)
	require.FileExists(t, filepath.Join(cfg.ExportOutputDir, "catalog-summary.json"))
	require.FileExists(t, filepath.Join(cfg.ExportStaticDir, "search-index.json"))
	for _, row := range out.Result.Report.Rows {
		require.NotContains(t, row[2], dir, "report rows never show absolute paths")
		if strings.HasPrefix(row[0], "audit: ") {
			require.Equal(t, "ok", row[1], row)
		}
	}
	requireNoTempFiles(t, dir)

	// A stale export is detected by the audit's export-fresh check.
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ExportOutputDir, "catalog-summary.json"), []byte("{}"), 0o644))
	out, err = runSpec(t, actions["audit"], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusPartial, out.Status)
	require.Contains(t, out.Result.Report.Rows, []string{"export-fresh", "stale",
		"1 file differs from the database (data/generated/catalog-summary.json); run export"})

	out, err = runSpec(t, actions["db-verify"], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
}

func TestPublishPrepPreviewShowsNamesNotPaths(t *testing.T) {
	cfg, dir := newPrepConfig(t)
	migrateDB(t, cfg.DBPath)
	for _, a := range NewPublishPrepActions(cfg) {
		pv, err := a.Preview(context.Background(), maintenance.Params{})
		require.NoError(t, err)
		for _, line := range pv.Scope {
			require.NotContains(t, line.Value, dir, a.Name())
		}
	}
	pv, err := prepActionsByName(cfg)["export"].Preview(context.Background(), maintenance.Params{})
	require.NoError(t, err)
	require.Contains(t, pv.Scope[2].Value, "data/generated/")
	require.Contains(t, pv.Scope[2].Value, "static/search-index.json")
}

func TestPublishPrepDigestTracksDatabaseSnapshotAndExport(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	migrateDB(t, cfg.DBPath)
	digest := publishPrepDigest(cfg)
	ctx := context.Background()
	d1, err := digest(ctx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.SnapshotPath, []byte("x"), 0o644))
	d2, err := digest(ctx)
	require.NoError(t, err)
	require.NotEqual(t, d1, d2)
	require.NoError(t, os.MkdirAll(cfg.ExportOutputDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ExportOutputDir, "a.json"), []byte("x"), 0o644))
	d3, err := digest(ctx)
	require.NoError(t, err)
	require.NotEqual(t, d2, d3)
	d4, err := digest(ctx)
	require.NoError(t, err)
	require.Equal(t, d3, d4)

	// Read-only actions leave the fingerprint alone, so they never make a
	// pending preview stale.
	out, err := runSpec(t, prepActionsByName(cfg)["db-verify"], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusPartial, out.Status, "the snapshot written above is not a valid snapshot")
	require.Contains(t, out.Result.Report.Rows, []string{"snapshot", "unreadable", "catalog.snapshot.sql.gz could not be read; run db-snapshot"})
	_, err = runSpec(t, prepActionsByName(cfg)["audit"], maintenance.Params{})
	require.NoError(t, err)
	d5, err := digest(ctx)
	require.NoError(t, err)
	require.Equal(t, d4, d5)
}

func TestRefreshExportRefusesWhileDatabaseIsLocked(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	migrateDB(t, cfg.DBPath)
	release, err := filesystem.NewResourceLock(cfg.DBPath + ".lock").TryLock()
	require.NoError(t, err)
	defer release()

	_, err = runSpec(t, prepActionsByName(cfg)["refresh-export"], maintenance.Params{})
	var se *maintenance.SafeError
	require.True(t, errors.As(err, &se), "got %v", err)
	require.Equal(t, "conflict", se.Code)
	require.NoFileExists(t, cfg.SnapshotPath)
}

func TestVerifyActionFailsOnUnmigratedDatabase(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	_, err = runSpec(t, prepActionsByName(cfg)["db-verify"], maintenance.Params{})
	var se *maintenance.SafeError
	require.True(t, errors.As(err, &se), "got %v", err)
	require.Equal(t, "check_failed", se.Code)
	require.NotContains(t, se.Message, cfg.DBPath)
}

func TestGitignoreCoversSnapshotPrevAndStaging(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(data), "*.snapshot.sql.gz.prev")
	require.Contains(t, string(data), ".export-staging-*/")
}
