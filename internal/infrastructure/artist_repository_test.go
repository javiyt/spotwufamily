package infrastructure_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jarcoal/httpmock"
	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/javiyt/spotwufamily/internal/infrastructure"
	"github.com/stretchr/testify/require"
	"github.com/zmb3/spotify"
)

func TestArtistHTTPRepository_SearchArtist(t *testing.T) {
	client := &http.Client{}

	httpmock.ActivateNonDefault(client)

	defer httpmock.DeactivateAndReset()

	sc := spotify.NewClient(client)
	repoHTTP := infrastructure.NewArtistHTTPRepository(sc)

	t.Run("it should fail when API endpoint not found", func(t *testing.T) {
		httpmock.RegisterResponder(
			"GET",
			"https://api.spotify.com/v1/search?q=notfound&type=artist",
			httpmock.NewStringResponder(http.StatusNotFound, ""),
		)

		artists, err := repoHTTP.SearchArtist("notfound")

		require.EqualError(t, err, "error spotify: HTTP 404: Not Found (body empty) searching for artist notfound")
		require.Nil(t, artists)
	})

	t.Run("it should not fail when artist not found", func(t *testing.T) {
		bytes, err := os.ReadFile("testdata/search_artist_non_existing.json")
		if err != nil {
			t.Fatal(err)
		}

		httpmock.RegisterResponder(
			"GET",
			"https://api.spotify.com/v1/search?q=itdoesnotexist&type=artist",
			httpmock.NewStringResponder(http.StatusOK, string(bytes)),
		)

		artists, err := repoHTTP.SearchArtist("itdoesnotexist")

		require.NoError(t, err)
		require.Empty(t, artists)
	})

	t.Run("it should be possible to get all artists", func(t *testing.T) {
		bytes, err := os.ReadFile("testdata/search_artist_method_man.json")
		if err != nil {
			t.Fatal(err)
		}

		httpmock.RegisterResponder(
			"GET",
			"https://api.spotify.com/v1/search?q=method+man&type=artist",
			httpmock.NewStringResponder(http.StatusOK, string(bytes)),
		)

		artists, err := repoHTTP.SearchArtist("method man")

		require.NoError(t, err)
		require.Len(t, artists, 15)
	})
}

func TestFileArtistRepository_SaveArtist_CreateAndUpdate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "artists.json")

	repo := infrastructure.NewFileArtistRepository(dbPath)

	a1 := domain.NewArtist("id-1", "Artist One", "https://img/one.jpg")

	// Save first time (file does not exist)
	err := repo.SaveArtist(a1)
	require.NoError(t, err)

	// Read file and verify contents
	b, err := os.ReadFile(dbPath)
	require.NoError(t, err)

	var got struct {
		Artists []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"artists"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Artists, 1)
	require.Equal(t, "id-1", got.Artists[0].ID)
	require.Equal(t, "Artist One", got.Artists[0].Name)
	require.Equal(t, "https://img/one.jpg", got.Artists[0].Image)

	// Save updated info for same artist (should update, not duplicate)
	a1updated := domain.NewArtist("id-1", "Artist One Updated", "https://img/one-updated.jpg")
	err = repo.SaveArtist(a1updated)
	require.NoError(t, err)

	b, err = os.ReadFile(dbPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Artists, 1)
	require.Equal(t, "Artist One Updated", got.Artists[0].Name)
	require.Equal(t, "https://img/one-updated.jpg", got.Artists[0].Image)

	// Save a second artist
	a2 := domain.NewArtist("id-2", "Second Artist", "")
	err = repo.SaveArtist(a2)
	require.NoError(t, err)

	b, err = os.ReadFile(dbPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Artists, 2)
}
