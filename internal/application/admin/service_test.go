package admin_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type fakeStore struct {
	c   catalog.EditorialCatalog
	err error
}

func (f fakeStore) Load(context.Context, string) (catalog.EditorialCatalog, error) { return f.c, f.err }
func (f fakeStore) Save(context.Context, string, catalog.EditorialCatalog) error {
	return errors.New("read-only")
}

type fakeOps struct {
	sync    admin.SyncInfo
	hasSync bool
	pending int
	err     error
}

func (f fakeOps) LastSync(context.Context) (admin.SyncInfo, bool, error) {
	return f.sync, f.hasSync, f.err
}

func (f fakeOps) PendingReconciliation(context.Context, catalog.EditorialCatalog) (int, error) {
	return f.pending, f.err
}

func id(n int) string { return fmt.Sprintf("%022d", n) }

func sampleCatalog() catalog.EditorialCatalog {
	return catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", SpotifyID: id(1), Category: catalog.CategoryCore, Enabled: true,
				SpotifyProfiles: []catalog.SpotifyProfile{{ID: id(1), Status: catalog.ProfileVerified}}},
			{Slug: "method-man", Name: "Method Man", SpotifyID: id(2), Category: catalog.CategoryCore, Enabled: true,
				SpotifyProfiles: []catalog.SpotifyProfile{{ID: id(2), Status: catalog.ProfileCandidate}}},
			{Slug: "killarmy", Name: "Killarmy", Category: catalog.CategoryAffiliateGroup, Roles: []catalog.Category{catalog.CategoryAffiliateGroup}},
			{Slug: "<script>x", Name: "<script>alert(1)</script>", Category: catalog.CategoryCollaborator},
		},
		Relationships: []catalog.Relationship{
			{Type: catalog.RelMemberOf, From: "method-man", To: "wu-tang-clan", Status: catalog.RelationshipProposed},
		},
	}
}

func newService(c catalog.EditorialCatalog, ops fakeOps, creds bool) admin.Service {
	return admin.NewService(fakeStore{c: c}, ops, func() bool { return creds }, "data/artists.yaml")
}

func TestDashboardCountsIDsGroupsAndCredentials(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc := newService(sampleCatalog(), fakeOps{sync: admin.SyncInfo{Status: "finished", FinishedAt: when}, hasSync: true, pending: 3}, true)

	d, err := svc.Dashboard(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, d.Artists)
	require.Equal(t, 2, d.Enabled)
	require.Equal(t, 1, d.VerifiedIDs)
	require.Equal(t, 1, d.PendingIDs)
	require.Equal(t, 2, d.UnresolvedIDs)
	require.Equal(t, 2, d.Groups)
	require.Equal(t, 2, d.People)
	require.Equal(t, 3, d.PendingReconciliation)
	require.True(t, d.DatabaseAvailable)
	require.True(t, d.SpotifyCredentialsPresent)
	require.NotNil(t, d.LastSync)
	require.Equal(t, when, d.LastSync.FinishedAt)
	require.GreaterOrEqual(t, d.ReviewQueue, 2) // pending profile + proposed relationship
}

func TestDashboardOfflineStillServesCatalog(t *testing.T) {
	svc := newService(sampleCatalog(), fakeOps{err: errors.New("no database")}, false)

	d, err := svc.Dashboard(context.Background())
	require.NoError(t, err)
	require.False(t, d.DatabaseAvailable)
	require.False(t, d.SpotifyCredentialsPresent)
	require.Nil(t, d.LastSync)
	require.Equal(t, 4, d.Artists)
}

func TestDashboardCatalogErrorIsReturned(t *testing.T) {
	svc := admin.NewService(fakeStore{err: errors.New("boom")}, fakeOps{}, func() bool { return false }, "x")
	_, err := svc.Dashboard(context.Background())
	require.Error(t, err)
}

func TestListArtistsSearchesAndPaginates(t *testing.T) {
	c := sampleCatalog()
	for i := 0; i < 30; i++ {
		c.Artists = append(c.Artists, catalog.Artist{Slug: fmt.Sprintf("extra-%02d", i), Name: fmt.Sprintf("Extra %02d", i), Category: catalog.CategoryCollaborator})
	}
	svc := newService(c, fakeOps{}, false)

	page, err := svc.ListArtists(context.Background(), admin.ListQuery{Page: 2, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 34, page.Total)
	require.Equal(t, 4, page.Pages)
	require.Equal(t, 2, page.Page)
	require.Len(t, page.Items, 10)

	hit, err := svc.ListArtists(context.Background(), admin.ListQuery{Search: "METHOD"})
	require.NoError(t, err)
	require.Len(t, hit.Items, 1)
	require.Equal(t, "method-man", hit.Items[0].Slug)
}

func TestListArtistsClampsPaging(t *testing.T) {
	svc := newService(sampleCatalog(), fakeOps{}, false)

	page, err := svc.ListArtists(context.Background(), admin.ListQuery{Page: 99, PageSize: 100000})
	require.NoError(t, err)
	require.Equal(t, 1, page.Page)
	require.LessOrEqual(t, page.PageSize, admin.MaxPageSize)

	page, err = svc.ListArtists(context.Background(), admin.ListQuery{Page: -5, PageSize: -1})
	require.NoError(t, err)
	require.Equal(t, 1, page.Page)
	require.Equal(t, admin.DefaultPageSize, page.PageSize)
}

func TestArtistIDStateClassification(t *testing.T) {
	svc := newService(sampleCatalog(), fakeOps{}, false)
	page, err := svc.ListArtists(context.Background(), admin.ListQuery{})
	require.NoError(t, err)
	states := map[string]admin.IDState{}
	for _, it := range page.Items {
		states[it.Slug] = it.IDState
	}
	require.Equal(t, admin.IDVerified, states["wu-tang-clan"])
	require.Equal(t, admin.IDPending, states["method-man"])
	require.Equal(t, admin.IDUnresolved, states["killarmy"])
}

func TestListGroupsIncludesMembersWithoutAssumingVerified(t *testing.T) {
	svc := newService(sampleCatalog(), fakeOps{}, false)
	page, err := svc.ListGroups(context.Background(), admin.ListQuery{})
	require.NoError(t, err)
	byslug := map[string]admin.GroupRow{}
	for _, g := range page.Items {
		byslug[g.Slug] = g
	}
	require.Contains(t, byslug, "killarmy")
	require.Contains(t, byslug, "wu-tang-clan")
	require.Equal(t, 0, byslug["wu-tang-clan"].VerifiedMembers)
	require.Equal(t, 1, byslug["wu-tang-clan"].ProposedMembers)
}

func TestReviewQueueListsPendingProfilesAndProposedRelationships(t *testing.T) {
	svc := newService(sampleCatalog(), fakeOps{}, false)
	page, err := svc.ReviewQueue(context.Background(), admin.ListQuery{})
	require.NoError(t, err)
	kinds := map[string]bool{}
	for _, it := range page.Items {
		kinds[it.Kind] = true
	}
	require.True(t, kinds["spotify_profile"])
	require.True(t, kinds["relationship"])
}
