package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMigrateVerifyAndSnapshot(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")

	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order)
VALUES ('wu-tang-clan', 'Wu-Tang Clan', '34EP7KEpOjXcM2TCat1ISk', 'core', 1, 1);
INSERT INTO artist_aliases(artist_slug, alias, position)
VALUES ('wu-tang-clan', 'Wu Tang Clan', 1);`)
	require.NoError(t, err)

	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	report, err := database.Verify(ctx, migrations)
	require.NoError(t, err)
	require.Equal(t, 1, report.Migrations)
	require.Contains(t, report.Checks, "integrity_check")
	require.Contains(t, report.Checks, "foreign_key_check")

	snapshot, err := database.Snapshot(ctx)
	require.NoError(t, err)
	snapshotText := string(snapshot)
	require.Contains(t, snapshotText, `DELETE FROM "configured_artists";`)
	require.Contains(t, snapshotText, `'wu-tang-clan'`)
	require.Contains(t, snapshotText, `'Wu Tang Clan'`)
}

func TestSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql")

	database := openMigratedDatabase(t, ctx, dbPath)
	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, category, enabled, editorial_order)
VALUES ('gravediggaz', 'Gravediggaz', 'affiliate_group', 0, 2);`)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	require.NoError(t, database.Close())

	rebuiltPath := filepath.Join(dir, "rebuilt.db")
	rebuilt := openMigratedDatabase(t, ctx, rebuiltPath)
	defer func() { require.NoError(t, rebuilt.Close()) }()

	require.NoError(t, sqliteadapter.RestoreSnapshot(ctx, rebuilt.DB(), snapshotPath))

	var name string
	err = rebuilt.DB().QueryRowContext(ctx, `SELECT name FROM configured_artists WHERE slug = 'gravediggaz'`).Scan(&name)
	require.NoError(t, err)
	require.Equal(t, "Gravediggaz", name)
}

func TestVerifyFailsWhenMigrationMissing(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")

	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	_, err := database.DB().ExecContext(ctx, `DELETE FROM schema_migrations`)
	require.NoError(t, err)

	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	_, err = database.Verify(ctx, migrations)
	require.ErrorContains(t, err, "is not applied")
}

func TestWriteSnapshotDoesNotRewriteUnchangedFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql")

	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	first, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	second, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.True(t, strings.HasPrefix(string(second), "-- Spot Wu Family catalog snapshot"))
}

func openMigratedDatabase(t *testing.T, ctx context.Context, path string) *sqliteadapter.Database {
	t.Helper()

	database, err := sqliteadapter.Open(path)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(ctx))

	return database
}
