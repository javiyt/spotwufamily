package spotify

import (
	"context"
	"errors"
	"fmt"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// ProfileSource adapts Client to the profile audit port, translating transport
// errors into the application's source errors.
type ProfileSource struct {
	client *Client
}

func NewProfileSource(client *Client) ProfileSource {
	return ProfileSource{client: client}
}

func (s ProfileSource) GetArtist(ctx context.Context, id string) (catalog.ArtistCandidate, error) {
	artist, err := s.client.GetArtist(ctx, id)
	return artist, mapSourceError(err)
}

func (s ProfileSource) GetArtistAlbums(ctx context.Context, id string, groups []string) ([]catalog.Release, error) {
	albums, err := s.client.GetArtistAlbums(ctx, id, groups)
	return albums, mapSourceError(err)
}

func (s ProfileSource) GetAlbumTracks(ctx context.Context, id string) ([]catalog.Track, error) {
	tracks, err := s.client.GetAlbumTracks(ctx, id)
	return tracks, mapSourceError(err)
}

func mapSourceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%w: %w", artists.ErrSourceNotFound, err)
	case errors.Is(err, ErrRateLimited):
		return fmt.Errorf("%w: %w", artists.ErrSourceRateLimited, err)
	default:
		return fmt.Errorf("%w: %w", artists.ErrSourceUnavailable, err)
	}
}
