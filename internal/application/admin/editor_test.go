package admin_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// memStore is an in-memory CatalogStore whose Update is serialised, like the
// YAML adapter's, so concurrency behaviour can be tested without a filesystem.
type memStore struct {
	mu      sync.Mutex
	c       catalog.EditorialCatalog
	saveErr error
	saves   int
}

func (m *memStore) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c.Clone(), nil
}

func (m *memStore) Save(_ context.Context, _ string, c catalog.EditorialCatalog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked(c)
}

func (m *memStore) saveLocked(c catalog.EditorialCatalog) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.c = c.Clone()
	m.saves++
	return nil
}

func (m *memStore) Update(_ context.Context, _ string, fn func(catalog.EditorialCatalog) (catalog.EditorialCatalog, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := fn(m.c.Clone())
	if err != nil {
		return err
	}
	return m.saveLocked(next)
}

func editorFixture() (*memStore, admin.Service) {
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", SpotifyID: id(1), Category: catalog.CategoryCore, Roles: []catalog.Category{catalog.CategoryCore, catalog.CategoryAffiliateGroup}, Aliases: []string{"Wu Tang"}, Enabled: true, EditorialOrder: 1,
				Genres: []string{"hardcore hip hop"}, AddedAt: "2026-01-01",
				SpotifyProfiles: []catalog.SpotifyProfile{{ID: id(1), Status: catalog.ProfileVerified, Reviewer: "javi"}}},
			{Slug: "method-man", Name: "Method Man", SpotifyID: id(2), Category: catalog.CategoryCore, Enabled: true, EditorialOrder: 2},
			{Slug: "killarmy", Name: "Killarmy", Category: catalog.CategoryAffiliateGroup, Aliases: []string{"Killarmy Crew"}},
			{Slug: "rza", Name: "RZA", Category: catalog.CategoryProducer},
		},
		Relationships: []catalog.Relationship{{
			Type: catalog.RelMemberOf, From: "method-man", To: "wu-tang-clan", Status: catalog.RelationshipVerified,
			Confidence: catalog.ConfidenceHigh, Reviewer: "javi", ReviewedAt: "2026-01-02T00:00:00Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Method_Man", Rationale: "founding member"}},
		}},
	}
	store := &memStore{c: c}
	return store, admin.NewService(store, nil, nil, "data/artists.yaml")
}

func input(slug, name string) admin.ArtistInput {
	return admin.ArtistInput{Slug: slug, Name: name, Category: catalog.CategoryCollaborator}
}

func detail(t *testing.T, svc admin.Service, slug string) admin.ArtistDetail {
	t.Helper()
	d, err := svc.Artist(context.Background(), slug)
	require.NoError(t, err)
	return d
}

func TestListArtistsFiltersSortsAndReportsFacets(t *testing.T) {
	_, svc := editorFixture()
	ctx := context.Background()

	byCategory, err := svc.ListArtists(ctx, admin.ListQuery{Category: catalog.CategoryCore})
	require.NoError(t, err)
	require.Equal(t, 2, byCategory.Total)

	enabled := true
	byEnabled, err := svc.ListArtists(ctx, admin.ListQuery{Enabled: &enabled})
	require.NoError(t, err)
	require.Equal(t, 2, byEnabled.Total)

	unresolved, err := svc.ListArtists(ctx, admin.ListQuery{IDState: admin.IDUnresolved})
	require.NoError(t, err)
	require.Equal(t, 2, unresolved.Total)

	verified, err := svc.ListArtists(ctx, admin.ListQuery{IDState: admin.IDVerified})
	require.NoError(t, err)
	require.Equal(t, 1, verified.Total)
	require.Equal(t, "wu-tang-clan", verified.Items[0].Slug)

	byRel, err := svc.ListArtists(ctx, admin.ListQuery{Relation: catalog.RelMemberOf})
	require.NoError(t, err)
	require.Equal(t, 2, byRel.Total, "both endpoints of a member_of edge match")

	byAlias, err := svc.ListArtists(ctx, admin.ListQuery{Search: "killarmy crew"})
	require.NoError(t, err)
	require.Equal(t, 1, byAlias.Total)
	require.Equal(t, "killarmy", byAlias.Items[0].Slug)

	sorted, err := svc.ListArtists(ctx, admin.ListQuery{Sort: admin.SortSlug, Desc: true})
	require.NoError(t, err)
	require.Equal(t, "wu-tang-clan", sorted.Items[0].Slug)

	ordered, err := svc.ListArtists(ctx, admin.ListQuery{Sort: admin.SortOrder})
	require.NoError(t, err)
	require.Equal(t, "wu-tang-clan", ordered.Items[0].Slug)
	require.Equal(t, "method-man", ordered.Items[1].Slug)

	require.Equal(t, admin.Facets{All: 4, Enabled: 2, Disabled: 2, Verified: 1, Pending: 1, Unresolved: 2}, byCategory.Facets)
}

