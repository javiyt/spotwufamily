package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/stretchr/testify/require"
)

func TestBackupToCreatesVerifiableCopy(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()
	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order)
VALUES ('wu-tang-clan', 'Wu-Tang Clan', '34EP7KEpOjXcM2TCat1ISk', 'core', 1, 1)`)
	require.NoError(t, err)

	restore := sqliteadapter.SetBackupClock(func() time.Time {
		return time.Date(2026, 10, 20, 9, 8, 7, 0, time.FixedZone("CEST", 2*3600))
	})
	defer restore()

	dir := filepath.Join(t.TempDir(), "nightly backups")
	name, err := database.BackupTo(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, "catalog-20261020T070807Z.db", name)
	require.Equal(t, name, filepath.Base(name), "only the file name is returned")

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	info, err := os.Stat(filepath.Join(dir, name))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	copyDB, err := sqliteadapter.Open(filepath.Join(dir, name))
	require.NoError(t, err)
	defer func() { require.NoError(t, copyDB.Close()) }()
	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	_, err = copyDB.Verify(ctx, migrations)
	require.NoError(t, err)
	var count int
	require.NoError(t, copyDB.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM configured_artists`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestBackupToNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	restore := sqliteadapter.SetBackupClock(func() time.Time {
		return time.Date(2026, 10, 20, 7, 8, 7, 0, time.UTC)
	})
	defer restore()

	dir := t.TempDir()
	existing := filepath.Join(dir, "catalog-20261020T070807Z.db")
	require.NoError(t, os.WriteFile(existing, []byte("keep me"), 0o600))

	name, err := database.BackupTo(ctx, dir)
	require.NoError(t, err, "a taken name is skipped, never replaced")
	require.Equal(t, "catalog-20261020T070807Z-2.db", name)
	data, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "keep me", string(data))

	// Backups within the same second (two jobs back to back) each get a name.
	other := t.TempDir()
	first, err := database.BackupTo(ctx, other)
	require.NoError(t, err)
	second, err := database.BackupTo(ctx, other)
	require.NoError(t, err)
	require.Equal(t, "catalog-20261020T070807Z.db", first)
	require.Equal(t, "catalog-20261020T070807Z-2.db", second)
}
