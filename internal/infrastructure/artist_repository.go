// Package infrastructure provides implementations of domain repositories.
package infrastructure

import (
	"fmt"

	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/zmb3/spotify"
)

// ArtistProxyRepository delegates artist lookups to an HTTP repository.
type ArtistProxyRepository struct {
	rH ArtistHTTPRepository
}

// NewArtistProxyRepository constructs a new ArtistProxyRepository.
func NewArtistProxyRepository(rH ArtistHTTPRepository) *ArtistProxyRepository {
	return &ArtistProxyRepository{rH: rH}
}

// SearchArtist looks up artists by name using the underlying HTTP repository.
func (a *ArtistProxyRepository) SearchArtist(name string) ([]domain.Artist, error) {
	return a.rH.SearchArtist(name)
}

// ArtistHTTPRepository performs lookups against the Spotify HTTP API.
type ArtistHTTPRepository struct {
	c spotify.Client
}

// NewArtistHTTPRepository constructs a new ArtistHTTPRepository.
func NewArtistHTTPRepository(c spotify.Client) *ArtistHTTPRepository {
	return &ArtistHTTPRepository{c: c}
}

// SearchArtist queries Spotify for artists by name and converts results to domain.Artist.
func (a *ArtistHTTPRepository) SearchArtist(name string) ([]domain.Artist, error) {
	artists := make([]domain.Artist, 0)

	search, err := a.c.Search(name, spotify.SearchTypeArtist)
	if err != nil {
		return nil, fmt.Errorf("error %w searching for artist %s", err, name)
	}

	for idx := range search.Artists.Artists {
		image := ""
		if len(search.Artists.Artists[idx].Images) > 0 {
			image = search.Artists.Artists[idx].Images[0].URL
		}

		artists = append(
			artists,
			domain.NewArtist(
				search.Artists.Artists[idx].ID.String(),
				search.Artists.Artists[idx].Name,
				image,
			),
		)
	}

	return artists, nil
}