func TestListArtistsHidesArchivedUnlessAsked(t *testing.T) {
	_, svc := editorFixture()
	ctx := context.Background()
	d := detail(t, svc, "rza")
	_, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeArchive, Slug: "rza", Revision: d.Revision})
	require.NoError(t, err)

	visible, err := svc.ListArtists(ctx, admin.ListQuery{})
	require.NoError(t, err)
	require.Equal(t, 3, visible.Total)
	all, err := svc.ListArtists(ctx, admin.ListQuery{IncludeArchived: true})
	require.NoError(t, err)
	require.Equal(t, 4, all.Total)
	require.Equal(t, 1, all.Facets.Archived)
}

func TestCreateArtistPersistsAndPreviewDoesNot(t *testing.T) {
	store, svc := editorFixture()
	ctx := context.Background()
	in := input("ghostface-killah", "Ghostface Killah")
	in.Aliases = []string{"Tony Starks", "  ", "Ironman"}

	preview, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeCreate, Input: in, DryRun: true})
	require.NoError(t, err)
	require.True(t, preview.DryRun)
	require.Zero(t, store.saves)
	require.Contains(t, preview.Changes, admin.FieldChange{Field: "name", After: "Ghostface Killah"})

	res, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeCreate, Input: in})
	require.NoError(t, err)
	require.Equal(t, "ghostface-killah", res.Slug)
	require.Equal(t, 1, store.saves)

	d := detail(t, svc, "ghostface-killah")
	require.Equal(t, []string{"Tony Starks", "Ironman"}, d.Artist.Aliases)
	require.Equal(t, res.Revision, d.Revision)
	require.False(t, d.Artist.Enabled, "new entries start disabled")
}

func TestCreateArtistReportsFieldLevelValidationErrors(t *testing.T) {
	store, svc := editorFixture()
	bad := input("Bad Slug", "Method Man")
	bad.ExternalURL = "http://example.com"
	bad.Enabled = true

	_, err := svc.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeCreate, Input: bad})

	var invalid *admin.ValidationError
	require.ErrorAs(t, err, &invalid)
	fields := map[string]string{}
	for _, issue := range invalid.Issues {
		fields[issue.Field] = issue.Message
	}
	require.Contains(t, fields, "slug")
	require.Contains(t, fields, "name", "duplicate name is reported on the name field")
	require.Contains(t, fields, "external_url")
	require.Contains(t, fields, "spotify_id", "enabling needs a reviewed Spotify profile")
	require.Zero(t, store.saves)
}

func TestUpdateKeepsSpotifyMappingsAndUnrelatedFields(t *testing.T) {
	_, svc := editorFixture()
	ctx := context.Background()
	d := detail(t, svc, "wu-tang-clan")

	in := admin.InputFrom(d.Artist)
	in.Notes = "Staten Island collective"
	in.Aliases = []string{"Wu Tang", "WTC"}
	in.ExternalURL = "https://en.wikipedia.org/wiki/Wu-Tang_Clan"
	res, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "wu-tang-clan", Revision: d.Revision, Input: in})
	require.NoError(t, err)
	require.NotEqual(t, d.Revision, res.Revision)

	after := detail(t, svc, "wu-tang-clan").Artist
	require.Equal(t, d.Artist.SpotifyProfiles, after.SpotifyProfiles)
	require.Equal(t, d.Artist.SpotifyID, after.SpotifyID)
	require.Equal(t, d.Artist.Genres, after.Genres)
	require.Equal(t, d.Artist.AddedAt, after.AddedAt)
	require.Equal(t, []string{"Wu Tang", "WTC"}, after.Aliases, "alias order is preserved")
	require.Equal(t, 1, after.EditorialOrder)
}

