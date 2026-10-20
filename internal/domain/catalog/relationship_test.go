package catalog

import (
	"errors"
	"strings"
	"testing"
)

func relCatalog() EditorialCatalog {
	return EditorialCatalog{
		Version: 1,
		Artists: []Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", Category: CategoryCore, Roles: []Category{CategoryCore, CategoryAffiliateGroup}},
			{Slug: "rza", Name: "RZA", Category: CategoryCore, Roles: []Category{CategoryCore, CategoryProducer}},
			{Slug: "gza", Name: "GZA", Category: CategoryCore},
			{Slug: "gravediggaz", Name: "Gravediggaz", Category: CategoryAffiliateGroup},
			{Slug: "prince-paul", Name: "Prince Paul", Category: CategoryProducer},
		},
	}
}

func wikiEvidence() []Evidence {
	return []Evidence{{Type: EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Wu-Tang_Clan", Rationale: "Lists the members."}}
}

func verified(t RelationshipType, from, to string) Relationship {
	return Relationship{Type: t, From: from, To: to, Status: RelationshipVerified, Confidence: ConfidenceHigh, Reviewer: "tester", Evidence: wikiEvidence()}
}

func issueMessages(issues []ValidationIssue) string {
	parts := make([]string, 0, len(issues))
	for _, i := range issues {
		parts = append(parts, i.Error())
	}
	return strings.Join(parts, "\n")
}

func TestValidateRelationshipsAcceptsLegacyAndValidEdges(t *testing.T) {
	c := relCatalog()
	if got := ValidateRelationships(c); len(got) != 0 {
		t.Fatalf("legacy catalog must have no relationship issues: %v", got)
	}
	c.Relationships = []Relationship{
		verified(RelMemberOf, "rza", "wu-tang-clan"),
		verified(RelProducerFor, "rza", "gza"),
		verified(RelCollaboratedWith, "gza", "rza"),
	}
	c.Relationships[2].From, c.Relationships[2].To = "gza", "rza"
	if got := ValidateRelationships(c); len(got) != 0 {
		t.Fatalf("unexpected issues: %s", issueMessages(got))
	}
}

func TestValidateRelationshipsRejectsBadGraphs(t *testing.T) {
	cases := map[string]struct {
		rels []Relationship
		want string
	}{
		"orphan from":       {[]Relationship{verified(RelMemberOf, "nobody", "wu-tang-clan")}, `unknown artist "nobody"`},
		"orphan to":         {[]Relationship{verified(RelMemberOf, "rza", "nobody")}, `unknown artist "nobody"`},
		"self edge":         {[]Relationship{verified(RelAssociatedWith, "rza", "rza")}, "self relationship"},
		"member of person":  {[]Relationship{verified(RelMemberOf, "rza", "gza")}, "requires a group target"},
		"producer required": {[]Relationship{verified(RelProducerFor, "gza", "rza")}, "requires a producer"},
		"unknown type":      {[]Relationship{{Type: "boss_of", From: "rza", To: "gza", Status: RelationshipProposed}}, "type: is unknown"},
		"duplicate": {[]Relationship{
			verified(RelMemberOf, "rza", "wu-tang-clan"), verified(RelMemberOf, "rza", "wu-tang-clan"),
		}, "duplicate member_of"},
		"undirected duplicate": {[]Relationship{
			verified(RelCollaboratedWith, "gza", "rza"), verified(RelCollaboratedWith, "gza", "rza"),
		}, "duplicate collaborated_with"},
		"membership vs collaboration": {[]Relationship{
			verified(RelMemberOf, "rza", "wu-tang-clan"), verified(RelAssociatedWith, "rza", "wu-tang-clan"),
		}, "separate claims"},
		"cycle": {[]Relationship{
			verified(RelMemberOf, "wu-tang-clan", "gravediggaz"), verified(RelMemberOf, "gravediggaz", "wu-tang-clan"),
		}, "membership cycle"},
		"credit is not evidence": {[]Relationship{func() Relationship {
			r := verified(RelMemberOf, "rza", "wu-tang-clan")
			r.Evidence = []Evidence{{Type: EvidenceCredit, Rationale: "Credited on a track."}}
			return r
		}()}, "independent evidence"},
		"bad dates": {[]Relationship{func() Relationship {
			r := verified(RelMemberOf, "rza", "wu-tang-clan")
			r.Start, r.End = "1993-13", "1990"
			return r
		}()}, "must use YYYY"},
		"end before start": {[]Relationship{func() Relationship {
			r := verified(RelMemberOf, "rza", "wu-tang-clan")
			r.Start, r.End = "1995", "1990"
			return r
		}()}, "before start"},
		"verified needs reviewer": {[]Relationship{func() Relationship {
			r := verified(RelMemberOf, "rza", "wu-tang-clan")
			r.Reviewer = ""
			return r
		}()}, "reviewer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := relCatalog()
			c.Relationships = tc.rels
			got := issueMessages(ValidateRelationships(c))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("want issue containing %q, got:\n%s", tc.want, got)
			}
		})
	}
}

