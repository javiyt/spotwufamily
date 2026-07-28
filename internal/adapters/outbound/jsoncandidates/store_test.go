package jsoncandidates_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/jsoncandidates"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestSearcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	err := os.WriteFile(path, []byte(`{
		"wu-tang-clan": [
			{"name": "Wu-Tang Clan", "spotify_id": "34EP7KEpOjXcM2TCat1ISk"}
		]
	}`), 0o644)
	require.NoError(t, err)

	searcher, err := jsoncandidates.NewSearcher(path)
	require.NoError(t, err)

	candidates, err := searcher.SearchArtistCandidates(context.Background(), catalog.Artist{Slug: "wu-tang-clan"})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "Wu-Tang Clan", candidates[0].Name)
}
