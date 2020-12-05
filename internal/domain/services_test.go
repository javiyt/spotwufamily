package domain_test

import (
	"io/ioutil"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/stretchr/testify/require"
	"github.com/zmb3/spotify"
)

func TestSearchArtists_GetArtists(t *testing.T) {
	client := &http.Client{}

	httpmock.ActivateNonDefault(client)
	defer httpmock.DeactivateAndReset()

	sc := spotify.NewClient(client)
	s := domain.NewSearchArtists(sc)

	t.Run("it should fail when API endpoint not found", func(t *testing.T) {
		httpmock.RegisterResponder(
			"GET",
			"https://api.spotify.com/v1/search?q=notfound&type=artist",
			httpmock.NewStringResponder(http.StatusNotFound, ""),
		)

		artists, err := s.GetArtists([]string{"notfound"})

		require.EqualError(t, err, "error spotify: HTTP 404: Not Found (body empty) searching for artist notfound")
		require.Nil(t, artists)
	})

	t.Run("it should not fail when artist not found", func(t *testing.T) {
		bytes, err := ioutil.ReadFile("testdata/search_artist_non_existing.json")
		if err != nil {
			t.Fatal(err)
		}

		httpmock.RegisterResponder(
			"GET",
			"https://api.spotify.com/v1/search?q=itdoesnotexist&type=artist",
			httpmock.NewStringResponder(http.StatusOK, string(bytes)),
		)

		artists, err := s.GetArtists([]string{"itdoesnotexist"})

		require.NoError(t, err)
		require.Empty(t, artists)
	})

	t.Run("it should be possible to get all artists", func(t *testing.T) {
		bytes, err := ioutil.ReadFile("testdata/search_artist_method_man.json")
		if err != nil {
			t.Fatal(err)
		}

		httpmock.RegisterResponder(
			"GET",
			"https://api.spotify.com/v1/search?q=method+man&type=artist",
			httpmock.NewStringResponder(http.StatusOK, string(bytes)),
		)

		artists, err := s.GetArtists([]string{"method man"})

		require.NoError(t, err)
		require.Len(t, artists, 15)
	})
}