func TestUndirectedKeyIsOrderIndependent(t *testing.T) {
	if NewRelationshipKey(RelCollaboratedWith, "b", "a") != NewRelationshipKey(RelCollaboratedWith, "a", "b") {
		t.Fatal("undirected pair must normalise")
	}
	if NewRelationshipKey(RelMemberOf, "b", "a") == NewRelationshipKey(RelMemberOf, "a", "b") {
		t.Fatal("directed edges keep direction")
	}
}

func TestRenameArtistSlugRewritesReferencesAndBlocksCollisions(t *testing.T) {
	c := relCatalog()
	c.Relationships = []Relationship{verified(RelMemberOf, "rza", "wu-tang-clan"), verified(RelCollaboratedWith, "gza", "rza")}
	renamed, err := c.RenameArtistSlug("rza", "the-rza")
	if err != nil {
		t.Fatal(err)
	}
	if got := ValidateRelationships(renamed); len(got) != 0 {
		t.Fatalf("renamed catalog invalid: %s", issueMessages(got))
	}
	if c.Artists[1].Slug != "rza" || c.Relationships[0].From != "rza" {
		t.Fatal("original catalog must be untouched")
	}
	if renamed.Relationships[0].From != "the-rza" {
		t.Fatalf("directed reference not rewritten: %+v", renamed.Relationships[0])
	}
	if k := renamed.Relationships[1]; k.From != "gza" || k.To != "the-rza" {
		t.Fatalf("undirected pair must be re-ordered: %+v", k)
	}
	if _, err := c.RenameArtistSlug("rza", "gza"); err == nil {
		t.Fatal("collision must be blocked")
	}
	if _, err := c.RenameArtistSlug("rza", "Bad Slug"); err == nil {
		t.Fatal("invalid slug must be blocked")
	}
	if _, err := c.RenameArtistSlug("nobody", "x"); !errors.Is(err, ErrUnknownArtist) {
		t.Fatalf("want ErrUnknownArtist, got %v", err)
	}
}

func TestRemoveArtistBlockedWhileReferenced(t *testing.T) {
	c := relCatalog()
	c.Relationships = []Relationship{verified(RelMemberOf, "rza", "wu-tang-clan")}
	_, err := c.RemoveArtist("rza")
	if !errors.Is(err, ErrArtistReferenced) || !strings.Contains(err.Error(), "member_of rza -> wu-tang-clan") {
		t.Fatalf("want actionable referenced error, got %v", err)
	}
	next, err := c.RemoveArtist("prince-paul")
	if err != nil || next.FindArtist("prince-paul") >= 0 || c.FindArtist("prince-paul") < 0 {
		t.Fatalf("unreferenced artist must be removable without touching the original: %v", err)
	}
}

func TestFormerMembershipIsATimeBoundedMemberOf(t *testing.T) {
	r := verified(RelMemberOf, "rza", "wu-tang-clan")
	if r.Former() {
		t.Fatal("no end date means current/unknown")
	}
	r.End = "2000"
	if !r.Former() {
		t.Fatal("end date makes it former")
	}
}
