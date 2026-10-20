package yamlcatalog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestStoreSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{{
			Slug:           "wu-tang-clan",
			Name:           "Wu-Tang Clan",
			SpotifyID:      "34EP7KEpOjXcM2TCat1ISk",
			SpotifyIDs:     []string{"0H8YCcvC3MPLKnbDRasGiG"},
			Genres:         []string{"east coast hip hop", "hardcore hip hop"},
			Category:       catalog.CategoryCore,
			Roles:          []catalog.Category{catalog.CategoryCore},
			Aliases:        []string{"Wu Tang Clan"},
			Enabled:        true,
			EditorialOrder: 1,
			Notes:          "",
			ImageURL:       "https://i.scdn.co/image/artist-large",
		}},
	}

	require.NoError(t, store.Save(context.Background(), path, c))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(first), "genres:")
	require.Contains(t, string(first), "east coast hip hop")
	require.Contains(t, string(first), "image_url: https://i.scdn.co/image/artist-large")

	require.NoError(t, store.Save(context.Background(), path, c))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, first, second)

	loaded, err := store.Load(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, c, loaded)
}

func TestStoreRoundTripsProfilesAndPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	c := catalog.EditorialCatalog{
		Version:           1,
		ProfileSyncPolicy: catalog.SyncVerifiedOnly,
		Artists: []catalog.Artist{{
			Slug: "rza", Name: "RZA", SpotifyID: "34EP7KEpOjXcM2TCat1ISk", Category: catalog.CategoryCore, Aliases: []string{},
			SpotifyProfiles: []catalog.SpotifyProfile{
				{
					ID: "34EP7KEpOjXcM2TCat1ISk", Status: catalog.ProfileVerified, Reviewer: "javi",
					ReviewedAt: "2026-01-02T15:04:05Z", LastCheckedAt: "2026-02-03T00:00:00Z",
					Evidence: []catalog.Evidence{
						{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/1", Rationale: "linked"},
						{Type: catalog.EvidenceCredit, ReleaseID: "0H8YCcvC3MPLKnbDRasGiG", TrackID: "1111111111111111111111", Rationale: "credited"},
					},
				},
				{ID: "0H8YCcvC3MPLKnbDRasGiG", Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z",
					Evidence: []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "different person"}}},
			},
		}},
	}

	require.NoError(t, store.Save(context.Background(), path, c))
	loaded, err := store.Load(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, c, loaded)
	require.Empty(t, catalog.ValidateEditorialCatalog(loaded))
}

func TestStoreOmitsProfileFieldsForLegacyCatalogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	c := catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{{Slug: "rza", Name: "RZA", SpotifyID: "34EP7KEpOjXcM2TCat1ISk", Category: catalog.CategoryCore}}}
	require.NoError(t, store.Save(context.Background(), path, c))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "spotify_profiles")
	require.NotContains(t, string(raw), "profile_sync_policy")
}

func TestStoreRejectsUnknownProfileFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	body := "version: 1\nartists:\n  - slug: a\n    name: A\n    spotify_profiles:\n      - id: 34EP7KEpOjXcM2TCat1ISk\n        status: candidate\n        confidence: 0.9\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	_, err := yamlcatalog.NewStore().Load(context.Background(), path)
	require.Error(t, err)
}

func TestStoreKeepsBackupOfPreviousCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	first := catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{{Slug: "a", Name: "A", Category: catalog.CategoryCore}}}
	require.NoError(t, store.Save(context.Background(), path, first))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	first.Artists[0].Name = "A2"
	require.NoError(t, store.Save(context.Background(), path, first))

	backup, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	require.Equal(t, before, backup)
}

func TestRepositoryCatalogRoundTripsByteForByte(t *testing.T) {
	const source = "../../../../data/artists.yaml"
	original, err := os.ReadFile(source)
	require.NoError(t, err)

	store := yamlcatalog.NewStore()
	loaded, err := store.Load(context.Background(), source)
	require.NoError(t, err)
	require.Empty(t, catalog.ValidateEditorialCatalog(loaded))

	out := filepath.Join(t.TempDir(), "artists.yaml")
	require.NoError(t, store.Save(context.Background(), out, loaded))
	saved, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, string(original), string(saved), "loading and saving must not lose or reorder anything")
}

// TestRepositoryCatalogHomonymDispositions pins the #263 review decisions so a
// later edit cannot silently re-link a rejected homonym profile.
func TestRepositoryCatalogHomonymDispositions(t *testing.T) {
	c, err := yamlcatalog.NewStore().Load(context.Background(), filepath.Join("..", "..", "..", "..", "data", "artists.yaml"))
	require.NoError(t, err)
	require.Empty(t, catalog.ValidateEditorialCatalog(c))

	bySlug := map[string]catalog.Artist{}
	for _, artist := range c.Artists {
		bySlug[artist.Slug] = artist
	}

	streetlife := bySlug["streetlife"]
	require.Equal(t, []string{"7CrVv4zLga71gdlMOMfjXL"}, streetlife.AllSpotifyIDs())
	verified, ok := streetlife.ProfileFor("7CrVv4zLga71gdlMOMfjXL")
	require.True(t, ok)
	require.Equal(t, catalog.ProfileVerified, verified.Status)
	rejected, ok := streetlife.ProfileFor("0FCYBhXjpfQIQWNKiVEDg5")
	require.True(t, ok, "the previous ID stays in the audit history")
	require.Equal(t, catalog.ProfileRejected, rejected.Status)
	require.NotEmpty(t, rejected.Evidence)

	kgb := bySlug["k-g-b"]
	require.Empty(t, kgb.AllSpotifyIDs(), "no verified replacement: quarantined, not guessed")
	require.False(t, kgb.Enabled)
	rejected, ok = kgb.ProfileFor("2gKwx0Lm0JoOrN2mY9Z0PG")
	require.True(t, ok)
	require.Equal(t, catalog.ProfileRejected, rejected.Status)
	require.NotEmpty(t, rejected.Evidence)
}
