package domain_test

import (
	"errors"
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/stretchr/testify/require"
)

var errAPITesting = errors.New("API testing error")

func TestSearchArtists_GetArtists(t *testing.T) {
	repo := new(domain.MockArtistRepository)

	svc := domain.NewSearchArtists(repo)

	t.Run("it fails when couldn't find any artist", func(t *testing.T) {
		repo.On("SearchArtist", "errored").
			Once().
			Return(nil, errAPITesting)

		artists, err := svc.GetArtists([]string{"errored"})

		require.EqualError(t, err, "error API testing error searching for artist errored")
		require.Nil(t, artists)
		repo.AssertExpectations(t)
	})

	t.Run("it should return all artists found", func(t *testing.T) {
		repo.On("SearchArtist", "one").
			Once().
			Return([]domain.Artist{
				domain.NewArtist("8xGQDuKe5y", "one", "https://image.cdn/4856820220"),
				domain.NewArtist("exmyl4Qaip", "one & third", ""),
			}, nil)
		repo.On("SearchArtist", "two").
			Once().
			Return([]domain.Artist{domain.NewArtist("c4iGhUEijM", "second", "https://image.cdn/8249544919")}, nil)

		artists, err := svc.GetArtists([]string{"one", "two"})

		require.NoError(t, err)
		require.Len(t, artists, 3)

		repo.AssertExpectations(t)
	})
}
