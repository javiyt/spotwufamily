package artists

import (
	"context"
	"errors"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type profileTrackSource interface {
	SpotifyProfileSource
	GetAlbumTracks(context.Context, string) ([]catalog.Track, error)
}

// CacheFirstProfileSource answers album and track reads from the SQLite cache
// and only asks the remote source on a miss. Without a remote it is
// cache-only, which is what offline audits use. Profile metadata is not
// cached, so GetArtist always goes remote.
type CacheFirstProfileSource struct {
	cache  SpotifyAlbumCache
	remote SpotifyProfileSource
}

func NewCacheFirstProfileSource(cache SpotifyAlbumCache, remote SpotifyProfileSource) CacheFirstProfileSource {
	return CacheFirstProfileSource{cache: cache, remote: remote}
}

func (s CacheFirstProfileSource) GetArtist(ctx context.Context, id string) (catalog.ArtistCandidate, error) {
	if s.remote == nil {
		return catalog.ArtistCandidate{}, ErrCacheMiss
	}
	return s.remote.GetArtist(ctx, id)
}

func (s CacheFirstProfileSource) GetArtistAlbums(ctx context.Context, id string, groups []string) ([]catalog.Release, error) {
	if s.cache != nil {
		releases, err := s.cache.GetCachedArtistAlbums(ctx, id, groups)
		if err == nil {
			return releases, nil
		}
		if !errors.Is(err, ErrCacheMiss) {
			return nil, err
		}
	}
	if s.remote == nil {
		return nil, ErrCacheMiss
	}
	return s.remote.GetArtistAlbums(ctx, id, groups)
}

func (s CacheFirstProfileSource) GetAlbumTracks(ctx context.Context, id string) ([]catalog.Track, error) {
	if s.cache != nil {
		tracks, err := s.cache.GetCachedAlbumTracks(ctx, id)
		if err == nil {
			return tracks, nil
		}
		if !errors.Is(err, ErrCacheMiss) {
			return nil, err
		}
	}
	remote, ok := s.remote.(profileTrackSource)
	if !ok {
		return nil, ErrCacheMiss
	}
	return remote.GetAlbumTracks(ctx, id)
}

// RetryAfterOf extracts the wait a rate-limited source asked for, when its
// error carries one.
func RetryAfterOf(err error) (time.Duration, bool) {
	var withWait interface{ RetryAfter() time.Duration }
	if errors.As(err, &withWait) {
		if wait := withWait.RetryAfter(); wait > 0 {
			return wait, true
		}
	}
	return 0, false
}