func TestUpdateRejectsStaleRevisionAndNamesChangedFields(t *testing.T) {
	store, svc := editorFixture()
	ctx := context.Background()
	d := detail(t, svc, "method-man")

	// Another tab saves first.
	first := admin.InputFrom(d.Artist)
	first.Notes = "from tab one"
	_, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "method-man", Revision: d.Revision, Input: first})
	require.NoError(t, err)
	saves := store.saves

	second := admin.InputFrom(d.Artist)
	second.Notes = "from tab two"
	second.Aliases = []string{"Johnny Blaze"}
	_, err = svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "method-man", Revision: d.Revision, Input: second})

	var conflict *admin.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "method-man", conflict.Slug)
	require.Equal(t, detail(t, svc, "method-man").Revision, conflict.CurrentRevision)
	require.Equal(t, []admin.FieldChange{{Field: "notes", Before: "", After: "from tab one"}}, conflict.Changed)
	require.Equal(t, saves, store.saves, "a conflict never writes")
	require.Equal(t, "from tab one", detail(t, svc, "method-man").Artist.Notes)
}

func TestConcurrentSavesOfOneRevisionAllowExactlyOneWinner(t *testing.T) {
	_, svc := editorFixture()
	d := detail(t, svc, "method-man")

	const writers = 8
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := admin.InputFrom(d.Artist)
			in.Notes = time.Now().String()
			_, err := svc.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "method-man", Revision: d.Revision, Input: in})
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	wins, conflicts := 0, 0
	for err := range results {
		var conflict *admin.ConflictError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &conflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 1, wins)
	require.Equal(t, writers-1, conflicts)
}

func TestSlugChangeNeedsConfirmationAndRewritesRelationships(t *testing.T) {
	store, svc := editorFixture()
	ctx := context.Background()
	d := detail(t, svc, "method-man")
	in := admin.InputFrom(d.Artist)
	in.Slug = "meth"

	_, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "method-man", Revision: d.Revision, Input: in})
	var confirm *admin.ConfirmationError
	require.ErrorAs(t, err, &confirm)
	require.Len(t, confirm.References, 1, "inbound references are shown before renaming")
	require.Zero(t, store.saves)

	res, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "method-man", Revision: d.Revision, Input: in, ConfirmSlugChange: true})
	require.NoError(t, err)
	require.Equal(t, "meth", res.Slug)
	require.Equal(t, "meth", store.c.Relationships[0].From)
}

func TestArchiveKeepsRelationshipsAndDisables(t *testing.T) {
	store, svc := editorFixture()
	ctx := context.Background()
	d := detail(t, svc, "method-man")

	_, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeArchive, Slug: "method-man", Revision: d.Revision})
	require.NoError(t, err)
	a := detail(t, svc, "method-man")
	require.True(t, a.Artist.Archived)
	require.False(t, a.Artist.Enabled)
	require.Len(t, store.c.Relationships, 1)

	_, err = svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeRestore, Slug: "method-man", Revision: a.Revision})
	require.NoError(t, err)
	require.False(t, detail(t, svc, "method-man").Artist.Archived)
}

func TestDeleteRequiresConfirmationAndRefusesReferencedArtists(t *testing.T) {
	store, svc := editorFixture()
	ctx := context.Background()

	ref := detail(t, svc, "method-man")
	_, err := svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeDelete, Slug: "method-man", Revision: ref.Revision, ConfirmDelete: true})
	var referenced *admin.ReferencedError
	require.ErrorAs(t, err, &referenced)
	require.Len(t, referenced.References, 1)
	require.Len(t, store.c.Artists, 4)

	free := detail(t, svc, "rza")
	_, err = svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeDelete, Slug: "rza", Revision: free.Revision})
	var confirm *admin.ConfirmationError
	require.ErrorAs(t, err, &confirm)
	require.Len(t, store.c.Artists, 4)

	_, err = svc.ApplyChange(ctx, admin.ChangeRequest{Kind: admin.ChangeDelete, Slug: "rza", Revision: free.Revision, ConfirmDelete: true})
	require.NoError(t, err)
	require.Len(t, store.c.Artists, 3)
}

func TestPreexistingCatalogProblemsDoNotBlockUnrelatedEdits(t *testing.T) {
	store, svc := editorFixture()
	store.c.Artists[3].ExternalURL = "http://legacy.example" // already invalid
	d := detail(t, svc, "killarmy")
	in := admin.InputFrom(d.Artist)
	in.Notes = "fine"

	_, err := svc.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: "killarmy", Revision: d.Revision, Input: in})
	require.NoError(t, err)
}

func TestFilesystemFailureSurfacesAndLeavesCatalogUntouched(t *testing.T) {
	store, svc := editorFixture()
	store.saveErr = errors.New("disk full")
	_, err := svc.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeCreate, Input: input("ghostface-killah", "Ghostface Killah")})
	require.ErrorContains(t, err, "disk full")
	require.Len(t, store.c.Artists, 4)
}

