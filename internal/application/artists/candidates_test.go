package artists_test

import (
	"context"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type memoryLedger struct {
	ledger    catalog.CandidateLedger
	saveCount int
}

func (m *memoryLedger) LoadLedger(context.Context, string) (catalog.CandidateLedger, error) {
	if m.ledger.Version == 0 {
		return catalog.CandidateLedger{Version: 1}, nil
	}
	return m.ledger, nil
}

func (m *memoryLedger) SaveLedger(_ context.Context, _ string, l catalog.CandidateLedger) error {
	m.ledger = l
	m.saveCount++
	return nil
}

type fixedInventory struct{ inputs []artists.CandidateInput }

func (f fixedInventory) ReadCandidates(context.Context, string) ([]artists.CandidateInput, error) {
	return f.inputs, nil
}

func candidateCatalog() *memoryStore {
	return &memoryStore{catalog: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "gravediggaz", Name: "Gravediggaz", Category: catalog.CategoryAffiliateGroup, Roles: []catalog.Category{catalog.CategoryAffiliateGroup}, Aliases: []string{}, EditorialOrder: 1},
		{Slug: "ancients-coins", Name: "Ancients Coins", Category: catalog.CategoryAffiliateGroup, Roles: []catalog.Category{catalog.CategoryAffiliateGroup}, Aliases: []string{}, EditorialOrder: 2},
		{Slug: "ghostface-killah", Name: "Ghostface Killah", SpotifyID: "6AZv3m27uyRxi8KyJSfUxL", Category: catalog.CategoryCore, Roles: []catalog.Category{catalog.CategoryCore}, Aliases: []string{"Ghostface"}, EditorialOrder: 3},
		{Slug: "ntyce", Name: "NTyce", Category: catalog.CategoryAffiliateArtist, Roles: []catalog.Category{catalog.CategoryAffiliateArtist}, Aliases: []string{}, EditorialOrder: 4},
	}}}
}

func importer(inputs ...artists.CandidateInput) (artists.ImportCandidates, *memoryStore, *memoryLedger) {
	store := candidateCatalog()
	ledgers := &memoryLedger{}
	return artists.NewImportCandidates(fixedInventory{inputs: inputs}, ledgers, store), store, ledgers
}

func TestImportCandidatesDryRunWritesNothing(t *testing.T) {
	uc, store, ledgers := importer(artists.CandidateInput{Name: "Frukwan", Kind: "artista", SourceTitle: "ref", Confidence: "alta"})

	report, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l"})

	require.NoError(t, err)
	require.False(t, report.Applied)
	require.Equal(t, 1, report.After.Pending)
	require.Zero(t, ledgers.saveCount)
	require.Zero(t, store.saveCount)
}

func TestImportCandidatesAddsPendingToLedgerNeverToCatalog(t *testing.T) {
	uc, store, ledgers := importer(
		artists.CandidateInput{Name: "Frukwan", Kind: "artista", RelationClaim: "group-gravediggaz", SourceTitle: "ref", SourceURL: "https://example.test/ref", Confidence: "alta"},
		artists.CandidateInput{Name: "Black Brigade", Kind: "grupo", SourceTitle: "ref", Confidence: "baja"},
	)

	report, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.NoError(t, err)
	require.True(t, report.Applied)
	require.Equal(t, 2, report.After.Pending)
	require.Len(t, ledgers.ledger.Candidates, 2)
	require.Len(t, store.catalog.Artists, 4, "pending candidates must not reach the catalog")
	black := ledgers.ledger.Candidates[0]
	require.Equal(t, "Black Brigade", black.Name)
	require.Equal(t, catalog.CandidateKindGroup, black.Kind)
	require.Equal(t, catalog.CategoryAffiliateGroup, black.Category)
	require.Equal(t, catalog.ConfidenceLow, black.Confidence)
}

func TestImportCandidatesIsIdempotent(t *testing.T) {
	inputs := []artists.CandidateInput{
		{Name: "Frukwan", Kind: "artista", SourceTitle: "ref", SourceURL: "https://example.test/ref"},
		{Name: "Black Brigade", Kind: "grupo", SourceTitle: "ref"},
	}
	uc, _, ledgers := importer(inputs...)
	opts := artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l", Apply: true}
	_, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	first := append([]catalog.Candidate(nil), ledgers.ledger.Candidates...)

	report, err := uc.Run(context.Background(), opts)

	require.NoError(t, err)
	require.Equal(t, first, ledgers.ledger.Candidates)
	for _, change := range report.Changes {
		require.Equal(t, artists.ActionKnown, change.Action)
	}
}

