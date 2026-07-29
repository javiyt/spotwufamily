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
