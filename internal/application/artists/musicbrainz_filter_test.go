package artists_test

import (
	"context"
	"errors"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type fixedCandidateSearcher struct {
	candidates []catalog.ArtistCandidate
}

func (s fixedCandidateSearcher) SearchArtistCandidates(context.Context, catalog.Artist) ([]catalog.ArtistCandidate, error) {
	return s.candidates, nil
}

type failingMusicBrainzSearcher struct{}

func (failingMusicBrainzSearcher) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return nil, errors.New("musicbrainz timeout")
}

type failingAlbumFetcher struct{}

func (failingAlbumFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return nil, errors.New("spotify albums timeout")
}

type trackCreditSpotifyFetcher struct {
	albumsByArtist  map[string][]catalog.Release
	tracksByAlbum   map[string][]catalog.Track
	requestedIDs    []string
	requestedAlbums []string
}

func (f *trackCreditSpotifyFetcher) GetArtistAlbums(_ context.Context, spotifyID string, groups []string) ([]catalog.Release, error) {
	f.requestedIDs = append(f.requestedIDs, spotifyID)
	return f.albumsByArtist[spotifyID], nil
}

func (f *trackCreditSpotifyFetcher) GetAlbumTracks(_ context.Context, spotifyID string) ([]catalog.Track, error) {
	f.requestedAlbums = append(f.requestedAlbums, spotifyID)
	return f.tracksByAlbum[spotifyID], nil
}

func TestAlbumEvidenceCandidateSearcherDiscardsCandidatesWithoutMusicBrainzAlbumMatches(t *testing.T) {
	spotify := &albumAuditSpotifyFetcher{albumsByArtist: map[string][]catalog.Release{
		"good-artist": {
			{SpotifyID: "album-1", Name: "Enter the Wu-Tang (36 Chambers)", ReleaseDate: "1993-11-09"},
		},
		"noise-artist": {
			{SpotifyID: "album-2", Name: "Unrelated Album", ReleaseDate: "2020-01-01"},
		},
	}}
	musicBrainz := albumAuditMusicBrainzSearcher{releaseGroups: []artists.MusicBrainzReleaseGroup{
		{ID: "mb-1", Title: "Enter the Wu-Tang (36 Chambers)", FirstReleaseDate: "1993-11-09"},
	}}
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{candidates: []catalog.ArtistCandidate{
			{Name: "Wu-Tang Clan", SpotifyID: "good-artist"},
			{Name: "Wu-Tang Clan", SpotifyID: "noise-artist"},
		}},
		spotify,
		musicBrainz,
	)

	candidates, err := searcher.SearchArtistCandidates(context.Background(), catalog.Artist{Name: "Wu-Tang Clan"})

	require.NoError(t, err)
	require.Equal(t, []catalog.ArtistCandidate{{Name: "Wu-Tang Clan", SpotifyID: "good-artist"}}, candidates)
	require.Equal(t, []string{"good-artist", "noise-artist"}, spotify.requestedIDs)
}

func TestAlbumEvidenceCandidateSearcherAddsTrackCreditEvidenceFromConfiguredGroups(t *testing.T) {
	spotify := &trackCreditSpotifyFetcher{
		albumsByArtist: map[string][]catalog.Release{
			"group-artist": {
				{SpotifyID: "group-album", Name: "Group Album"},
			},
			"credited-artist": {
				{SpotifyID: "credited-album", Name: "Unrelated Solo Album", ReleaseDate: "2020-01-01"},
			},
			"noise-artist": {
				{SpotifyID: "noise-album", Name: "Noise Album", ReleaseDate: "2020-01-01"},
			},
		},
		tracksByAlbum: map[string][]catalog.Track{
			"group-album": {
				{
					Name: "Feature Track",
					Artists: []catalog.ArtistCandidate{
						{SpotifyID: "group-artist", Name: "Configured Group"},
						{SpotifyID: "credited-artist", Name: "Solomon Childs"},
					},
				},
			},
		},
	}
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{candidates: []catalog.ArtistCandidate{
			{Name: "Solomon Childs", SpotifyID: "credited-artist"},
			{Name: "Solomon Childs", SpotifyID: "noise-artist"},
		}},
		spotify,
		albumAuditMusicBrainzSearcher{releaseGroups: []artists.MusicBrainzReleaseGroup{
			{ID: "mb-1", Title: "Expected MusicBrainz Album", FirstReleaseDate: "1998"},
		}},
	)

	candidates, err := searcher.SearchArtistCandidatesWithCatalog(context.Background(), catalog.Artist{
		Slug: "solomon-childs",
		Name: "Solomon Childs",
	}, catalog.EditorialCatalog{Artists: []catalog.Artist{{
		Slug:      "configured-group",
		Name:      "Configured Group",
		SpotifyID: "group-artist",
		Category:  catalog.CategoryAffiliateGroup,
	}}})

	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "credited-artist", candidates[0].SpotifyID)
	require.Contains(t, candidates[0].RelatedArtistEvidence[0], "credited on Configured Group track")
	require.Contains(t, spotify.requestedIDs, "group-artist")
	require.Contains(t, spotify.requestedAlbums, "group-album")
}

