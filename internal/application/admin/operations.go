package admin

import (
	"context"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// SyncReader is the outbound port for the latest sync run.
type SyncReader interface {
	LastSync(ctx context.Context) (SyncInfo, bool, error)
}

// Operations implements OperationsReader on top of read-only database ports.
type Operations struct {
	sync  SyncReader
	assoc artists.ReconcileStore
}

// NewOperations never calls the write methods of the reconcile store.
func NewOperations(sync SyncReader, assoc artists.ReconcileStore) Operations {
	return Operations{sync: sync, assoc: assoc}
}

func (o Operations) LastSync(ctx context.Context) (SyncInfo, bool, error) {
	return o.sync.LastSync(ctx)
}

// PendingReconciliation counts active associations whose credits point away
// from every linked profile of the artist. Unverifiable ones are not counted.
func (o Operations) PendingReconciliation(ctx context.Context, c catalog.EditorialCatalog) (int, error) {
	total := 0
	for _, a := range c.Artists {
		associations, err := o.assoc.ActiveAssociations(ctx, a.Slug)
		if err != nil {
			return 0, err
		}
		linked := map[string]struct{}{}
		for _, id := range a.AllSpotifyIDs() {
			linked[id] = struct{}{}
		}
		for _, as := range associations {
			if artists.ClassifyAssociation(as, linked) == artists.ReconcileOrphan {
				total++
			}
		}
	}
	return total, nil
}
