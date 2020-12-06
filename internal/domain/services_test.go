package domain_test

import (
	"errors"
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestSearchArtists_GetArtists(t *testing.T) {
	r := new(domain.MockArtistRepository)

	s := domain.NewSearchArtists(r)

	t.Run("it fails when couldn't find any artist", func(t *testing.T) {
		r.On("SearchArtist", "errored").
			Once().
			Return(nil, errors.New("API testing error"))

		artists, err := s.GetArtists([]string{"errored"})

		require.EqualError(t, err, "error API testing error searching for artist errored")
		require.Nil(t, artists)
		r.AssertExpectations(t)
	})

	t.Run("it should return all artists found", func(t *testing.T) {
		r.On("SearchArtist", "one").
			Once().
			Return([]domain.Artist{
				domain.NewArtist("8xGQDuKe5y", "one", "https://image.cdn/4856820220"),
				domain.NewArtist("exmyl4Qaip", "one & third", ""),
			}, nil)
		r.On("SearchArtist", "two").
			Once().
			Return([]domain.Artist{domain.NewArtist("c4iGhUEijM", "second", "https://image.cdn/8249544919")}, nil)

		artists, err := s.GetArtists([]string{"one", "two"})

		require.NoError(t, err)
		require.Len(t, artists, 3)

		r.AssertExpectations(t)
	})
}