func TestAlbumEvidenceCandidateSearcherDropsWeakCandidatesBeforeAlbumFetch(t *testing.T) {
	spotify := &albumAuditSpotifyFetcher{albumsByArtist: map[string][]catalog.Release{
		"noise-artist": {
			{SpotifyID: "album-1", Name: "Noise Album", ReleaseDate: "2020-01-01"},
		},
	}}
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{candidates: []catalog.ArtistCandidate{
			{Name: "Completely Unrelated Orchestra", SpotifyID: "noise-artist", Genres: []string{"classical"}},
		}},
		spotify,
		albumAuditMusicBrainzSearcher{releaseGroups: []artists.MusicBrainzReleaseGroup{
			{ID: "mb-1", Title: "Expected Album", FirstReleaseDate: "1998"},
		}},
	)

	candidates, err := searcher.SearchArtistCandidates(context.Background(), catalog.Artist{Name: "Killarmy"})

	require.NoError(t, err)
	require.Empty(t, candidates)
	require.Empty(t, spotify.requestedIDs)
}

func TestAlbumEvidenceCandidateSearcherKeepsCandidatesWhenMusicBrainzFails(t *testing.T) {
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{candidates: []catalog.ArtistCandidate{{Name: "A.I.G.", SpotifyID: "artist-1"}}},
		failingAlbumFetcher{},
		failingMusicBrainzSearcher{},
	)

	candidates, err := searcher.SearchArtistCandidates(context.Background(), catalog.Artist{Name: "A.I.G."})

	require.NoError(t, err)
	require.Equal(t, []catalog.ArtistCandidate{{Name: "A.I.G.", SpotifyID: "artist-1"}}, candidates)
}

func TestAlbumEvidenceCandidateSearcherSkipsCandidatesWhenSpotifyAlbumFetchFails(t *testing.T) {
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{candidates: []catalog.ArtistCandidate{{Name: "A.I.G.", SpotifyID: "artist-1"}}},
		failingAlbumFetcher{},
		albumAuditMusicBrainzSearcher{releaseGroups: []artists.MusicBrainzReleaseGroup{{ID: "mb-1", Title: "Album One", FirstReleaseDate: "2000"}}},
	)

	candidates, err := searcher.SearchArtistCandidates(context.Background(), catalog.Artist{Name: "A.I.G."})

	require.NoError(t, err)
	require.Empty(t, candidates)
}

func TestAlbumEvidenceCandidateSearcherKeepsCandidatesWhenMusicBrainzHasNoAlbums(t *testing.T) {
	spotify := &albumAuditSpotifyFetcher{albumsByArtist: map[string][]catalog.Release{}}
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{candidates: []catalog.ArtistCandidate{{Name: "Unknown", SpotifyID: "artist-1"}}},
		spotify,
		albumAuditMusicBrainzSearcher{},
	)

	candidates, err := searcher.SearchArtistCandidates(context.Background(), catalog.Artist{Name: "Unknown"})

	require.NoError(t, err)
	require.Equal(t, []catalog.ArtistCandidate{{Name: "Unknown", SpotifyID: "artist-1"}}, candidates)
	require.Empty(t, spotify.requestedIDs)
}

func TestAlbumEvidenceCandidateSearcherWarnsWhenConfiguredSpotifyIDHasNoMusicBrainzAlbumMatches(t *testing.T) {
	spotify := &albumAuditSpotifyFetcher{albumsByArtist: map[string][]catalog.Release{
		"good-artist": {
			{SpotifyID: "album-1", Name: "Enter the Wu-Tang (36 Chambers)", ReleaseDate: "1993-11-09"},
		},
		"bad-artist": {
			{SpotifyID: "album-2", Name: "Unrelated Album", ReleaseDate: "2020-01-01"},
		},
	}}
	searcher := artists.NewAlbumEvidenceCandidateSearcher(
		fixedCandidateSearcher{},
		spotify,
		albumAuditMusicBrainzSearcher{releaseGroups: []artists.MusicBrainzReleaseGroup{
			{ID: "mb-1", Title: "Enter the Wu-Tang (36 Chambers)", FirstReleaseDate: "1993-11-09"},
		}},
	)

	warnings, err := searcher.ReviewConfiguredSpotifyIDs(context.Background(), catalog.Artist{
		Name:       "Wu-Tang Clan",
		SpotifyID:  "good-artist",
		SpotifyIDs: []string{"bad-artist"},
	})

	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Equal(t, "bad-artist", warnings[0].SpotifyID)
	require.Contains(t, warnings[0].Reason, "no Spotify albums matched")
	require.Equal(t, 1, warnings[0].SpotifyAlbumCount)
	require.Equal(t, 1, warnings[0].MusicBrainzAlbumCount)
	require.Equal(t, "Unrelated Album", warnings[0].SpotifyAlbums[0].Title)
	require.Equal(t, []string{"good-artist", "bad-artist"}, spotify.requestedIDs)
}
