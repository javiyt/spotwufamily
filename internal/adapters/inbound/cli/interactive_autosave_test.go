package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type interactiveStore struct {
	catalog   catalog.EditorialCatalog
	saveCount int
}

func (s *interactiveStore) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return s.catalog, nil
}

func (s *interactiveStore) Save(_ context.Context, _ string, c catalog.EditorialCatalog) error {
	s.catalog = c
	s.saveCount++
	return nil
}

type flakyInteractiveSearcher struct{}

func (flakyInteractiveSearcher) SearchArtistCandidates(_ context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	if artist.Slug == "a-i-g" {
		return nil, errors.New("musicbrainz timeout")
	}
	return []catalog.ArtistCandidate{{Name: "Wu-Tang Clan", SpotifyID: "34EP7KEpOjXcM2TCat1ISk"}}, nil
}

func TestExecuteArtistsResolveInteractiveAutosavesAndContinuesAfterSearchError(t *testing.T) {
	store := &interactiveStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:           "wu-tang-clan",
				Name:           "Wu-Tang Clan",
				Category:       catalog.CategoryCore,
				Roles:          []catalog.Category{catalog.CategoryCore},
				Aliases:        []string{},
				EditorialOrder: 1,
			},
			{
				Slug:           "a-i-g",
				Name:           "A.I.G.",
				SpotifyID:      "4W91zWkx3syrUDACiK5Jxh",
				Category:       catalog.CategoryAffiliateGroup,
				Roles:          []catalog.Category{catalog.CategoryAffiliateGroup},
				Aliases:        []string{},
				Enabled:        true,
				EditorialOrder: 2,
			},
		},
	}}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeArtistsResolveInteractive(
		context.Background(),
		strings.NewReader("1\n"),
		&stdout,
		&stderr,
		store,
		flakyInteractiveSearcher{},
		resolveOptions{catalogPath: "artists.yaml", reviewAll: true},
	)

	require.Equal(t, 0, code)
	require.Contains(t, stderr.String(), "artists resolve: search a-i-g: musicbrainz timeout")
	require.Contains(t, stdout.String(), "interactive resolve: applied=1 skipped=1 kept=0 cleared=0")
	require.Equal(t, "34EP7KEpOjXcM2TCat1ISk", store.catalog.Artists[0].SpotifyID)
	require.GreaterOrEqual(t, store.saveCount, 2)
}
