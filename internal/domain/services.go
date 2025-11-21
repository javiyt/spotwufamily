package domain

import "fmt"

// SearchService describes a service able to search artists.
type SearchService interface {
	GetArtists([]string) []Artist
}

// SearchArtists implements SearchService using an ArtistRepository.
type SearchArtists struct {
	r ArtistRepository
}

// NewSearchArtists constructs a SearchArtists service with the given repository.
func NewSearchArtists(r ArtistRepository) *SearchArtists {
	return &SearchArtists{r: r}
}

// GetArtists searches for artists by names in the provided list.
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
