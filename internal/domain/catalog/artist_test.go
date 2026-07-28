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

func TestValidateEditorialCatalogReportsInvalidEntries(t *testing.T) {
	c := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{
				Slug:      "Wu Tang",
				Name:      "Wu-Tang Clan",
				SpotifyID: "bad",
				Category:  catalog.Category("unknown"),
				Aliases:   []string{"Clan", " clan "},
				Enabled:   true,
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
	require.Len(t, issues, 7)
}

func TestSlugify(t *testing.T) {
	require.Equal(t, "wu-tang-clan", catalog.Slugify("Wu-Tang Clan"))
	require.Equal(t, "1-4-0-productions", catalog.Slugify("1.4.0. Productions"))
	require.Equal(t, "trūvillain", catalog.Slugify("TrūVillain"))
}
