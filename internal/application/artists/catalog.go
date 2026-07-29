package artists

import (
	"context"
	"fmt"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type CatalogStore interface {
	Load(context.Context, string) (catalog.EditorialCatalog, error)
	Save(context.Context, string, catalog.EditorialCatalog) error
}

type ValidateCatalog struct {
	store CatalogStore
}

func NewValidateCatalog(store CatalogStore) ValidateCatalog {
	return ValidateCatalog{store: store}
}

func (v ValidateCatalog) Run(ctx context.Context, path string) ([]catalog.ValidationIssue, error) {
	c, err := v.store.Load(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("load artist catalog: %w", err)
	}

	return catalog.ValidateEditorialCatalog(c), nil
}
