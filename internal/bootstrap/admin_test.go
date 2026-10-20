package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/bootstrap"
)

const catalogYAML = `
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    category: core
    roles: [core]
    enabled: false
    editorial_order: 1
`

func writeCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artists.yaml")
	require.NoError(t, os.WriteFile(path, []byte(catalogYAML), 0o644))
	return path
}

func TestNewAdminOfflineDoesNotCreateDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing", "catalog.db")
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")

	svc, closeFn, err := bootstrap.NewAdminService(writeCatalog(t), dbPath)
	require.NoError(t, err)
	defer closeFn()

	d, err := svc.Dashboard(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, d.Artists)
	require.False(t, d.DatabaseAvailable)
	require.False(t, d.SpotifyCredentialsPresent)
	require.NoFileExists(t, dbPath)
	require.NoDirExists(t, filepath.Dir(dbPath))
}

func TestNewAdminUsesExistingDatabaseAndReportsCredentialsPresence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	require.NoError(t, bootstrap.MigrateDatabase(ctx, dbPath))
	t.Setenv("SPOTIFY_CLIENT_ID", "id")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "secret")

	svc, closeFn, err := bootstrap.NewAdminService(writeCatalog(t), dbPath)
	require.NoError(t, err)
	defer closeFn()

	d, err := svc.Dashboard(ctx)
	require.NoError(t, err)
	require.True(t, d.DatabaseAvailable)
	require.True(t, d.SpotifyCredentialsPresent)
	require.Nil(t, d.LastSync)
}
