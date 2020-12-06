package domain

import "fmt"

type SearchService interface {
	GetArtists([]string) []Artist
}

type SearchArtists struct {
	r ArtistRepository
}

func NewSearchArtists(r ArtistRepository) *SearchArtists {
	return &SearchArtists{r: r}
}

func (s SearchArtists) GetArtists(list []string) ([]Artist, error) {
	artists := make([]Artist, 0)

	for i := range list {
		artist, err := s.r.SearchArtist(list[i])
		if err != nil {
			return nil, fmt.Errorf("error %w searching for artist %s", err, list[i])
		}

		artists = append(artists, artist...)
	}

	return artists, nil
}
