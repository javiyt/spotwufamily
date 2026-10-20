package artists_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type relMemoryStore struct {
	catalog catalog.EditorialCatalog
	saves   int
	saveErr error
	loadErr error
}

func (m *relMemoryStore) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	if m.loadErr != nil {
		return catalog.EditorialCatalog{}, m.loadErr
	}
	return m.catalog.Clone(), nil
}

func (m *relMemoryStore) Save(_ context.Context, _ string, c catalog.EditorialCatalog) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saves++
	m.catalog = c.Clone()
	return nil
}

func relStore() *relMemoryStore {
	return &relMemoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", Category: catalog.CategoryCore, Roles: []catalog.Category{catalog.CategoryCore, catalog.CategoryAffiliateGroup}},
			{Slug: "rza", Name: "RZA", Category: catalog.CategoryCore},
			{Slug: "gza", Name: "GZA", Category: catalog.CategoryCore},
			{Slug: "cappadonna", Name: "Cappadonna", Category: catalog.CategoryCore},
		},
	}}
}

func member(from, to string) catalog.Relationship {
	return catalog.Relationship{
		Type: catalog.RelMemberOf, From: from, To: to, Status: catalog.RelationshipVerified,
		Confidence: catalog.ConfidenceHigh, Reviewer: "tester",
		Evidence: []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Wu-Tang_Clan", Rationale: "Lists the members."}},
	}
}

func service(store *relMemoryStore) artists.ManageRelationships {
	return artists.NewManageRelationships(store).WithClock(func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) })
}

func TestAddRelationshipSavesAndStampsReviewTime(t *testing.T) {
	store := relStore()
	change, err := service(store).Add(context.Background(), "x", member("rza", "wu-tang-clan"), false)
	require.NoError(t, err)
	require.False(t, change.DryRun)
	require.Equal(t, 1, store.saves)
	require.Len(t, store.catalog.Relationships, 1)
	require.Equal(t, "2026-10-20T12:00:00Z", store.catalog.Relationships[0].ReviewedAt)
}

func TestDryRunValidatesButNeverSaves(t *testing.T) {
	store := relStore()
	change, err := service(store).Add(context.Background(), "x", member("rza", "wu-tang-clan"), true)
	require.NoError(t, err)
	require.True(t, change.DryRun)
	require.NotNil(t, change.After)
	require.Zero(t, store.saves)
	require.Empty(t, store.catalog.Relationships)

	_, err = service(store).Add(context.Background(), "x", member("rza", "gza"), true)
	require.True(t, artists.IsInvalidRelationship(err), "dry run must report invalid edges: %v", err)
}

func TestAddRejectsInvalidDuplicateAndSelfEdgesWithoutSaving(t *testing.T) {
	store := relStore()
	svc := service(store)
	ctx := context.Background()
	_, err0 := svc.Add(ctx, "x", member("rza", "wu-tang-clan"), false)
	require.NoError(t, err0)

	_, err := svc.Add(ctx, "x", member("rza", "wu-tang-clan"), false)
	require.ErrorIs(t, err, catalog.ErrRelationshipExists)

	_, err = svc.Add(ctx, "x", member("nobody", "wu-tang-clan"), false)
	require.True(t, artists.IsInvalidRelationship(err))
	require.Contains(t, err.Error(), `unknown artist "nobody"`)

	self := member("rza", "rza")
	self.Type = catalog.RelAssociatedWith
	_, err = svc.Add(ctx, "x", self, false)
	require.Contains(t, err.Error(), "self relationship")

	require.Equal(t, 1, store.saves)
	require.Len(t, store.catalog.Relationships, 1)
}

func TestSaveFailureRollsBackAndLeavesCatalogUntouched(t *testing.T) {
	store := relStore()
	store.saveErr = errors.New("disk full")
	_, err := service(store).Add(context.Background(), "x", member("rza", "wu-tang-clan"), false)
	require.ErrorContains(t, err, "disk full")
	require.Empty(t, store.catalog.Relationships)

	store.saveErr = nil
	_, err = service(store).RenameArtist(context.Background(), "x", "rza", "the-rza", false)
	require.NoError(t, err)
	store.saveErr = errors.New("disk full")
	_, err = service(store).RenameArtist(context.Background(), "x", "the-rza", "rza", false)
	require.Error(t, err)
	require.Equal(t, "the-rza", store.catalog.Artists[1].Slug)
}

