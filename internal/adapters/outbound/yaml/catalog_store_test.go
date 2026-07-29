package yamlcatalog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestStoreSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{{
			Slug:           "wu-tang-clan",
			Name:           "Wu-Tang Clan",
			SpotifyID:      "34EP7KEpOjXcM2TCat1ISk",
			SpotifyIDs:     []string{"0H8YCcvC3MPLKnbDRasGiG"},
			Genres:         []string{"east coast hip hop", "hardcore hip hop"},
			Category:       catalog.CategoryCore,
			Roles:          []catalog.Category{catalog.CategoryCore},
			Aliases:        []string{"Wu Tang Clan"},
			Enabled:        true,
			EditorialOrder: 1,
			Notes:          "",
			ImageURL:       "https://i.scdn.co/image/artist-large",
		}},
	}

	require.NoError(t, store.Save(context.Background(), path, c))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(first), "genres:")
	require.Contains(t, string(first), "east coast hip hop")
	require.Contains(t, string(first), "image_url: https://i.scdn.co/image/artist-large")

	require.NoError(t, store.Save(context.Background(), path, c))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, first, second)

	loaded, err := store.Load(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, c, loaded)
}
