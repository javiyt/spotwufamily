package artists_test

import (
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func integrityCatalog(policy catalog.ProfileSyncPolicy, artist catalog.Artist) catalog.EditorialCatalog {
	artist.Slug, artist.Name = "act", "Act"
	artist.Category, artist.Roles = catalog.CategoryAffiliateArtist, []catalog.Category{catalog.CategoryAffiliateArtist}
	artist.EditorialOrder = 1
	return catalog.EditorialCatalog{Version: 1, ProfileSyncPolicy: policy, Artists: []catalog.Artist{artist}}
}

func codes(report artists.IntegrityReport) []artists.IntegrityCode {
	out := []artists.IntegrityCode{}
	for _, f := range report.Findings {
		out = append(out, f.Code)
	}
	return out
}

func TestCheckIntegrityWarnsOnLegacyUnreviewedSyncButDoesNotFail(t *testing.T) {
	report := artists.CheckIntegrity(integrityCatalog(catalog.SyncAllowUnreviewed, catalog.Artist{
		Enabled: true, SpotifyID: "1111111111111111111111",
	}))

	require.ElementsMatch(t, []artists.IntegrityCode{artists.IntegrityPolicyAllowsLegacy, artists.IntegrityUnreviewedSync}, codes(report))
	require.False(t, report.Failed(false))
	require.True(t, report.Failed(true))
}

func TestCheckIntegrityWarnsWhenVerifiedOnlyBySpotifyPage(t *testing.T) {
	report := artists.CheckIntegrity(integrityCatalog(catalog.SyncVerifiedOnly, catalog.Artist{
		Enabled: true, SpotifyID: "1111111111111111111111",
		SpotifyProfiles: []catalog.SpotifyProfile{{
			ID: "1111111111111111111111", Status: catalog.ProfileVerified, Reviewer: "r", ReviewedAt: "2026-10-20T12:00:00Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceSpotifyProfile, URL: "https://open.spotify.com/artist/1111111111111111111111", Rationale: "found"}},
		}},
	}))

	require.Equal(t, []artists.IntegrityCode{artists.IntegrityCircularEvidence}, codes(report))
	require.False(t, report.Failed(false))
}

func TestCheckIntegrityEmptyCatalogReportsSchemaErrorWithNonNilFindings(t *testing.T) {
	report := artists.CheckIntegrity(catalog.EditorialCatalog{Version: 1})
	require.True(t, report.Failed(false))
	require.NotNil(t, report.Findings)
}
