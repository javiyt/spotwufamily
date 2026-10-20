package admin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type fakeAssoc struct {
	bySlug map[string][]artists.Association
	err    error
}

func (f fakeAssoc) ActiveAssociations(_ context.Context, slug string) ([]artists.Association, error) {
	return f.bySlug[slug], f.err
}

type fakeSyncReader struct {
	info admin.SyncInfo
	ok   bool
	err  error
}

func (f fakeSyncReader) LastSync(context.Context) (admin.SyncInfo, bool, error) {
	return f.info, f.ok, f.err
}

func TestOperationsCountsOnlyOrphanAssociations(t *testing.T) {
	linked := id(1)
	c := catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "a", Name: "A", SpotifyID: linked},
		{Slug: "b", Name: "B"}, // no linked ID: credited entities are orphans
	}}
	store := fakeAssoc{bySlug: map[string][]artists.Association{
		"a": {
			{Kind: artists.AssociationAlbum, SpotifyID: "x", CreditedArtistIDs: []string{linked}}, // supported
			{Kind: artists.AssociationAlbum, SpotifyID: "y", CreditedArtistIDs: []string{id(9)}},  // orphan
			{Kind: artists.AssociationTrack, SpotifyID: "z"},                                      // unverifiable
		},
		"b": {{Kind: artists.AssociationTrack, SpotifyID: "w", CreditedArtistIDs: []string{id(9)}}}, // orphan
	}}
	when := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	ops := admin.NewOperations(fakeSyncReader{info: admin.SyncInfo{Status: "finished", FinishedAt: when}, ok: true}, store)

	n, err := ops.PendingReconciliation(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	info, ok, err := ops.LastSync(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, when, info.FinishedAt)
}

func TestOperationsPropagatesDatabaseErrors(t *testing.T) {
	ops := admin.NewOperations(fakeSyncReader{err: errors.New("no such table")}, fakeAssoc{err: errors.New("no such table")})
	_, _, err := ops.LastSync(context.Background())
	require.Error(t, err)
	_, err = ops.PendingReconciliation(context.Background(), catalog.EditorialCatalog{Artists: []catalog.Artist{{Slug: "a"}}})
	require.Error(t, err)
}
