package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
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

type warningInteractiveSearcher struct{}

func (warningInteractiveSearcher) SearchArtistCandidates(context.Context, catalog.Artist) ([]catalog.ArtistCandidate, error) {
	return nil, nil
}

func (warningInteractiveSearcher) ReviewConfiguredSpotifyIDs(context.Context, catalog.Artist) ([]artists.ConfiguredSpotifyIDWarning, error) {
	return []artists.ConfiguredSpotifyIDWarning{{
		SpotifyID:             "1111111111111111111111",
		Reason:                "no Spotify albums matched MusicBrainz release groups",
		SpotifyAlbumCount:     1,
		MusicBrainzAlbumCount: 1,
		SpotifyAlbums:         []artists.AuditedAlbum{{ID: "album-2", Title: "Unrelated Album", Year: "2020"}},
		MusicBrainzAlbums:     []artists.AuditedAlbum{{ID: "mb-1", Title: "Enter the Wu-Tang (36 Chambers)", Year: "1993"}},
	}}, nil
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

func TestExecuteArtistsResolveInteractiveShowsMusicBrainzWarningForConfiguredSpotifyID(t *testing.T) {
	store := &interactiveStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{{
			Slug:           "wu-tang-clan",
			Name:           "Wu-Tang Clan",
			SpotifyID:      "1111111111111111111111",
			Category:       catalog.CategoryCore,
			Roles:          []catalog.Category{catalog.CategoryCore},
			Aliases:        []string{},
			EditorialOrder: 1,
		}},
	}}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeArtistsResolveInteractive(
		context.Background(),
		strings.NewReader("k\n"),
		&stdout,
		&stderr,
		store,
		warningInteractiveSearcher{},
		resolveOptions{catalogPath: "artists.yaml", reviewAll: true},
	)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "MusicBrainz warnings:")
	require.Contains(t, stdout.String(), "1111111111111111111111: no Spotify albums matched MusicBrainz release groups")
	require.Contains(t, stdout.String(), "Spotify sample: Unrelated Album (2020)")
	require.Contains(t, stdout.String(), "MusicBrainz sample: Enter the Wu-Tang (36 Chambers) (1993)")
	require.Contains(t, stdout.String(), "interactive resolve: applied=0 skipped=0 kept=1 cleared=0")
}

func TestPrintInteractiveMatchesShowsTrackEvidence(t *testing.T) {
	var stdout bytes.Buffer

	printInteractiveMatches(&stdout, []catalog.CandidateMatch{{
		Candidate: catalog.ArtistCandidate{
			Name:                  "Solomon Childs",
			SpotifyID:             "1111111111111111111111",
			RelatedArtistEvidence: []string{"credited on Configured Group track \"Feature Track\""},
		},
		Score:      95,
		Confidence: "strong",
	}}, 1)

	require.Contains(t, stdout.String(), "track_evidence=credited on Configured Group track")
}
