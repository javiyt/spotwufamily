package domain

import (
	"fmt"

	"github.com/zmb3/spotify"
)

type SearchService interface {
	GetArtists([]string) []Artist
}

type SearchArtists struct {
	c spotify.Client
}

func NewSearchArtists(c spotify.Client) *SearchArtists {
	return &SearchArtists{c: c}
}

func (s SearchArtists) GetArtists(list []string) ([]Artist, error) {
	artists := make([]Artist, 0)

	for i := range list {
		search, err := s.c.Search(list[i], spotify.SearchTypeArtist)
		if err != nil {
			return nil, fmt.Errorf("error %w searching for artist %s", err, list[i])
		}

		for j := range search.Artists.Artists {
			image := ""
			if len(search.Artists.Artists[j].Images) > 0 {
				image = search.Artists.Artists[j].Images[0].URL
			}

			artists = append(artists, NewArtist(search.Artists.Artists[j].ID.String(), search.Artists.Artists[j].Name, image))
		}
	}

	return artists, nil
}
