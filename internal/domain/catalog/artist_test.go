package catalog_test

import (
	"testing"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestValidateEditorialCatalogAcceptsDisabledArtistsWithoutSpotifyID(t *testing.T) {
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{{
			Slug:     "wu-tang-clan",
			Name:     "Wu-Tang Clan",
			Category: catalog.CategoryCore,
			Aliases:  []string{"Wu Tang Clan"},
			Enabled:  false,
		}},
	}

	require.Empty(t, catalog.ValidateEditorialCatalog(c))
}

func TestArtistSupportsMultipleSpotifyIDs(t *testing.T) {
	artist := catalog.Artist{
		SpotifyID:  "34EP7KEpOjXcM2TCat1ISk",
		SpotifyIDs: []string{"0H8YCcvC3MPLKnbDRasGiG", "34EP7KEpOjXcM2TCat1ISk"},
	}

	require.Equal(t, []string{"34EP7KEpOjXcM2TCat1ISk", "0H8YCcvC3MPLKnbDRasGiG"}, artist.AllSpotifyIDs())
	require.True(t, artist.HasSpotifyID("0H8YCcvC3MPLKnbDRasGiG"))
}

func TestValidateEditorialCatalogReportsInvalidEntries(t *testing.T) {
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:       "Wu Tang",
				Name:       "Wu-Tang Clan",
				PublicName: "Wu-Tang Clan",
				SpotifyID:  "bad",
				SpotifyIDs: []string{"bad", "bad"},
				Category:   catalog.Category("unknown"),
				Aliases:    []string{"Clan", " clan "},
				Enabled:    true,
			},
			{
				Slug:      "wu-tang",
				Name:      " wu-tang clan ",
				SpotifyID: "bad",
				Category:  catalog.CategoryCore,
				Enabled:   false,
			},
		},
	}

	issues := catalog.ValidateEditorialCatalog(c)

	require.ErrorContains(t, issues[0], "slug")
	require.Len(t, issues, 13)
}

func TestValidateEditorialCatalogReportsEditorialIssues(t *testing.T) {
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:           "wu-tang-clan",
				Name:           "Wu-Tang Clan",
				Category:       catalog.CategoryCore,
				Roles:          []catalog.Category{catalog.CategoryCore, catalog.CategoryCore},
				Aliases:        []string{"Wu Tang Clan"},
				EditorialOrder: 1,
				ExternalURL:    "http://example.com",
				AddedAt:        "20260728",
			},
			{
				Slug:           "gravediggaz",
				Name:           "Gravediggaz",
				Category:       catalog.CategoryAffiliateGroup,
				Roles:          []catalog.Category{catalog.CategoryProducer},
				Aliases:        []string{"Wu Tang Clan"},
				EditorialOrder: 1,
			},
		},
	}

	issues := catalog.ValidateEditorialCatalog(c)

	require.Len(t, issues, 6)
	require.ErrorContains(t, issues[0], "duplicate role")
}

func TestSlugify(t *testing.T) {
	require.Equal(t, "wu-tang-clan", catalog.Slugify("Wu-Tang Clan"))
	require.Equal(t, "1-4-0-productions", catalog.Slugify("1.4.0. Productions"))
	require.Equal(t, "trūvillain", catalog.Slugify("TrūVillain"))
}
