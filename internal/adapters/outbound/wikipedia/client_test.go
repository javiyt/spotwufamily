package wikipedia_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/wikipedia"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestClientDiscoversWuFamilyArtistsFromWikipediaSections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "parse", r.URL.Query().Get("action"))
		require.Equal(t, "List of Wu-Tang Clan affiliates", r.URL.Query().Get("page"))
		require.NotEmpty(t, r.Header.Get("User-Agent"))
		_, _ = fmt.Fprint(w, `{
			"parse": {
				"sections": [
					{"toclevel": 1, "line": "Groups", "anchor": "Groups"},
					{"toclevel": 2, "line": "A.I.G.", "anchor": "A.I.G."},
					{"toclevel": 2, "line": "Royal Fam", "anchor": "Royal_Fam"},
					{"toclevel": 1, "line": "Rappers", "anchor": "Rappers"},
					{"toclevel": 2, "line": "Killah Priest", "anchor": "Killah_Priest"},
					{"toclevel": 1, "line": "Producers", "anchor": "Producers"},
					{"toclevel": 2, "line": "4th Disciple", "anchor": "4th_Disciple"},
					{"toclevel": 1, "line": "References", "anchor": "References"}
				]
			}
		}`)
	}))
	defer server.Close()

	client := wikipedia.NewClient(wikipedia.Config{APIURL: server.URL, HTTPClient: server.Client()})

	discovered, err := client.DiscoverWuFamilyArtists(context.Background())

	require.NoError(t, err)
	require.Len(t, discovered, 4)
	require.Equal(t, "A.I.G.", discovered[0].Name)
	require.Equal(t, catalog.CategoryAffiliateGroup, discovered[0].Category)
	require.Contains(t, discovered[0].SourceURL, "List%20of%20Wu-Tang%20Clan%20affiliates#A.I.G.")
	require.Equal(t, "Killah Priest", discovered[2].Name)
	require.Equal(t, catalog.CategoryAffiliateArtist, discovered[2].Category)
	require.Equal(t, "4th Disciple", discovered[3].Name)
	require.Equal(t, catalog.CategoryProducer, discovered[3].Category)
}
