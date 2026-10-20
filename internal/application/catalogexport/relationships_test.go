package catalogexport_test

import (
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestRelationshipsFromCatalogPublishesOnlyVerifiedEdges(t *testing.T) {
	evidence := []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Wu-Tang_Clan", Rationale: "Lists the members."}}
	c := catalog.EditorialCatalog{
		Artists: []catalog.Artist{{Slug: "wu-tang-clan"}, {Slug: "rza"}, {Slug: "gza"}},
		Relationships: []catalog.Relationship{
			{Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipVerified, Confidence: catalog.ConfidenceHigh, Evidence: evidence},
			{Type: catalog.RelMemberOf, From: "gza", To: "wu-tang-clan", Status: catalog.RelationshipProposed, Evidence: evidence},
			{Type: catalog.RelAssociatedWith, From: "rza", To: "gza", Status: catalog.RelationshipRejected, Commentary: "no"},
			{Type: catalog.RelMemberOf, From: "ghost", To: "wu-tang-clan", Status: catalog.RelationshipVerified},
			{Type: catalog.RelCollaboratedWith, From: "rza", To: "gza", Status: catalog.RelationshipVerified, Commentary: "Pair stored out of order."},
		},
	}
	got := catalogexport.RelationshipsFromCatalog(c)
	require.Len(t, got, 2, "proposed, rejected and dangling edges must not be published")
	require.Equal(t, "collaborated_with", got[0].Type)
	require.Equal(t, "gza", got[0].From, "undirected pair is normalised")
	require.Equal(t, "member_of", got[1].Type)
	require.Equal(t, "rza", got[1].From)
	require.Len(t, got[1].Evidence, 1)
}
