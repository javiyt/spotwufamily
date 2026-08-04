package artists_test

import (
	"context"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type fixedSpotifyAlbumCache struct {
	albums []catalog.Release
	tracks []catalog.Track
	err    error
}

func (c fixedSpotifyAlbumCache) GetCachedArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return c.albums, c.err
}

func (c fixedSpotifyAlbumCache) GetCachedAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return c.tracks, c.err
}

type countingRemoteSpotifyFetcher struct {
	albumCalls int
	trackCalls int
}

func (f *countingRemoteSpotifyFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	f.albumCalls++
	return []catalog.Release{{SpotifyID: "remote-album"}}, nil
}

func (f *countingRemoteSpotifyFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	f.trackCalls++
	return []catalog.Track{{SpotifyID: "remote-track"}}, nil
}

func TestCachedSpotifyAlbumFetcherUsesCacheBeforeRemote(t *testing.T) {
	remote := &countingRemoteSpotifyFetcher{}
	fetcher := artists.NewCachedSpotifyAlbumFetcher(
		fixedSpotifyAlbumCache{
			albums: []catalog.Release{{SpotifyID: "cached-album"}},
			tracks: []catalog.Track{{SpotifyID: "cached-track"}},
		},
		remote,
	)

	albums, err := fetcher.GetArtistAlbums(context.Background(), "artist-1", []string{"album"})
	require.NoError(t, err)
	require.Equal(t, "cached-album", albums[0].SpotifyID)
	tracks, err := fetcher.GetAlbumTracks(context.Background(), "album-1")
	require.NoError(t, err)
	require.Equal(t, "cached-track", tracks[0].SpotifyID)
	require.Zero(t, remote.albumCalls)
	require.Zero(t, remote.trackCalls)
}

func TestCachedSpotifyAlbumFetcherFallsBackOnCacheMiss(t *testing.T) {
	remote := &countingRemoteSpotifyFetcher{}
	fetcher := artists.NewCachedSpotifyAlbumFetcher(
		fixedSpotifyAlbumCache{err: artists.ErrCacheMiss},
		remote,
	)

	albums, err := fetcher.GetArtistAlbums(context.Background(), "artist-1", []string{"album"})
	require.NoError(t, err)
	require.Equal(t, "remote-album", albums[0].SpotifyID)
	tracks, err := fetcher.GetAlbumTracks(context.Background(), "album-1")
	require.NoError(t, err)
	require.Equal(t, "remote-track", tracks[0].SpotifyID)
	require.Equal(t, 1, remote.albumCalls)
	require.Equal(t, 1, remote.trackCalls)
}