func TestImportCandidatesDetectsDuplicatesByNameAliasAndSpotifyID(t *testing.T) {
	uc, _, ledgers := importer(
		artists.CandidateInput{Name: "ghostface killah", Kind: "artista", SourceTitle: "ref"},
		artists.CandidateInput{Name: "Ghostface", Kind: "artista", SourceTitle: "ref"},
		artists.CandidateInput{Name: "Tony Starks", Kind: "artista", SourceTitle: "ref", ExternalIDs: map[string]string{"spotify": "6AZv3m27uyRxi8KyJSfUxL"}},
		artists.CandidateInput{Name: "Gravediggaz", Kind: "grupo", SourceTitle: "ref"},
		artists.CandidateInput{Name: "Brand New", Kind: "artista", Aliases: []string{"Ancients Coins"}, SourceTitle: "ref"},
	)

	report, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.NoError(t, err)
	require.Equal(t, 5, report.After.AliasOfExisting)
	for _, candidate := range ledgers.ledger.Candidates {
		require.NotEmpty(t, candidate.AliasOf, candidate.Name)
	}
}

func TestImportCandidatesFlagsLooseVariantWithoutMerging(t *testing.T) {
	uc, _, ledgers := importer(artists.CandidateInput{Name: "N-Tyce", Kind: "artista", SourceTitle: "ref"})

	_, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.NoError(t, err)
	require.Len(t, ledgers.ledger.Candidates, 1)
	require.Equal(t, catalog.CandidatePending, ledgers.ledger.Candidates[0].Status)
	require.Equal(t, "ntyce", ledgers.ledger.Candidates[0].PossibleDuplicateOf)
}

func TestImportCandidatesNormalizesDiacriticsApostrophesAndInputDuplicates(t *testing.T) {
	uc, _, ledgers := importer(
		artists.CandidateInput{Name: "Mr. Müller’s Crew", Kind: "grupo", SourceTitle: "a", Aliases: []string{"Muller's Crew"}},
		artists.CandidateInput{Name: "MR. MULLER'S CREW", Kind: "grupo", SourceTitle: "b"},
		artists.CandidateInput{Name: " ... ", Kind: "grupo", SourceTitle: "a"},
	)

	report, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.NoError(t, err)
	require.Len(t, ledgers.ledger.Candidates, 1)
	require.Len(t, ledgers.ledger.Candidates[0].Sources, 2, "both sources are kept as provenance")
	require.Len(t, report.Invalid, 1)
}

func TestImportCandidatesPreservesManualDecisionsAcrossRuns(t *testing.T) {
	uc, store, ledgers := importer(
		artists.CandidateInput{Name: "Black Brigade", Kind: "grupo", SourceTitle: "ref", Confidence: "alta", RelationClaim: "new claim"},
	)
	ledgers.ledger = catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{{
		Name: "Black Brigade", Kind: catalog.CandidateKindGroup, Status: catalog.CandidateRejected, Confidence: catalog.ConfidenceLow,
		Reviewer: "javi", ReviewedAt: "2026-10-20T10:00:00Z", Reason: "homonym of an unrelated crew", Category: catalog.CategoryAffiliateGroup,
	}}}

	report, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{InputPath: "x", CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.NoError(t, err)
	require.Equal(t, artists.ActionSuppressed, report.Changes[0].Action)
	got := ledgers.ledger.Candidates[0]
	require.Equal(t, catalog.CandidateRejected, got.Status)
	require.Equal(t, catalog.ConfidenceLow, got.Confidence)
	require.Equal(t, "homonym of an unrelated crew", got.Reason)
	require.Len(t, store.catalog.Artists, 4)
}

