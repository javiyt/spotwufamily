package artists_test

import (
	"context"
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
