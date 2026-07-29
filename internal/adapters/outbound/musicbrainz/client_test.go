package musicbrainz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/musicbrainz"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestSearchArtistAlbumReleaseGroups(t *testing.T) {
	var query string
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("query")
		userAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "count": 2,
  "release-groups": [
    {"id": "mb-1", "title": "Enter the Wu-Tang (36 Chambers)", "first-release-date": "1993-11-09", "primary-type": "Album"},
    {"id": "mb-2", "title": "C.R.E.A.M.", "first-release-date": "1994", "primary-type": "Single"}
  ]
}`))
	}))
	defer server.Close()

	client := musicbrainz.NewClient(musicbrainz.Config{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		UserAgent:  "spotwufamily-test",
	})

	releaseGroups, err := client.SearchArtistAlbumReleaseGroups(context.Background(), catalog.Artist{Name: "Wu-Tang Clan"})

	require.NoError(t, err)
	require.Equal(t, `artist:"Wu-Tang Clan" AND primarytype:album`, query)
	require.Equal(t, "spotwufamily-test", userAgent)
	require.Equal(t, "mb-1", releaseGroups[0].ID)
	require.Equal(t, "https://musicbrainz.org/release-group/mb-1", releaseGroups[0].URL)
	require.Len(t, releaseGroups, 1)
}