func acceptedFrukwan() catalog.Candidate {
	return catalog.Candidate{
		Name: "Frukwan", Kind: catalog.CandidateKindArtist, Status: catalog.CandidateAccepted,
		Category: catalog.CategoryAffiliateArtist, Roles: []catalog.Category{catalog.CategoryAffiliateArtist},
		Relation: catalog.RelationMember, RelationClaim: "group-gravediggaz", RelationGroups: []string{"gravediggaz"},
		Evidence: []catalog.Evidence{
			{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/6c20758e-b910-4ea0-84da-b20ecc7c7d1e", Rationale: "member of Gravediggaz"},
			{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Gravediggaz", Rationale: "lists the member"},
		},
		Reviewer: "javi", ReviewedAt: "2026-10-20T10:00:00Z",
	}
}

func TestStagingAddsAcceptedCandidateDisabledWithoutSpotifyAndIsReversible(t *testing.T) {
	uc, store, ledgers := importer()
	ledgers.ledger = catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{acceptedFrukwan()}}
	opts := artists.ImportCandidatesOptions{CatalogPath: "c", LedgerPath: "l", Apply: true}

	report, err := uc.Run(context.Background(), opts)

	require.NoError(t, err)
	require.Equal(t, artists.ActionStaged, report.Changes[0].Action)
	require.Len(t, store.catalog.Artists, 5)
	staged := store.catalog.Artists[4]
	require.Equal(t, "frukwan", staged.Slug)
	require.False(t, staged.Enabled)
	require.Empty(t, staged.AllSpotifyIDs())
	require.Equal(t, 5, staged.EditorialOrder)
	require.Equal(t, "2026-10-20", staged.AddedAt)
	require.Equal(t, "https://musicbrainz.org/artist/6c20758e-b910-4ea0-84da-b20ecc7c7d1e", staged.ExternalURL)
	require.Equal(t, "frukwan", ledgers.ledger.Candidates[0].StagedSlug)

	// Staging twice changes nothing.
	again, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Empty(t, again.Changes)
	require.Len(t, store.catalog.Artists, 5)

	opts.Revert = true
	reverted, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, artists.ActionReverted, reverted.Changes[0].Action)
	require.Len(t, store.catalog.Artists, 4)
	require.Empty(t, ledgers.ledger.Candidates[0].StagedSlug)
	require.Equal(t, catalog.CandidateAccepted, ledgers.ledger.Candidates[0].Status)
}

func TestRevertKeepsStagedEntryThatWasEditedAfterwards(t *testing.T) {
	uc, store, ledgers := importer()
	ledgers.ledger = catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{acceptedFrukwan()}}
	opts := artists.ImportCandidatesOptions{CatalogPath: "c", LedgerPath: "l", Apply: true}
	_, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	store.catalog.Artists[4].SpotifyID = "7CrVv4zLga71gdlMOMfjXL"

	opts.Revert = true
	report, err := uc.Run(context.Background(), opts)

	require.NoError(t, err)
	require.Equal(t, artists.ActionRevertKept, report.Changes[0].Action)
	require.Len(t, store.catalog.Artists, 5)
	require.Equal(t, "frukwan", ledgers.ledger.Candidates[0].StagedSlug)
}

func TestStagingNeverStagesWeakAcceptance(t *testing.T) {
	uc, store, ledgers := importer()
	weak := acceptedFrukwan()
	weak.Evidence = weak.Evidence[:1]
	ledgers.ledger = catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{weak}}

	_, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.Error(t, err)
	require.Len(t, store.catalog.Artists, 4)
	require.Zero(t, ledgers.saveCount)
}

func TestAliasDecisionAddsAliasToExistingEntryOnceAndReverts(t *testing.T) {
	uc, store, ledgers := importer()
	ledgers.ledger = catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{{
		Name: "Ancient Coins", Kind: catalog.CandidateKindGroup, Status: catalog.CandidateAliasOfExisting, AliasOf: "ancients-coins",
		Reviewer: "javi", ReviewedAt: "2026-10-20T10:00:00Z", Reason: "MusicBrainz and Wikipedia spell it Ancient Coins",
	}}}
	opts := artists.ImportCandidatesOptions{CatalogPath: "c", LedgerPath: "l", Apply: true}

	_, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, []string{"Ancient Coins"}, store.catalog.Artists[1].Aliases)
	_, err = uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, []string{"Ancient Coins"}, store.catalog.Artists[1].Aliases)

	opts.Revert = true
	_, err = uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Empty(t, store.catalog.Artists[1].Aliases)
	require.False(t, ledgers.ledger.Candidates[0].AliasApplied)
}

