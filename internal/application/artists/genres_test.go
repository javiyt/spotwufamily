package artists_test

import (
	"context"
	"errors"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type artistFetcher struct {
	artists map[string]catalog.ArtistCandidate
	errs    map[string]error
}

func (f artistFetcher) GetArtist(_ context.Context, spotifyID string) (catalog.ArtistCandidate, error) {
	if err, ok := f.errs[spotifyID]; ok {
		return catalog.ArtistCandidate{}, err
	}
	return f.artists[spotifyID], nil
}

func TestRefreshGenresStoresMergedSpotifyGenres(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:           "wu-tang-clan",
				Name:           "Wu-Tang Clan",
				SpotifyID:      "34EP7KEpOjXcM2TCat1ISk",
				SpotifyIDs:     []string{"0H8YCcvC3MPLKnbDRasGiG"},
				Category:       catalog.CategoryCore,
				Roles:          []catalog.Category{catalog.CategoryCore},
				Aliases:        []string{},
				EditorialOrder: 1,
			},
		},
	}}
	fetcher := artistFetcher{artists: map[string]catalog.ArtistCandidate{
		"34EP7KEpOjXcM2TCat1ISk": {URL: "https://open.spotify.com/artist/34EP7KEpOjXcM2TCat1ISk", ImageURL: "https://i.scdn.co/image/artist-large", Genres: []string{"east coast hip hop", "hardcore hip hop"}},
		"0H8YCcvC3MPLKnbDRasGiG": {Genres: []string{"Hardcore Hip Hop", "rap"}},
	}}

	report, err := artists.NewRefreshGenres(store, fetcher).Run(context.Background(), artists.RefreshGenresOptions{CatalogPath: "ignored"})

	require.NoError(t, err)
	require.Equal(t, 1, report.ArtistsWithIDs)
	require.Equal(t, 1, report.Updated)
	require.Equal(t, []string{"east coast hip hop", "hardcore hip hop", "rap"}, store.catalog.Artists[0].Genres)
	require.Equal(t, "https://open.spotify.com/artist/34EP7KEpOjXcM2TCat1ISk", store.catalog.Artists[0].ExternalURL)
	require.Equal(t, "https://i.scdn.co/image/artist-large", store.catalog.Artists[0].ImageURL)
}

func TestRefreshGenresDoesNotSaveWhenSpotifyErrors(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:           "a-i-g",
				Name:           "A.I.G.",
				SpotifyID:      "4W91zWkx3syrUDACiK5Jxh",
				Category:       catalog.CategoryAffiliateGroup,
				Roles:          []catalog.Category{catalog.CategoryAffiliateGroup},
				Aliases:        []string{},
				EditorialOrder: 1,
			},
		},
	}}
	fetcher := artistFetcher{errs: map[string]error{"4W91zWkx3syrUDACiK5Jxh": errors.New("spotify timeout")}}

	report, err := artists.NewRefreshGenres(store, fetcher).Run(context.Background(), artists.RefreshGenresOptions{CatalogPath: "ignored"})

	require.Error(t, err)
	require.Len(t, report.Errors, 1)
	require.Empty(t, store.catalog.Artists[0].Genres)
}
