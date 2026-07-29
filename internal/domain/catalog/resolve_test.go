package catalog_test

import (
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestRankCandidates(t *testing.T) {
	artist := catalog.Artist{
		Name:    "Wu-Tang Clan",
		Aliases: []string{"Wu Tang Clan"},
	}

	matches := catalog.RankCandidates(artist, []catalog.ArtistCandidate{
		{Name: "The Clan"},
		{Name: "Wu Tang Clan"},
		{Name: "Wu-Tang Clan"},
	})

	require.Equal(t, "Wu-Tang Clan", matches[0].Candidate.Name)
	require.Equal(t, 100, matches[0].Score)
	require.Equal(t, "strong", matches[0].Confidence)
	require.Equal(t, "Wu Tang Clan", matches[1].Candidate.Name)
	require.Equal(t, 95, matches[1].Score)
}

func TestRankCandidatesUsesHipHopGenresAsEvidence(t *testing.T) {
	artist := catalog.Artist{Name: "A.I.G."}

	matches := catalog.RankCandidates(artist, []catalog.ArtistCandidate{
		{Name: "A I G Orchestra", Genres: []string{"classical"}},
		{Name: "A I G Crew", Genres: []string{"east coast hip hop"}},
	})

	require.Equal(t, "A I G Crew", matches[0].Candidate.Name)
	require.Equal(t, 68, matches[0].Score)
	require.Contains(t, matches[0].Reason, "hip-hop genre evidence")
	require.Equal(t, "A I G Orchestra", matches[1].Candidate.Name)
	require.Equal(t, 50, matches[1].Score)
	require.Contains(t, matches[1].Reason, "non hip-hop genre evidence")
}

func TestRankCandidatesDegradesExactMatchWithKnownNonHipHopGenres(t *testing.T) {
	matches := catalog.RankCandidates(catalog.Artist{Name: "A.I.G."}, []catalog.ArtistCandidate{
		{Name: "A.I.G.", Genres: []string{"classical"}},
	})

	require.Equal(t, 90, matches[0].Score)
	require.Equal(t, "possible", matches[0].Confidence)
	require.Contains(t, matches[0].Reason, "non hip-hop genre evidence")
}

func TestRankCandidatesUsesStoredArtistGenresForCompatibility(t *testing.T) {
	artist := catalog.Artist{Name: "Achozen", Genres: []string{"alternative hip hop", "rap rock"}}

	matches := catalog.RankCandidates(artist, []catalog.ArtistCandidate{
		{Name: "Achozen", Genres: []string{"new age"}},
		{Name: "Achozen", Genres: []string{"rap rock"}},
	})

	require.Equal(t, []string{"rap rock"}, matches[0].Candidate.Genres)
	require.Equal(t, 100, matches[0].Score)
	require.Contains(t, matches[0].Reason, "similar genre evidence")
	require.Equal(t, []string{"new age"}, matches[1].Candidate.Genres)
	require.Equal(t, 80, matches[1].Score)
	require.Contains(t, matches[1].Reason, "incompatible genre evidence")
}
