package bootstrap_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestNewAdminServiceCanEditTheCatalog(t *testing.T) {
	svc, closeFn, err := bootstrap.NewAdminService(writeCatalog(t), filepath.Join(t.TempDir(), "missing.db"))
	require.NoError(t, err)
	defer closeFn()

	require.True(t, svc.Editable(), "the YAML store supports locked updates")
}

func TestNewAdminEnablesProfileReviewWithoutCredentialsOrDatabase(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")
	svc, closeFn, err := bootstrap.NewAdminService(writeCatalog(t), filepath.Join(t.TempDir(), "none.db"))
	require.NoError(t, err)
	defer closeFn()
	require.True(t, svc.Resolvable())

	wb, err := svc.ResolveWorkbench(context.Background(), "wu-tang-clan", "Wu-Tang Clan")
	require.NoError(t, err)
	require.False(t, wb.SearchAvailable, "no credentials, no online search")
	require.NotEmpty(t, wb.Notices)
	for _, n := range wb.Notices {
		require.NotContains(t, n.Message, "SPOTIFY_CLIENT")
	}
}

func TestNewAdminWithCredentialsKeepsThemOutOfTheWorkbench(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "client-id-secret-value")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "client-secret-value")
	svc, closeFn, err := bootstrap.NewAdminService(writeCatalog(t), filepath.Join(t.TempDir(), "none.db"))
	require.NoError(t, err)
	defer closeFn()

	// An unsafe link is rejected locally, so this test never touches the network.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	wb, err := svc.ResolveWorkbench(ctx, "wu-tang-clan", "https://evil.example/artist/7CrVv4zLga71gdlMOMfjXL")
	require.NoError(t, err)
	require.True(t, wb.SearchAvailable)
	require.Empty(t, wb.Candidates)
	dump := fmt.Sprintf("%+v", wb)
	require.NotContains(t, dump, "client-secret-value")
	require.NotContains(t, dump, "client-id-secret-value")
}
