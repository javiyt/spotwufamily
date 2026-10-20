package artists_test

import (
	"context"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type fakeAlbumCache struct {
	albums map[string][]catalog.Release
	tracks map[string][]catalog.Track
}

func (f fakeAlbumCache) GetCachedArtistAlbums(_ context.Context, id string, _ []string) ([]catalog.Release, error) {
	if a, ok := f.albums[id]; ok {
		return a, nil
	}
	return nil, artists.ErrCacheMiss
}
func (f fakeAlbumCache) GetCachedAlbum(context.Context, string) (catalog.Release, error) {
	return catalog.Release{}, artists.ErrCacheMiss
}
func (f fakeAlbumCache) GetCachedAlbumTracks(_ context.Context, id string) ([]catalog.Track, error) {
	if tr, ok := f.tracks[id]; ok {
		return tr, nil
	}
	return nil, artists.ErrCacheMiss
}

func TestCacheFirstProfileSourceUsesCacheBeforeRemote(t *testing.T) {
	remote := auditSpotify()
	cache := fakeAlbumCache{albums: map[string][]catalog.Release{wuPrimary: {album("c1", "Cached", "2000-01-01")}}}
	source := artists.NewCacheFirstProfileSource(cache, remote)

	albums, err := source.GetArtistAlbums(context.Background(), wuPrimary, []string{"album"})
	require.NoError(t, err)
	require.Equal(t, "c1", albums[0].SpotifyID)
	require.Zero(t, remote.calls["albums:"+wuPrimary])

	albums, err = source.GetArtistAlbums(context.Background(), wuSecondary, []string{"album"})
	require.NoError(t, err)
	require.Equal(t, "a3", albums[0].SpotifyID)
	require.Equal(t, 1, remote.calls["albums:"+wuSecondary])
}

func TestCacheFirstProfileSourceWithoutRemoteIsOffline(t *testing.T) {
	cache := fakeAlbumCache{
		albums: map[string][]catalog.Release{wuPrimary: {album("c1", "Cached", "2000-01-01")}},
		tracks: map[string][]catalog.Track{"c1": {{SpotifyID: "t1", Name: "Track"}}},
	}
	source := artists.NewCacheFirstProfileSource(cache, nil)

	_, err := source.GetArtist(context.Background(), wuPrimary)
	require.ErrorIs(t, err, artists.ErrCacheMiss, "profile metadata is not cached")
	albums, err := source.GetArtistAlbums(context.Background(), wuPrimary, []string{"album"})
	require.NoError(t, err)
	require.Len(t, albums, 1)
	_, err = source.GetArtistAlbums(context.Background(), wuWrong, []string{"album"})
	require.ErrorIs(t, err, artists.ErrCacheMiss)
	tracks, err := source.GetAlbumTracks(context.Background(), "c1")
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	_, err = source.GetAlbumTracks(context.Background(), "zz")
	require.ErrorIs(t, err, artists.ErrCacheMiss)
}
