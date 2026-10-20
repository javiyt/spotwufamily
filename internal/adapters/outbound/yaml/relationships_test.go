package yamlcatalog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

const legacyCatalog = `# Editorial artist catalog.
# Spotify IDs are intentionally reviewed before sync.
# Entries remain disabled so validation can reject accidental syncs without a Spotify ID.
# ` + "`roles`" + ` preserves cases where an entry belongs to more than one editorial category.

version: 1
artists:
  - slug: rza
    name: RZA
    spotify_id: ""
    category: core
    aliases: []
    enabled: false
    notes: ""
`

func TestLegacyYAMLWithoutRelationshipsRoundTripsByteForByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	require.NoError(t, os.WriteFile(path, []byte(legacyCatalog), 0o644))
	store := yamlcatalog.NewStore()

	loaded, err := store.Load(context.Background(), path)
	require.NoError(t, err)
	require.Empty(t, loaded.Relationships)
	require.NoError(t, store.Save(context.Background(), path, loaded))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, legacyCatalog, string(after))
	_, err = os.Stat(path + ".bak")
	require.True(t, os.IsNotExist(err), "an unchanged catalog must not rewrite the backup")
}

func TestRepositoryCatalogLoadsAndValidates(t *testing.T) {
	loaded, err := yamlcatalog.NewStore().Load(context.Background(), filepath.Join("..", "..", "..", "..", "data", "artists.yaml"))
	require.NoError(t, err)
	require.Empty(t, catalog.ValidateEditorialCatalog(loaded))
}

func TestRelationshipsGoldenRoundTripAndDeterministicOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	artist := func(slug string, roles ...catalog.Category) catalog.Artist {
		return catalog.Artist{Slug: slug, Name: slug, Category: roles[0], Roles: roles, Aliases: []string{}, SpotifyIDs: []string{}, Genres: []string{}}
	}
	evidence := []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Wu-Tang_Clan", Rationale: "Lists the members."}}
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			artist("wu-tang-clan", catalog.CategoryCore, catalog.CategoryAffiliateGroup),
			artist("rza", catalog.CategoryCore),
			artist("gza", catalog.CategoryCore),
		},
		Relationships: []catalog.Relationship{
			{Type: catalog.RelMemberOf, From: "rza", To: "wu-tang-clan", Status: catalog.RelationshipVerified, Confidence: catalog.ConfidenceHigh, Reviewer: "tester", ReviewedAt: "2026-10-20T00:00:00Z", Evidence: evidence, Era: "founding member"},
			{Type: catalog.RelCollaboratedWith, From: "rza", To: "gza", Status: catalog.RelationshipProposed, Commentary: "pair given out of order"},
		},
	}

	require.NoError(t, store.Save(context.Background(), path, c))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(first), "relationships:")

	loaded, err := store.Load(context.Background(), path)
	require.NoError(t, err)
	require.Len(t, loaded.Relationships, 2)
	// collaborated_with sorts first and its undirected pair is stored ordered.
	require.Equal(t, catalog.RelCollaboratedWith, loaded.Relationships[0].Type)
	require.Equal(t, "gza", loaded.Relationships[0].From)
	require.Equal(t, "rza", loaded.Relationships[0].To)
	require.Empty(t, catalog.ValidateEditorialCatalog(loaded))

	// Reversing the input order produces the identical file.
	c.Relationships[0], c.Relationships[1] = c.Relationships[1], c.Relationships[0]
	other := filepath.Join(t.TempDir(), "artists.yaml")
	require.NoError(t, store.Save(context.Background(), other, c))
	second, err := os.ReadFile(other)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
}

func TestSeededRelationshipsEncodeCappadonnaNuanceWithoutInventingMembers(t *testing.T) {
	loaded, err := yamlcatalog.NewStore().Load(context.Background(), filepath.Join("..", "..", "..", "..", "data", "artists.yaml"))
	require.NoError(t, err)

	members := map[string]catalog.Relationship{}
	for _, rel := range loaded.Relationships {
		if rel.Type == catalog.RelMemberOf && rel.To == "wu-tang-clan" {
			members[rel.From] = rel
		}
	}
	require.Len(t, members, 10, "exactly the ten documented members, nothing inferred from credits")
	require.Equal(t, "1992", members["rza"].Start)
	require.Equal(t, "2004", members["ol-dirty-bastard"].End)
	require.Contains(t, members["cappadonna"].Commentary, "not an official member")
	require.Equal(t, catalog.CandidateConfidence("medium"), members["cappadonna"].Confidence)
	for _, rel := range loaded.Relationships {
		require.True(t, rel.Established(), "seeded edges are reviewed: %s", rel.Key())
		for _, evidence := range rel.Evidence {
			require.NotEqual(t, catalog.EvidenceCredit, evidence.Type)
		}
	}
	// Affiliate groups are associated with the group, never members of it.
	for _, rel := range loaded.Relationships {
		if rel.Type == catalog.RelAssociatedWith {
			require.Contains(t, []string{rel.From, rel.To}, "wu-tang-clan")
		}
	}
}
