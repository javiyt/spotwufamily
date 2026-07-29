package artists_test

import (
	"context"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type fixedDiscoverySource struct {
	items []artists.DiscoveredArtist
}

func (s fixedDiscoverySource) DiscoverWuFamilyArtists(context.Context) ([]artists.DiscoveredArtist, error) {
	return s.items, nil
}

func TestDiscoverWuFamilyReportsNewAndExistingArtists(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", Category: catalog.CategoryCore, Roles: []catalog.Category{catalog.CategoryCore}, Aliases: []string{"Wu Tang Clan"}, EditorialOrder: 1},
			{Slug: "killarmy", Name: "Killarmy", Category: catalog.CategoryAffiliateGroup, Roles: []catalog.Category{catalog.CategoryAffiliateGroup}, Aliases: []string{}, EditorialOrder: 2},
		},
	}}
	source := fixedDiscoverySource{items: []artists.DiscoveredArtist{
		{Name: "Killarmy", Category: catalog.CategoryAffiliateGroup, Source: "Wikipedia", SourceURL: "https://example/killarmy", SourceNote: "Groups"},
		{Name: "Tekitha", Category: catalog.CategoryAffiliateArtist, Source: "Wikipedia", SourceURL: "https://example/tekitha", SourceNote: "Singers"},
	}}

	report, err := artists.NewDiscoverWuFamily(store, source).Run(context.Background(), artists.DiscoverWuFamilyOptions{CatalogPath: "ignored"})

	require.NoError(t, err)
	require.Equal(t, 2, report.Found)
	require.Len(t, report.Existing, 1)
	require.Equal(t, "killarmy", report.Existing[0].Slug)
	require.Len(t, report.New, 1)
	require.Equal(t, "tekitha", report.New[0].Slug)
	require.Empty(t, report.Added)
	require.Len(t, store.catalog.Artists, 2)
}

func TestDiscoverWuFamilyApplyAddsNewArtistsDisabled(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", Category: catalog.CategoryCore, Roles: []catalog.Category{catalog.CategoryCore}, Aliases: []string{}, EditorialOrder: 1},
		},
	}}
	source := fixedDiscoverySource{items: []artists.DiscoveredArtist{
		{Name: "Tekitha", Category: catalog.CategoryAffiliateArtist, Source: "Wikipedia", SourceURL: "https://example/tekitha", SourceNote: "Singers"},
	}}

	report, err := artists.NewDiscoverWuFamily(store, source).Run(context.Background(), artists.DiscoverWuFamilyOptions{CatalogPath: "ignored", Apply: true})

	require.NoError(t, err)
	require.Len(t, report.Added, 1)
	require.Len(t, store.catalog.Artists, 2)
	added := store.catalog.Artists[1]
	require.Equal(t, "tekitha", added.Slug)
	require.Equal(t, catalog.CategoryAffiliateArtist, added.Category)
	require.False(t, added.Enabled)
	require.Empty(t, added.SpotifyID)
	require.Equal(t, "https://example/tekitha", added.ExternalURL)
	require.Contains(t, added.Notes, "review before enabling")
}

func TestDiscoverWuFamilyMatchesSlashSeparatedAliases(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "tha-beggas", Name: "Tha Beggas", Category: catalog.CategoryAffiliateGroup, Roles: []catalog.Category{catalog.CategoryAffiliateGroup}, Aliases: []string{}, EditorialOrder: 1},
		},
	}}
	source := fixedDiscoverySource{items: []artists.DiscoveredArtist{
		{Name: "Tha Beggas/ The Beggaz Clan", Category: catalog.CategoryAffiliateGroup, Source: "Wikipedia", SourceURL: "https://example/beggas", SourceNote: "Groups"},
		{Name: "Warcloud / The Holocaust", Category: catalog.CategoryAffiliateArtist, Source: "Wikipedia", SourceURL: "https://example/warcloud", SourceNote: "Rappers"},
	}}

	report, err := artists.NewDiscoverWuFamily(store, source).Run(context.Background(), artists.DiscoverWuFamilyOptions{CatalogPath: "ignored"})

	require.NoError(t, err)
	require.Len(t, report.Existing, 1)
	require.Equal(t, "tha-beggas", report.Existing[0].Slug)
	require.Len(t, report.New, 1)
	require.Equal(t, "Warcloud", report.New[0].Name)
	require.Equal(t, []string{"The Holocaust"}, report.New[0].Aliases)
}
