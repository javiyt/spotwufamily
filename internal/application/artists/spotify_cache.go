package artists

import (
	"context"
	"errors"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

var ErrCacheMiss = errors.New("artist Spotify cache miss")

type SpotifyAlbumCache interface {
	GetCachedArtistAlbums(context.Context, string, []string) ([]catalog.Release, error)
	GetCachedAlbum(context.Context, string) (catalog.Release, error)
	GetCachedAlbumTracks(context.Context, string) ([]catalog.Track, error)
}

type CachedSpotifyAlbumFetcher struct {
	cache  SpotifyAlbumCache
	remote spotifyAlbumTrackFetcher
}

func NewCachedSpotifyAlbumFetcher(cache SpotifyAlbumCache, remote spotifyAlbumTrackFetcher) CachedSpotifyAlbumFetcher {
	return CachedSpotifyAlbumFetcher{cache: cache, remote: remote}
}

func (f CachedSpotifyAlbumFetcher) GetArtistAlbums(ctx context.Context, spotifyID string, groups []string) ([]catalog.Release, error) {
	if f.cache != nil {
		releases, err := f.cache.GetCachedArtistAlbums(ctx, spotifyID, groups)
		if err == nil {
			return releases, nil
		}
		if !errors.Is(err, ErrCacheMiss) {
			return nil, err
		}
	}

	return f.remote.GetArtistAlbums(ctx, spotifyID, groups)
}

func (f CachedSpotifyAlbumFetcher) GetAlbum(ctx context.Context, spotifyID string) (catalog.Release, error) {
	if f.cache != nil {
		release, err := f.cache.GetCachedAlbum(ctx, spotifyID)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, ErrCacheMiss) {
			return catalog.Release{}, err
		}
	}

	remote, ok := f.remote.(interface {
		GetAlbum(context.Context, string) (catalog.Release, error)
	})
	if !ok {
		return catalog.Release{}, ErrCacheMiss
	}

	return remote.GetAlbum(ctx, spotifyID)
}

func (f CachedSpotifyAlbumFetcher) GetAlbumTracks(ctx context.Context, spotifyID string) ([]catalog.Track, error) {
	if f.cache != nil {
		tracks, err := f.cache.GetCachedAlbumTracks(ctx, spotifyID)
		if err == nil {
			return tracks, nil
		}
		if !errors.Is(err, ErrCacheMiss) {
			return nil, err
		}
	}

	return f.remote.GetAlbumTracks(ctx, spotifyID)
}
