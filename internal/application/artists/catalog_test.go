package artists_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	catalog   catalog.EditorialCatalog
	saveCount int
}

func (m *memoryStore) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return m.catalog, nil
}

func (m *memoryStore) Save(_ context.Context, _ string, c catalog.EditorialCatalog) error {
	m.catalog = c
	m.saveCount++
	return nil
}

func TestValidateCatalog(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{Version: 1}}

	issues, err := artists.NewValidateCatalog(store).Run(context.Background(), "ignored")

	require.NoError(t, err)
	require.NotEmpty(t, issues)
}

func TestImportGroupsBuildsDeterministicCatalogAndMergesRoles(t *testing.T) {
	var input bytes.Buffer
	input.WriteString("Wu-Tang Clan\n")
	for i := 2; i <= 41; i++ {
		input.WriteString(fmt.Sprintf("Artist %d\n", i))
	}
	input.WriteString("Bronze Nazareth\n")
	for i := 43; i <= 91; i++ {
		input.WriteString(fmt.Sprintf("Later %d\n", i))
	}
	input.WriteString("Bronze Nazareth\n")

	store := &memoryStore{}
	result, err := artists.NewImportGroups(store).Run(context.Background(), &input, "ignored")

	require.NoError(t, err)
	require.Equal(t, 91, result.Artists)
	require.Equal(t, []string{"Bronze Nazareth"}, result.ExactDuplicates)
	require.Equal(t, "wu-tang-clan", store.catalog.Artists[0].Slug)
	require.Equal(t, []string{"Wu Tang Clan"}, store.catalog.Artists[0].Aliases)

	var bronze catalog.Artist
	for _, artist := range store.catalog.Artists {
		if artist.Name == "Bronze Nazareth" {
			bronze = artist
			break
		}
	}
	require.Equal(t, []catalog.Category{catalog.CategoryAffiliateArtist, catalog.CategoryProducer}, bronze.Roles)
}