func TestEditingNeedsAStoreThatCanUpdate(t *testing.T) {
	svc := admin.NewService(fakeStore{c: sampleCatalog()}, nil, nil, "x")
	_, err := svc.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeCreate, Input: input("a-b", "A B")})
	require.ErrorIs(t, err, admin.ErrReadOnly)
	require.False(t, svc.Editable())
}

func TestRelationshipEditsUseDomainValidation(t *testing.T) {
	store, svc := editorFixture()
	ctx := context.Background()

	// Official membership without independent evidence is refused.
	_, err := svc.ApplyRelationship(ctx, admin.RelationshipRequest{Kind: admin.RelAdd, Input: admin.RelationshipInput{
		Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipVerified,
		Reviewer: "javi", Confidence: catalog.ConfidenceHigh,
		EvidenceType: catalog.EvidenceNameMatch, EvidenceRationale: "same name",
	}})
	var invalid *admin.ValidationError
	require.ErrorAs(t, err, &invalid)
	require.Len(t, store.c.Relationships, 1)

	// Proposed membership needs no evidence and is clearly unverified.
	res, err := svc.ApplyRelationship(ctx, admin.RelationshipRequest{Kind: admin.RelAdd, Input: admin.RelationshipInput{
		Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipProposed,
	}})
	require.NoError(t, err)
	require.Len(t, store.c.Relationships, 2)

	// Verifying it with sourced evidence works, with a provenance period.
	_, err = svc.ApplyRelationship(ctx, admin.RelationshipRequest{Kind: admin.RelUpdate, Key: res.Key, Revision: res.Revision, Input: admin.RelationshipInput{
		Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipVerified,
		Reviewer: "javi", Confidence: catalog.ConfidenceHigh, Start: "1992",
		EvidenceType: catalog.EvidenceWikipedia, EvidenceURL: "https://en.wikipedia.org/wiki/RZA", EvidenceRationale: "founding member",
	}})
	require.NoError(t, err)
	var stored catalog.Relationship
	for _, r := range store.c.Relationships {
		if r.From == "rza" {
			stored = r
		}
	}
	require.Equal(t, catalog.RelationshipVerified, stored.Status)
	require.Equal(t, "1992", stored.Start)
	require.NotEmpty(t, stored.ReviewedAt)
	require.Len(t, stored.Evidence, 1)
}

func TestRelationshipUpdateAndRemoveDetectStaleRevision(t *testing.T) {
	_, svc := editorFixture()
	ctx := context.Background()
	d := detail(t, svc, "method-man")
	require.Len(t, d.Relationships, 1)
	rel := d.Relationships[0]

	_, err := svc.ApplyRelationship(ctx, admin.RelationshipRequest{Kind: admin.RelRemove, Key: rel.Key, Revision: "stale"})
	var conflict *admin.ConflictError
	require.ErrorAs(t, err, &conflict)

	_, err = svc.ApplyRelationship(ctx, admin.RelationshipRequest{Kind: admin.RelRemove, Key: rel.Key, Revision: rel.Revision})
	require.NoError(t, err)
	require.Empty(t, detail(t, svc, "method-man").Relationships)
}

func TestGroupDetailListsMembersWithProvenance(t *testing.T) {
	_, svc := editorFixture()
	d := detail(t, svc, "wu-tang-clan")
	require.True(t, d.IsGroup)
	require.Len(t, d.Relationships, 1)
	require.Equal(t, "method-man", d.Relationships[0].OtherSlug)
	require.Equal(t, "Method Man", d.Relationships[0].OtherName)
	require.Equal(t, "inbound", d.Relationships[0].Direction)
	require.Equal(t, catalog.RelationshipVerified, d.Relationships[0].Relationship.Status)
}

func TestArtistDetailUnknownSlug(t *testing.T) {
	_, svc := editorFixture()
	_, err := svc.Artist(context.Background(), "nobody")
	require.ErrorIs(t, err, admin.ErrNotFound)
}

func TestChoicesListEveryArtistBySlugAndName(t *testing.T) {
	_, svc := editorFixture()
	choices, err := svc.Choices(context.Background())
	require.NoError(t, err)
	require.Len(t, choices, 4)
	require.Equal(t, admin.Choice{Slug: "killarmy", Name: "Killarmy", Group: true}, choices[0])
}
