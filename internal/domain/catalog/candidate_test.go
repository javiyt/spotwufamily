package catalog_test

import (
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestCandidateKeyIgnoresCaseAccentsAndApostrophes(t *testing.T) {
	require.Equal(t, "mc-rene", catalog.CandidateKey("MC René"))
	require.Equal(t, catalog.CandidateKey("Ol' Dirty"), catalog.CandidateKey("Ol’ Dirty"))
	require.Equal(t, "bjork", catalog.CandidateKey("Björk"))
	require.Empty(t, catalog.CandidateKey("   ...  "))
}

func TestCandidateKeyKeepsSeparatorsButLooseKeyDoesNot(t *testing.T) {
	require.NotEqual(t, catalog.CandidateKey("N-Tyce"), catalog.CandidateKey("NTyce"))
	require.Equal(t, catalog.CandidateLooseKey("N-Tyce"), catalog.CandidateLooseKey("N Tyce"))
}

func evidencePair() []catalog.Evidence {
	return []catalog.Evidence{
		{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/6c20758e-b910-4ea0-84da-b20ecc7c7d1e", Rationale: "member of Gravediggaz"},
		{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Gravediggaz", Rationale: "lists the member"},
	}
}

func acceptedCandidate() catalog.Candidate {
	return catalog.Candidate{
		Name: "Frukwan", Kind: catalog.CandidateKindArtist, Status: catalog.CandidateAccepted,
		Category: catalog.CategoryAffiliateArtist, Relation: catalog.RelationMember, RelationClaim: "group-gravediggaz",
		RelationGroups: []string{"gravediggaz"}, Evidence: evidencePair(),
		Reviewer: "javi", ReviewedAt: "2026-10-20T10:00:00Z",
	}
}

func validationCatalog() catalog.EditorialCatalog {
	return catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{{Slug: "gravediggaz", Name: "Gravediggaz", Category: catalog.CategoryAffiliateGroup}}}
}

func TestValidateCandidateLedgerAcceptsCorroboratedCandidate(t *testing.T) {
	ledger := catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{acceptedCandidate()}}

	require.Empty(t, catalog.ValidateCandidateLedger(ledger, validationCatalog()))
}

func TestValidateCandidateLedgerRejectsWeakAcceptance(t *testing.T) {
	cases := map[string]func(*catalog.Candidate){
		"one source type only": func(c *catalog.Candidate) { c.Evidence = c.Evidence[:1] },
		"same type twice": func(c *catalog.Candidate) {
			c.Evidence[1].Type = catalog.EvidenceMusicBrainz
			c.Evidence[1].URL = "https://musicbrainz.org/artist/x"
		},
		"name match is not proof": func(c *catalog.Candidate) {
			c.Evidence[1] = catalog.Evidence{Type: catalog.EvidenceNameMatch, Rationale: "same name"}
		},
		"missing relation":       func(c *catalog.Candidate) { c.Relation = "" },
		"missing reviewer":       func(c *catalog.Candidate) { c.Reviewer = "" },
		"unknown relation group": func(c *catalog.Candidate) { c.RelationGroups = []string{"nope"} },
		"missing claim":          func(c *catalog.Candidate) { c.RelationClaim = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := acceptedCandidate()
			mutate(&candidate)
			ledger := catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{candidate}}

			require.NotEmpty(t, catalog.ValidateCandidateLedger(ledger, validationCatalog()))
		})
	}
}

func TestValidateCandidateLedgerRejectsDuplicateKeysAndBadAliasTarget(t *testing.T) {
	ledger := catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{
		{Name: "Ol' Dirty", Kind: catalog.CandidateKindArtist, Status: catalog.CandidatePending},
		{Name: "Ol’ Dirty", Kind: catalog.CandidateKindArtist, Status: catalog.CandidatePending},
		{Name: "Ancient Coins", Kind: catalog.CandidateKindGroup, Status: catalog.CandidateAliasOfExisting, AliasOf: "missing"},
	}}

	issues := catalog.ValidateCandidateLedger(ledger, validationCatalog())

	require.Len(t, issues, 2)
}

func TestReviewCandidateRequiresManualStatusAndReviewer(t *testing.T) {
	_, err := catalog.ReviewCandidate(catalog.Candidate{Name: "X", Status: catalog.CandidatePending}, catalog.CandidateReview{Status: catalog.CandidatePending, Reviewer: "javi"})
	require.Error(t, err)
	_, err = catalog.ReviewCandidate(catalog.Candidate{Name: "X", Status: catalog.CandidatePending}, catalog.CandidateReview{Status: catalog.CandidateRejected})
	require.Error(t, err)
}

func TestReviewCandidateKeepsRejectedUntilNewIndependentEvidence(t *testing.T) {
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	rejected := catalog.Candidate{Name: "X", Status: catalog.CandidateRejected, Evidence: []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/X", Rationale: "r"}}}

	_, err := catalog.ReviewCandidate(rejected, catalog.CandidateReview{Status: catalog.CandidateNeedsMoreEvidence, Reviewer: "javi", Reason: "again", At: at})
	require.ErrorIs(t, err, catalog.ErrCandidateNeedsNewProof)

	// Re-submitting the evidence already attached to the rejection is not new.
	_, err = catalog.ReviewCandidate(rejected, catalog.CandidateReview{Status: catalog.CandidateNeedsMoreEvidence, Reviewer: "javi", Reason: "again", At: at, Evidence: rejected.Evidence})
	require.ErrorIs(t, err, catalog.ErrCandidateNeedsNewProof)

	fresh := catalog.Evidence{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/abc", Rationale: "new"}
	next, err := catalog.ReviewCandidate(rejected, catalog.CandidateReview{Status: catalog.CandidateNeedsMoreEvidence, Reviewer: "javi", Reason: "new proof", At: at, Evidence: []catalog.Evidence{fresh}})
	require.NoError(t, err)
	require.Equal(t, catalog.CandidateNeedsMoreEvidence, next.Status)
	require.Len(t, next.Evidence, 2)
	require.Equal(t, "2026-10-20T10:00:00Z", next.ReviewedAt)
}

func TestReviewCandidateRefusesStagedCandidate(t *testing.T) {
	staged := acceptedCandidate()
	staged.StagedSlug = "frukwan"

	_, err := catalog.ReviewCandidate(staged, catalog.CandidateReview{Status: catalog.CandidateRejected, Reviewer: "javi", Reason: "oops", At: time.Now()})

	require.ErrorIs(t, err, catalog.ErrCandidateStaged)
}
