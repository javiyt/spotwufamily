package domain

import (
	"fmt"
	"log"

	"github.com/zmb3/spotify"
)

type SearchArtistError struct {
	artist string
}

func NewSearchArtistError(artist string) SearchArtistError {
	return SearchArtistError{artist: artist}
}

func (s SearchArtistError) Error() string {
	return fmt.Sprintf("Error searching for artist %s", s.artist)
}

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
	artist := make([]Artist, 0, len(list))
	for i := range list {
		search, err := s.c.Search("method man", spotify.SearchTypeArtist)
		if err != nil {

		}

	}
}
