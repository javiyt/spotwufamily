package artists_test

import (
	"context"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type candidateSearcher struct {
	candidates map[string][]catalog.ArtistCandidate
}

func (c candidateSearcher) SearchArtistCandidates(_ context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	return c.candidates[artist.Slug], nil
}

func TestResolveArtistsSkipsArtistsWithSpotifyIDAndFormatsReport(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:           "wu-tang-clan",
				Name:           "Wu-Tang Clan",
				SpotifyID:      "34EP7KEpOjXcM2TCat1ISk",
				Category:       catalog.CategoryCore,
				EditorialOrder: 1,
			},
			{
				Slug:           "gravediggaz",
				Name:           "Gravediggaz",
				Category:       catalog.CategoryAffiliateGroup,
				EditorialOrder: 2,
			},
		},
	}}
	searcher := candidateSearcher{candidates: map[string][]catalog.ArtistCandidate{
		"gravediggaz": {
			{
				Name:       "Gravediggaz",
				SpotifyID:  "0CH4f9m2L3TRaA5oErU2p0",
				URL:        "https://open.spotify.com/artist/0CH4f9m2L3TRaA5oErU2p0",
				Popularity: 45,
				Followers:  1000,
				Genres:     []string{"hip hop"},
			},
		},
	}}

	report, err := artists.NewResolveArtists(store, searcher).Run(context.Background(), "ignored")

	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	require.Equal(t, "gravediggaz", report.Entries[0].Artist.Slug)

	markdown := string(artists.FormatResolveReportMarkdown(report))
	require.Contains(t, markdown, "Artists needing review: 1")
	require.Contains(t, markdown, "`0CH4f9m2L3TRaA5oErU2p0`")
}

func TestResolveArtistsApplyWritesStrongUnambiguousSpotifyIDs(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{{
			Slug:           "gravediggaz",
			Name:           "Gravediggaz",
			Category:       catalog.CategoryAffiliateGroup,
			Roles:          []catalog.Category{catalog.CategoryAffiliateGroup},
			Aliases:        []string{},
			EditorialOrder: 1,
		}},
	}}
	searcher := candidateSearcher{candidates: map[string][]catalog.ArtistCandidate{
		"gravediggaz": {{
			Name:      "Gravediggaz",
			SpotifyID: "0CH4f9m2L3TRaA5oErU2p0",
		}},
	}}

	report, err := artists.NewResolveArtists(store, searcher).Apply(context.Background(), "ignored", artists.ApplyResolveOptions{})

	require.NoError(t, err)
	require.Len(t, report.Applied, 1)
	require.Empty(t, report.Skipped)
	require.Equal(t, "0CH4f9m2L3TRaA5oErU2p0", store.catalog.Artists[0].SpotifyID)
	require.False(t, store.catalog.Artists[0].Enabled)

	markdown := string(artists.FormatResolveReportMarkdown(report))
	require.Contains(t, markdown, "Applied automatically: 1")
}

func TestResolveArtistsApplySkipsAmbiguousCandidates(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{{
			Slug:           "gravediggaz",
			Name:           "Gravediggaz",
			Category:       catalog.CategoryAffiliateGroup,
			Roles:          []catalog.Category{catalog.CategoryAffiliateGroup},
			Aliases:        []string{},
			EditorialOrder: 1,
		}},
	}}
	searcher := candidateSearcher{candidates: map[string][]catalog.ArtistCandidate{
		"gravediggaz": {
			{Name: "Gravediggaz", SpotifyID: "0CH4f9m2L3TRaA5oErU2p0"},
			{Name: "Gravediggaz", SpotifyID: "1111111111111111111111"},
		},
	}}

	report, err := artists.NewResolveArtists(store, searcher).Apply(context.Background(), "ignored", artists.ApplyResolveOptions{})

	require.NoError(t, err)
	require.Empty(t, report.Applied)
	require.Len(t, report.Skipped, 1)
	require.Empty(t, store.catalog.Artists[0].SpotifyID)
	require.Contains(t, report.Skipped[0].Reason, "ambiguous")
}
