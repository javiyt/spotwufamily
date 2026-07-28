package artists

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type CatalogStore interface {
	Load(context.Context, string) (catalog.EditorialCatalog, error)
	Save(context.Context, string, catalog.EditorialCatalog) error
}

type ValidateCatalog struct {
	store CatalogStore
}

func NewValidateCatalog(store CatalogStore) ValidateCatalog {
	return ValidateCatalog{store: store}
}

func (v ValidateCatalog) Run(ctx context.Context, path string) ([]catalog.ValidationIssue, error) {
	c, err := v.store.Load(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("load artist catalog: %w", err)
	}

	return catalog.ValidateEditorialCatalog(c), nil
}

type ImportGroups struct {
	store CatalogStore
}

func NewImportGroups(store CatalogStore) ImportGroups {
	return ImportGroups{store: store}
}

type ImportGroupsResult struct {
	Artists              int
	ExactDuplicates      []string
	NormalizedDuplicates []string
}

func (i ImportGroups) Run(ctx context.Context, groups io.Reader, outputPath string) (ImportGroupsResult, error) {
	lines, err := readGroupLines(groups)
	if err != nil {
		return ImportGroupsResult{}, err
	}

	c, result := buildCatalog(lines)
	if err := i.store.Save(ctx, outputPath, c); err != nil {
		return ImportGroupsResult{}, fmt.Errorf("save artist catalog: %w", err)
	}

	return result, nil
}

func readGroupLines(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}

	rawLines := strings.Split(string(data), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}

	return lines, nil
}

func buildCatalog(lines []string) (catalog.EditorialCatalog, ImportGroupsResult) {
	exactSeen := map[string]int{}
	normalizedSeen := map[string]string{}
	artistsBySlug := map[string]*catalog.Artist{}
	result := ImportGroupsResult{}

	for index, name := range lines {
		exactKey := name
		if _, ok := exactSeen[exactKey]; ok {
			result.ExactDuplicates = append(result.ExactDuplicates, name)
		}
		exactSeen[exactKey] = index

		normalizedKey := strings.ToLower(name)
		if previous, ok := normalizedSeen[normalizedKey]; ok && previous != name {
			result.NormalizedDuplicates = append(result.NormalizedDuplicates, name)
		}
		normalizedSeen[normalizedKey] = name

		category := categoryForOrder(index + 1)
		slug := catalog.Slugify(name)
		if existing, ok := artistsBySlug[slug]; ok {
			existing.Roles = appendRole(existing.Roles, category)
			continue
		}

		slug = uniqueSlug(slug, artistsBySlug)
		artist := catalog.Artist{
			Slug:           slug,
			Name:           name,
			Category:       category,
			Roles:          []catalog.Category{category},
			Aliases:        aliasesFor(name),
			Enabled:        false,
			EditorialOrder: len(artistsBySlug) + 1,
		}
		artistsBySlug[slug] = &artist
	}

	artists := make([]catalog.Artist, 0, len(artistsBySlug))
	for _, artist := range artistsBySlug {
		artists = append(artists, *artist)
	}
	sort.SliceStable(artists, func(i, j int) bool {
		return artists[i].EditorialOrder < artists[j].EditorialOrder
	})

	result.Artists = len(artists)
	return catalog.EditorialCatalog{Version: 1, Artists: artists}, result
}

func categoryForOrder(order int) catalog.Category {
	switch {
	case order == 1:
		return catalog.CategoryCore
	case order <= 37:
		return catalog.CategoryAffiliateGroup
	case order <= 82:
		return catalog.CategoryAffiliateArtist
	case order <= 96:
		return catalog.CategoryProducer
	default:
		return catalog.CategoryCollaborator
	}
}

func aliasesFor(name string) []string {
	if name == "Wu-Tang Clan" {
		return []string{"Wu Tang Clan"}
	}

	return []string{}
}

func uniqueSlug(slug string, existing map[string]*catalog.Artist) string {
	if slug == "" {
		slug = "artist"
	}
	if _, ok := existing[slug]; !ok {
		return slug
	}

	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", slug, suffix)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

func appendRole(roles []catalog.Category, role catalog.Category) []catalog.Category {
	for _, existing := range roles {
		if existing == role {
			return roles
		}
	}

	return append(roles, role)
}
