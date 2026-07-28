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
