package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestSaveArtistRelationshipsProjectsOnlyVerifiedEdges(t *testing.T) {
	ctx := context.Background()
	database := openMigratedDatabase(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	defer func() { require.NoError(t, database.Close()) }()

	artists := []catalog.Artist{
		{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", Category: catalog.CategoryCore},
		{Slug: "rza", Name: "RZA", Category: catalog.CategoryCore},
		{Slug: "gza", Name: "GZA", Category: catalog.CategoryCore},
	}
	require.NoError(t, database.SaveConfiguredArtists(ctx, artists))

	evidence := []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Wu-Tang_Clan", Rationale: "Lists the members."}}
	rels := []catalog.Relationship{
		{Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipVerified, Confidence: catalog.ConfidenceHigh, Reviewer: "tester", Evidence: evidence},
		{Type: catalog.RelMemberOf, From: "gza", To: "wu-tang-clan", Status: catalog.RelationshipProposed, Evidence: evidence},
		{Type: catalog.RelAssociatedWith, From: "rza", To: "gza", Status: catalog.RelationshipRejected, Commentary: "no"},
	}
	require.NoError(t, database.SaveArtistRelationships(ctx, rels))
	// Saving is a full replacement, so it is idempotent.
	require.NoError(t, database.SaveArtistRelationships(ctx, rels))

	exported, err := database.LoadExportCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, exported.Relationships, 1, "proposed and rejected claims must not be projected")
	require.Equal(t, "rza", exported.Relationships[0].From)
	require.Len(t, exported.Relationships[0].Evidence, 1)

	require.NoError(t, database.SaveArtistRelationships(ctx, nil))
	exported, err = database.LoadExportCatalog(ctx)
	require.NoError(t, err)
	require.Empty(t, exported.Relationships)
}

func TestSaveArtistRelationshipsRollsBackOnUnknownArtist(t *testing.T) {
	ctx := context.Background()
	database := openMigratedDatabase(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	defer func() { require.NoError(t, database.Close()) }()
	require.NoError(t, database.SaveConfiguredArtists(ctx, []catalog.Artist{
		{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", Category: catalog.CategoryCore},
		{Slug: "rza", Name: "RZA", Category: catalog.CategoryCore},
	}))
	good := catalog.Relationship{Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipVerified}
	require.NoError(t, database.SaveArtistRelationships(ctx, []catalog.Relationship{good}))

	bad := catalog.Relationship{Type: catalog.RelMemberOf, From: "ghost", To: "wu-tang-clan", Status: catalog.RelationshipVerified}
	require.Error(t, database.SaveArtistRelationships(ctx, []catalog.Relationship{bad}))

	exported, err := database.LoadExportCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, exported.Relationships, 1, "a failed replacement must keep the previous projection")
}
