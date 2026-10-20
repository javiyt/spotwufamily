package yamlcatalog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestLedgerStoreMissingFileIsEmptyLedger(t *testing.T) {
	ledger, err := yamlcatalog.NewLedgerStore().LoadLedger(context.Background(), filepath.Join(t.TempDir(), "none.yaml"))

	require.NoError(t, err)
	require.Equal(t, 1, ledger.Version)
	require.Empty(t, ledger.Candidates)
}

func TestLedgerStoreRoundTripIsDeterministicAndKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wu-candidates.yaml")
	store := yamlcatalog.NewLedgerStore()
	ledger := catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{{
		Name: "Frukwan", Kind: catalog.CandidateKindArtist, Status: catalog.CandidateAccepted, Confidence: catalog.ConfidenceHigh,
		Relation: catalog.RelationMember, RelationGroups: []string{"gravediggaz"}, Category: catalog.CategoryAffiliateArtist,
		ExternalIDs: map[string]string{"musicbrainz": "6c20758e", "a": "b"},
		Sources:     []catalog.CandidateSource{{Title: "ref", URL: "https://example.test"}},
		Evidence:    []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Gravediggaz", Rationale: "lists member"}},
		Reviewer:    "javi", ReviewedAt: "2026-10-20T10:00:00Z", StagedSlug: "frukwan",
	}}}

	require.NoError(t, store.SaveLedger(context.Background(), path, ledger))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	loaded, err := store.LoadLedger(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, ledger, loaded)

	require.NoError(t, store.SaveLedger(context.Background(), path, loaded))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, first, second)
	_, err = os.Stat(path + ".bak")
	require.True(t, os.IsNotExist(err), "unchanged content must not rewrite or back up")

	loaded.Candidates[0].Reason = "changed"
	require.NoError(t, store.SaveLedger(context.Background(), path, loaded))
	backup, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	require.Equal(t, first, backup)
}

func TestLedgerStoreRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\ncandidates:\n  - name: X\n    spotify_id: 34EP7KEpOjXcM2TCat1ISk\n"), 0o644))

	_, err := yamlcatalog.NewLedgerStore().LoadLedger(context.Background(), path)

	require.Error(t, err, "a ledger must not carry Spotify IDs")
}
