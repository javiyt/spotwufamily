package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func seedArtist(slug string, order int, aliases ...string) catalog.Artist {
	return catalog.Artist{Slug: slug, Name: slug, Category: catalog.CategoryCore, Enabled: true,
		EditorialOrder: order, Aliases: aliases}
}

func TestSeedCatalogMirrorsTheEditorialCatalog(t *testing.T) {
	ctx := context.Background()
	database := openMigratedDatabase(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	defer func() { require.NoError(t, database.Close()) }()

	require.NoError(t, database.SeedCatalog(ctx, []catalog.Artist{
		seedArtist("gza", 1, "Genius", "Justice"), seedArtist("rza", 2, "Bobby Digital"), seedArtist("old", 3, "Gone"),
	}))
	// gza loses an alias, old is removed from the catalog, new takes its order.
	require.NoError(t, database.SeedCatalog(ctx, []catalog.Artist{
		seedArtist("gza", 1, "Genius"), seedArtist("rza", 2, "Bobby Digital"), seedArtist("new", 3),
	}))

	exported, err := database.LoadExportCatalog(ctx)
	require.NoError(t, err)
	got := map[string][]string{}
	for _, a := range exported.Artists {
		got[a.Slug] = a.Aliases
	}
	require.NotContains(t, got, "old", "an artist removed from the catalog is not exported")
	require.Equal(t, []string{"Genius"}, got["gza"], "a removed alias is not exported")
	require.Equal(t, []string{"Bobby Digital"}, got["rza"], "unrelated artists keep their aliases")
	require.Contains(t, got, "new")

	var active int
	var order any
	require.NoError(t, database.DB().QueryRowContext(ctx,
		`SELECT active, editorial_order FROM configured_artists WHERE slug = 'old'`).Scan(&active, &order))
	require.Equal(t, 0, active, "the removed artist is deactivated, not deleted")
	require.Nil(t, order, "a deactivated artist frees its editorial order")

	// Coming back reactivates it.
	require.NoError(t, database.SeedCatalog(ctx, []catalog.Artist{
		seedArtist("gza", 1, "Genius"), seedArtist("rza", 2, "Bobby Digital"), seedArtist("new", 3), seedArtist("old", 4),
	}))
	require.NoError(t, database.DB().QueryRowContext(ctx,
		`SELECT active FROM configured_artists WHERE slug = 'old'`).Scan(&active))
	require.Equal(t, 1, active)
}

func TestSaveConfiguredArtistsReplacesTheListedArtistsAliases(t *testing.T) {
	ctx := context.Background()
	database := openMigratedDatabase(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	defer func() { require.NoError(t, database.Close()) }()
	require.NoError(t, database.SaveConfiguredArtists(ctx, []catalog.Artist{seedArtist("gza", 1, "Genius", "Justice"), seedArtist("rza", 2, "Bobby")}))
	// A partial save never deactivates the artists it does not list.
	require.NoError(t, database.SaveConfiguredArtists(ctx, []catalog.Artist{seedArtist("gza", 1, "Genius")}))
	exported, err := database.LoadExportCatalog(ctx)
	require.NoError(t, err)
	got := map[string][]string{}
	for _, a := range exported.Artists {
		got[a.Slug] = a.Aliases
	}
	require.Equal(t, []string{"Genius"}, got["gza"])
	require.Equal(t, []string{"Bobby"}, got["rza"])
}
