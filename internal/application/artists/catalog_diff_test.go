package artists

import (
	"reflect"
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func diffArtist(slug string, ids ...string) catalog.Artist {
	profiles := make([]catalog.SpotifyProfile, 0, len(ids))
	for _, id := range ids {
		profiles = append(profiles, catalog.SpotifyProfile{ID: id, Status: catalog.ProfileVerified})
	}

	return catalog.Artist{
		Slug: slug, Name: slug, PublicName: slug, SpotifyIDs: ids, SpotifyProfiles: profiles,
		Genres: []string{"pop", "rock"}, Category: catalog.CategoryCore, Enabled: true,
	}
}

func diffCatalog(artists ...catalog.Artist) catalog.EditorialCatalog {
	return catalog.EditorialCatalog{Version: 1, Artists: artists}
}

func TestDiffCatalog(t *testing.T) {
	base := func() catalog.EditorialCatalog {
		return diffCatalog(diffArtist("a", "id1"), diffArtist("b", "id2", "id3"))
	}
	rel := catalog.Relationship{Type: catalog.RelAssociatedWith, From: "a", To: "b", Status: catalog.RelationshipVerified}

	tests := []struct {
		name      string
		mutate    func(c *catalog.EditorialCatalog)
		slug      string
		kinds     []ChangeKind
		reconcile bool
		sync      bool
		empty     bool
	}{
		{name: "identical", mutate: func(*catalog.EditorialCatalog) {}, empty: true},
		{name: "ids added", slug: "a", kinds: []ChangeKind{ChangeIDsAdded}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists[0] = diffArtist("a", "id1", "id9") }},
		{name: "ids removed", slug: "b", kinds: []ChangeKind{ChangeIDsRemoved}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists[1] = diffArtist("b", "id2") }},
		{name: "ids reordered", slug: "b", kinds: []ChangeKind{ChangeIDsReordered}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists[1] = diffArtist("b", "id3", "id2") }},
		{name: "verified toggled", slug: "a", kinds: []ChangeKind{ChangeVerified}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists[0].SpotifyProfiles[0].Status = catalog.ProfileRejected }},
		{name: "enabled toggled", slug: "a", kinds: []ChangeKind{ChangeEnabled}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists[0].Enabled = false }},
		{name: "relationships only", slug: "a", kinds: []ChangeKind{ChangeRelationships},
			mutate: func(c *catalog.EditorialCatalog) { c.Relationships = []catalog.Relationship{rel} }},
		{name: "metadata only", slug: "a", kinds: []ChangeKind{ChangeMetadata},
			mutate: func(c *catalog.EditorialCatalog) { c.Artists[0].Notes = "new note" }},
		{name: "artist added with ids", slug: "c", kinds: []ChangeKind{ChangeAdded}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists = append(c.Artists, diffArtist("c", "id4")) }},
		{name: "artist added without ids", slug: "c", kinds: []ChangeKind{ChangeAdded},
			mutate: func(c *catalog.EditorialCatalog) { c.Artists = append(c.Artists, diffArtist("c")) }},
		{name: "artist removed", slug: "b", kinds: []ChangeKind{ChangeRemoved}, reconcile: true, sync: true,
			mutate: func(c *catalog.EditorialCatalog) { c.Artists = c.Artists[:1] }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := base()
			tt.mutate(&current)
			got := DiffCatalog(FingerprintCatalog(base()), FingerprintCatalog(current))
			if tt.empty {
				if len(got) != 0 {
					t.Fatalf("expected no changes, got %+v", got)
				}
				return
			}
			want := []ArtistChange{{Slug: tt.slug, Kinds: tt.kinds, NeedsReconcile: tt.reconcile, NeedsSync: tt.sync}}
			if tt.name == "relationships only" {
				want = append(want, ArtistChange{Slug: "b", Kinds: tt.kinds})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
}

func TestFingerprintCatalogIsCanonical(t *testing.T) {
	a := diffCatalog(diffArtist("a", "id1"), diffArtist("b", "id2"))
	b := diffCatalog(diffArtist("b", "id2"), diffArtist("a", "id1"))
	b.Artists[0].Genres = []string{"rock", "pop"}
	if FingerprintCatalog(a).Digest != FingerprintCatalog(b).Digest {
		t.Fatal("digest must not depend on artist or genre ordering")
	}
	r1 := catalog.Relationship{Type: catalog.RelCollaboratedWith, From: "a", To: "b"}
	r2 := catalog.Relationship{Type: catalog.RelCollaboratedWith, From: "b", To: "a"}
	a.Relationships = []catalog.Relationship{r1}
	b.Relationships = []catalog.Relationship{r2}
	if FingerprintCatalog(a).Digest != FingerprintCatalog(b).Digest {
		t.Fatal("undirected relationships must normalise")
	}
	c := diffCatalog(diffArtist("a", "id1", "id9"))
	d := diffCatalog(diffArtist("a", "id9", "id1"))
	if FingerprintCatalog(c).Digest == FingerprintCatalog(d).Digest {
		t.Fatal("spotify id order is semantic")
	}
}

func TestDiffCatalogSortedBySlug(t *testing.T) {
	applied := FingerprintCatalog(diffCatalog(diffArtist("a", "id1")))
	current := FingerprintCatalog(diffCatalog(diffArtist("a", "id1"), diffArtist("z", "id2"), diffArtist("m", "id3")))
	got := DiffCatalog(applied, current)
	if len(got) != 2 || got[0].Slug != "m" || got[1].Slug != "z" {
		t.Fatalf("unexpected %+v", got)
	}
}