func TestUpdateAndRemoveRelationship(t *testing.T) {
	store := relStore()
	svc := service(store)
	ctx := context.Background()
	_, err := svc.Add(ctx, "x", member("cappadonna", "wu-tang-clan"), false)
	require.NoError(t, err)

	key := catalog.NewRelationshipKey(catalog.RelMemberOf, "cappadonna", "wu-tang-clan")
	updated := member("cappadonna", "wu-tang-clan")
	updated.Commentary = "Affiliate first, full member on later albums."
	change, err := svc.Update(ctx, "x", key, updated, false)
	require.NoError(t, err)
	require.NotNil(t, change.Before)
	require.Equal(t, "Affiliate first, full member on later albums.", store.catalog.Relationships[0].Commentary)

	_, err = svc.Update(ctx, "x", catalog.NewRelationshipKey(catalog.RelMemberOf, "rza", "wu-tang-clan"), updated, false)
	require.ErrorIs(t, err, catalog.ErrRelationshipNotFound)

	_, err = svc.Remove(ctx, "x", key, true)
	require.NoError(t, err)
	require.Len(t, store.catalog.Relationships, 1, "dry-run removal must keep the edge")
	_, err = svc.Remove(ctx, "x", key, false)
	require.NoError(t, err)
	require.Empty(t, store.catalog.Relationships)
	require.Len(t, store.catalog.Artists, 4, "removing an edge never removes artists")
}

func TestRenameUpdatesReferencesAndRemoveArtistIsBlocked(t *testing.T) {
	store := relStore()
	svc := service(store)
	ctx := context.Background()
	_, err := svc.Add(ctx, "x", member("rza", "wu-tang-clan"), false)
	require.NoError(t, err)

	change, err := svc.RenameArtist(ctx, "x", "rza", "the-rza", false)
	require.NoError(t, err)
	require.Len(t, change.Rewritten, 1)
	require.Equal(t, "the-rza", store.catalog.Relationships[0].From)

	_, err = svc.RenameArtist(ctx, "x", "the-rza", "gza", false)
	require.Error(t, err, "renaming onto an existing slug must be blocked")

	_, err = svc.RemoveArtist(ctx, "x", "the-rza", false)
	require.ErrorIs(t, err, catalog.ErrArtistReferenced)
	require.Contains(t, err.Error(), "member_of the-rza -> wu-tang-clan")

	_, err = svc.RemoveArtist(ctx, "x", "gza", false)
	require.NoError(t, err)
}

func TestReverseLookupsAndEstablishedFilter(t *testing.T) {
	store := relStore()
	svc := service(store)
	ctx := context.Background()
	_, err := svc.Add(ctx, "x", member("rza", "wu-tang-clan"), false)
	require.NoError(t, err)
	proposed := member("gza", "wu-tang-clan")
	proposed.Status = catalog.RelationshipProposed
	_, err = svc.Add(ctx, "x", proposed, false)
	require.NoError(t, err)
	former := member("cappadonna", "wu-tang-clan")
	former.End = "2010"
	_, err = svc.Add(ctx, "x", former, false)
	require.NoError(t, err)

	query := artists.NewQueryRelationships(store)
	all, err := query.MembersOf(ctx, "x", "wu-tang-clan", false)
	require.NoError(t, err)
	require.Equal(t, []string{"cappadonna", "gza", "rza"}, slugs(all))
	require.True(t, all[0].Relationship.Former())

	facts, err := query.MembersOf(ctx, "x", "wu-tang-clan", true)
	require.NoError(t, err)
	require.Equal(t, []string{"cappadonna", "rza"}, slugs(facts), "proposed claims are not facts")

	groups, err := query.GroupsFor(ctx, "x", "rza", true)
	require.NoError(t, err)
	require.Equal(t, []string{"wu-tang-clan"}, slugs(groups))

	_, err = query.MembersOf(ctx, "x", "nobody", false)
	require.ErrorIs(t, err, catalog.ErrUnknownArtist)

	list, err := query.List(ctx, "x", artists.RelationshipFilter{Slug: "gza", EstablishedOnly: true})
	require.NoError(t, err)
	require.Empty(t, list)
}

func slugs(entries []artists.RelationshipEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Artist.Slug)
	}
	return out
}
