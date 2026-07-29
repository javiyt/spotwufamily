package artists_test

import (
	"context"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type albumAuditSpotifyFetcher struct {
	albumsByArtist map[string][]catalog.Release
	requestedIDs   []string
}

func (f *albumAuditSpotifyFetcher) GetArtistAlbums(_ context.Context, spotifyID string, groups []string) ([]catalog.Release, error) {
	f.requestedIDs = append(f.requestedIDs, spotifyID)
	return f.albumsByArtist[spotifyID], nil
}

type albumAuditMusicBrainzSearcher struct {
	releaseGroups []artists.MusicBrainzReleaseGroup
}

func (s albumAuditMusicBrainzSearcher) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return s.releaseGroups, nil
}

func TestAuditAlbumsComparesSpotifyAlbumsWithMusicBrainzReleaseGroups(t *testing.T) {
	store := &memoryStore{
		catalog: catalog.EditorialCatalog{
			Version: 1,
			Artists: []catalog.Artist{
				{
					Slug:           "wu-tang-clan",
					Name:           "Wu-Tang Clan",
					SpotifyID:      "34EP7KEpOjXcM2TCat1ISk",
					SpotifyIDs:     []string{"0H8YCcvC3MPLKnbDRasGiG"},
					Category:       catalog.CategoryCore,
					Roles:          []catalog.Category{catalog.CategoryCore},
					EditorialOrder: 1,
				},
			},
		},
	}
	spotify := &albumAuditSpotifyFetcher{albumsByArtist: map[string][]catalog.Release{
		"34EP7KEpOjXcM2TCat1ISk": {
			{SpotifyID: "album-1", Name: "Enter the Wu-Tang (36 Chambers) [Expanded Edition]", ReleaseDate: "2007-01-01", URL: "https://spotify/album-1"},
			{SpotifyID: "album-extra", Name: "Wu-Tang Clan Greatest Hits", ReleaseDate: "2004", URL: "https://spotify/extra"},
		},
		"0H8YCcvC3MPLKnbDRasGiG": {
			{SpotifyID: "album-2", Name: "Wu-Tang Forever", ReleaseDate: "1997-06-03", URL: "https://spotify/album-2"},
		},
	}}
	musicBrainz := albumAuditMusicBrainzSearcher{releaseGroups: []artists.MusicBrainzReleaseGroup{
		{ID: "mb-1", Title: "Enter the Wu-Tang (36 Chambers)", FirstReleaseDate: "1993-11-09", URL: "https://musicbrainz/mb-1"},
		{ID: "mb-2", Title: "Wu-Tang Forever", FirstReleaseDate: "1997-06-03", URL: "https://musicbrainz/mb-2"},
		{ID: "mb-3", Title: "The W", FirstReleaseDate: "2000-11-21", URL: "https://musicbrainz/mb-3"},
	}}

	report, err := artists.NewAuditAlbums(store, spotify, musicBrainz).Run(context.Background(), artists.AuditAlbumsOptions{
		CatalogPath: "artists.yaml",
		ArtistSlug:  "wu-tang-clan",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"34EP7KEpOjXcM2TCat1ISk", "0H8YCcvC3MPLKnbDRasGiG"}, spotify.requestedIDs)
	require.Len(t, report.Artists, 1)
	require.Len(t, report.Artists[0].Matched, 2)
	require.Equal(t, "The W", report.Artists[0].MissingSpotify[0].Title)
	require.Equal(t, "Wu-Tang Clan Greatest Hits", report.Artists[0].SuspiciousSpotify[0].Title)

	markdown := string(artists.FormatAuditAlbumsMarkdown(report))
	require.Contains(t, markdown, "missing_from_spotify=1")
	require.Contains(t, markdown, "suspicious_spotify=1")
	require.Contains(t, markdown, "https://open.spotify.com/artist/34EP7KEpOjXcM2TCat1ISk")
}
