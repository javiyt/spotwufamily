package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

// These tests pin the CLI's observable behaviour for sync and refresh-genres
// so the wiring can move into bootstrap without changing output, exit codes or
// side effects.

const characterisationCatalog = `
version: 1
profile_sync_policy: allow_unreviewed
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
`

func writeCharacterisationCatalog(t *testing.T) (dir, catalogPath string) {
	t.Helper()
	dir = t.TempDir()
	catalogPath = filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(characterisationCatalog), 0o644))
	return dir, catalogPath
}

func TestSyncDryRunOutputIsUnchanged(t *testing.T) {
	_, catalogPath := writeCharacterisationCatalog(t)
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"sync", "--dry-run", "--catalog", catalogPath}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Equal(t, "dry-run planned artists: 1\nprocessed: 0 failed: 0 skipped: 0\nalbums: 0 tracks: 0 artist_albums: 0 artist_tracks: 0\n", stdout.String())
}

func TestSyncWithoutCredentialsMigratesDatabaseThenFails(t *testing.T) {
	dir, catalogPath := writeCharacterisationCatalog(t)
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql.gz")
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"sync", "--catalog", catalogPath, "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(t, "sync: SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required\n", stderr.String())
	require.FileExists(t, dbPath, "the database is opened and migrated before credentials are checked")
	require.NoFileExists(t, snapshotPath)
}

func TestRefreshGenresWithoutCredentialsFailsBeforeOpeningDatabase(t *testing.T) {
	dir, catalogPath := writeCharacterisationCatalog(t)
	dbPath := filepath.Join(dir, "catalog.db")
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "refresh-genres", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(t, "artists refresh-genres: SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required\n", stderr.String())
	require.NoFileExists(t, dbPath, "credentials are checked before the database is opened")
}