func TestReviewCandidateCreatesAndDecides(t *testing.T) {
	store := candidateCatalog()
	ledgers := &memoryLedger{}
	uc := artists.NewReviewCandidate(ledgers, store)

	_, err := uc.Run(context.Background(), artists.ReviewCandidateOptions{Name: "Ancient Coins", Review: catalog.CandidateReview{Status: catalog.CandidateAliasOfExisting, Reviewer: "javi", AliasOf: "ancients-coins", At: time.Now()}})
	require.ErrorContains(t, err, "--kind")

	got, err := uc.Run(context.Background(), artists.ReviewCandidateOptions{Name: "Ancient Coins", Kind: catalog.CandidateKindGroup, Review: catalog.CandidateReview{Status: catalog.CandidateAliasOfExisting, Reviewer: "javi", AliasOf: "ancients-coins", At: time.Now()}})

	require.NoError(t, err)
	require.Equal(t, catalog.CandidateAliasOfExisting, got.Status)
	require.Len(t, ledgers.ledger.Candidates, 1)

	_, err = uc.Run(context.Background(), artists.ReviewCandidateOptions{Name: "Ancient Coins", DryRun: true, Review: catalog.CandidateReview{Status: catalog.CandidateAliasOfExisting, Reviewer: "javi", AliasOf: "nope", At: time.Now()}})
	require.ErrorContains(t, err, "unknown catalog slug")
}

func TestFormatImportCandidatesMarkdownIsDeterministic(t *testing.T) {
	uc, _, ledgers := importer()
	ledgers.ledger = catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{acceptedFrukwan()}}
	report, err := uc.Run(context.Background(), artists.ImportCandidatesOptions{CatalogPath: "c", LedgerPath: "l"})
	require.NoError(t, err)

	first := artists.FormatImportCandidatesMarkdown(report)

	require.Equal(t, first, artists.FormatImportCandidatesMarkdown(report))
	require.Contains(t, string(first), "| Frukwan |")
	require.Contains(t, string(first), "https://en.wikipedia.org/wiki/Gravediggaz")
}

func TestDiscoverWuFamilySuppressesLedgerCandidates(t *testing.T) {
	store := candidateCatalog()
	ledgers := &memoryLedger{ledger: catalog.CandidateLedger{Version: 1, Candidates: []catalog.Candidate{
		{Name: "Black Brigade", Kind: catalog.CandidateKindGroup, Status: catalog.CandidateRejected},
		{Name: "Wisemen", Aliases: []string{"The Wisemen"}, Kind: catalog.CandidateKindGroup, Status: catalog.CandidatePending},
	}}}
	source := fixedDiscoverySource{items: []artists.DiscoveredArtist{
		{Name: "black brigade", Category: catalog.CategoryAffiliateGroup, Source: "Wikipedia"},
		{Name: "The Wisemen", Category: catalog.CategoryAffiliateGroup, Source: "Wikipedia"},
		{Name: "Tekitha", Category: catalog.CategoryAffiliateArtist, Source: "Wikipedia"},
	}}

	report, err := artists.NewDiscoverWuFamily(store, source).WithLedger(ledgers).Run(context.Background(), artists.DiscoverWuFamilyOptions{CatalogPath: "c", LedgerPath: "l", Apply: true})

	require.NoError(t, err)
	require.Len(t, report.Suppressed, 2)
	require.Equal(t, catalog.CandidateRejected, report.Suppressed[0].LedgerStatus)
	require.Len(t, report.Added, 1)
	require.Equal(t, "tekitha", report.Added[0].Slug)
	require.Zero(t, ledgers.saveCount, "discovery never writes the ledger")
	require.Equal(t, catalog.CandidateRejected, ledgers.ledger.Candidates[0].Status)
}

func TestDiscoverWuFamilyWithoutLedgerBehavesAsBefore(t *testing.T) {
	store := candidateCatalog()
	source := fixedDiscoverySource{items: []artists.DiscoveredArtist{{Name: "Tekitha", Category: catalog.CategoryAffiliateArtist, Source: "Wikipedia"}}}

	report, err := artists.NewDiscoverWuFamily(store, source).WithLedger(&memoryLedger{}).Run(context.Background(), artists.DiscoverWuFamilyOptions{CatalogPath: "c", LedgerPath: "l"})

	require.NoError(t, err)
	require.Empty(t, report.Suppressed)
	require.Len(t, report.New, 1)
}
