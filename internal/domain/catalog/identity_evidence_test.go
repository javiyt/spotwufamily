package catalog_test

import (
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func releaseEvidence(strong bool) catalog.IdentityEvidence {
	return catalog.IdentityEvidence{
		Kind:        catalog.EvidenceMusicBrainzRelease,
		Polarity:    catalog.EvidencePositive,
		Strong:      strong,
		Source:      "musicbrainz",
		URL:         "https://musicbrainz.org/release-group/abc",
		ReleaseName: "6 Feet Deep",
		ReleaseYear: "1994",
	}
}

func TestEvaluateEligibility(t *testing.T) {
	hipHop := []string{"hip hop"}
	tests := []struct {
		name       string
		artist     catalog.Artist
		candidates []catalog.ArtistCandidate
		policy     catalog.EligibilityPolicy
		status     catalog.EligibilityStatus
		spotifyID  string
		reason     string
	}{
		{
			name:       "exact name alone is never approved",
			artist:     catalog.Artist{Name: "Streetlife"},
			candidates: []catalog.ArtistCandidate{{Name: "Streetlife", SpotifyID: "a"}},
			status:     catalog.EligibilityNeedsReview,
			reason:     "independent",
		},
		{
			name:       "streetlife homonym with hip hop genre and popularity is not proof",
			artist:     catalog.Artist{Name: "Streetlife"},
			candidates: []catalog.ArtistCandidate{{Name: "Streetlife", SpotifyID: "a", Genres: hipHop, Popularity: 70, Followers: 90000}},
			status:     catalog.EligibilityNeedsReview,
		},
		{
			name:       "K.G.B. identical name unrelated artist with plausible genre needs review",
			artist:     catalog.Artist{Name: "K.G.B."},
			candidates: []catalog.ArtistCandidate{{Name: "K.G.B.", SpotifyID: "kgb", Genres: []string{"rap", "hip hop"}}},
			status:     catalog.EligibilityNeedsReview,
		},
		{
			name:   "exact name with strong release evidence is approved",
			artist: catalog.Artist{Name: "Gravediggaz"},
			candidates: []catalog.ArtistCandidate{{
				Name: "Gravediggaz", SpotifyID: "g", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)},
			}},
			status:    catalog.EligibilityApproved,
			spotifyID: "g",
		},
		{
			name:   "single homonymous album title is weak and not approved",
			artist: catalog.Artist{Name: "Gravediggaz"},
			candidates: []catalog.ArtistCandidate{{
				Name: "Gravediggaz", SpotifyID: "g", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(false)},
			}},
			status: catalog.EligibilityNeedsReview,
		},
		{
			name:   "verified track credit counts as strong evidence",
			artist: catalog.Artist{Name: "Solomon Childs"},
			candidates: []catalog.ArtistCandidate{{
				Name: "Solomon Childs", SpotifyID: "s", RelatedArtistEvidence: []string{`credited on Wu-Tang Clan track "Triumph"`},
			}},
			status:    catalog.EligibilityApproved,
			spotifyID: "s",
		},
		{
			name:   "valid alias with strong evidence is approved",
			artist: catalog.Artist{Name: "Method Man", Aliases: []string{"Johnny Blaze"}},
			candidates: []catalog.ArtistCandidate{{
				Name: "Johnny Blaze", SpotifyID: "j", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)},
			}},
			status:    catalog.EligibilityApproved,
			spotifyID: "j",
		},
		{
			name:       "group member with no solo album and no evidence needs review",
			artist:     catalog.Artist{Name: "Blue Raspberry"},
			candidates: []catalog.ArtistCandidate{{Name: "Blue Raspberry", SpotifyID: "b", IdentityEvidence: []catalog.IdentityEvidence{{Kind: catalog.EvidenceMusicBrainzUnavailable, Polarity: catalog.EvidenceWarning, Detail: "no release groups"}}}},
			status:     catalog.EligibilityNeedsReview,
		},
		{
			name:       "obscure artist with insufficient sources stays reviewable",
			artist:     catalog.Artist{Name: "Obscure Wu Affiliate"},
			candidates: []catalog.ArtistCandidate{{Name: "Obscure Wu Affiliate", SpotifyID: "o"}},
			status:     catalog.EligibilityNeedsReview,
		},
		{
			name:   "two same-name candidates are ambiguous even with evidence on both",
			artist: catalog.Artist{Name: "Gravediggaz"},
			candidates: []catalog.ArtistCandidate{
				{Name: "Gravediggaz", SpotifyID: "g1", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)}},
				{Name: "Gravediggaz", SpotifyID: "g2", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)}},
			},
			status: catalog.EligibilityNeedsReview,
			reason: "ambiguous",
		},
		{
			name:   "low scoring candidate is rejected even with evidence",
			artist: catalog.Artist{Name: "Gravediggaz"},
			candidates: []catalog.ArtistCandidate{{
				Name: "Totally Different", SpotifyID: "t", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)},
			}},
			status: catalog.EligibilityRejected,
		},
		{
			name:       "no candidates needs review",
			artist:     catalog.Artist{Name: "Gravediggaz"},
			candidates: nil,
			status:     catalog.EligibilityNeedsReview,
			reason:     "no candidates",
		},
		{
			name:   "previously rejected profile is rejected despite evidence",
			artist: catalog.Artist{Name: "Gravediggaz"},
			candidates: []catalog.ArtistCandidate{{
				Name: "Gravediggaz", SpotifyID: "g", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)},
			}},
			policy: catalog.EligibilityPolicy{IsRejected: func(id string) bool { return id == "g" }},
			status: catalog.EligibilityRejected,
		},
		{
			name:   "genre and popularity are warnings only, never strong",
			artist: catalog.Artist{Name: "Gravediggaz"},
			candidates: []catalog.ArtistCandidate{{
				Name: "Gravediggaz", SpotifyID: "g",
				IdentityEvidence: []catalog.IdentityEvidence{{Kind: catalog.EvidenceGenre, Polarity: catalog.EvidencePositive, Strong: true}},
			}},
			status: catalog.EligibilityNeedsReview,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := catalog.RankCandidates(tt.artist, tt.candidates)
			decision := catalog.EvaluateEligibility(tt.artist, matches, tt.policy)

			require.Equal(t, tt.status, decision.Status, decision.Reasons)
			require.NotEmpty(t, decision.Reasons)
			if tt.status == catalog.EligibilityApproved {
				require.Equal(t, tt.spotifyID, decision.SpotifyID)
				require.NotEmpty(t, decision.Positive)
			} else {
				require.Empty(t, decision.SpotifyID)
			}
			if tt.reason != "" {
				require.Contains(t, joinReasons(decision.Reasons), tt.reason)
			}
		})
	}
}

func TestEvaluateEligibilityHonoursConfiguredThresholds(t *testing.T) {
	artist := catalog.Artist{Name: "Gravediggaz"}
	matches := catalog.RankCandidates(artist, []catalog.ArtistCandidate{{
		Name: "Gravediggaz", SpotifyID: "g", IdentityEvidence: []catalog.IdentityEvidence{releaseEvidence(true)},
	}})

	decision := catalog.EvaluateEligibility(artist, matches, catalog.EligibilityPolicy{MinScore: 101})

	require.Equal(t, catalog.EligibilityRejected, decision.Status)
}

func joinReasons(reasons []string) string {
	out := ""
	for _, r := range reasons {
		out += r + "\n"
	}
	return out
}
