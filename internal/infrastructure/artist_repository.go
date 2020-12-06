package infrastructure

import (
	"fmt"

	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/zmb3/spotify"
)

type ArtistProxyRepository struct {
	rH ArtistHTTPRepository
}

func NewArtistProxyRepository(rH ArtistHTTPRepository) *ArtistProxyRepository {
	return &ArtistProxyRepository{rH: rH}
}

func (a *ArtistProxyRepository) SearchArtist(name string) ([]domain.Artist, error) {
	return a.rH.SearchArtist(name)
}

type ArtistHTTPRepository struct {
	c spotify.Client
}

func NewArtistHTTPRepository(c spotify.Client) *ArtistHTTPRepository {
	return &ArtistHTTPRepository{c: c}
}

func (a *ArtistHTTPRepository) SearchArtist(name string) ([]domain.Artist, error) {
	artists := make([]domain.Artist, 0)

	search, err := a.c.Search(name, spotify.SearchTypeArtist)
	if err != nil {
		return nil, fmt.Errorf("error %w searching for artist %s", err, name)
	}

	for j := range search.Artists.Artists {
		image := ""
		if len(search.Artists.Artists[j].Images) > 0 {
			image = search.Artists.Artists[j].Images[0].URL
		}

		artists = append(
			artists,
			domain.NewArtist(
				search.Artists.Artists[j].ID.String(),
				search.Artists.Artists[j].Name,
				image,
			),
		)
	}

	return artists, nil
}
