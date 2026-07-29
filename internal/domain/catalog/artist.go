package catalog

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Category string

const (
	CategoryCore            Category = "core"
	CategoryAffiliateGroup  Category = "affiliate_group"
	CategoryAffiliateArtist Category = "affiliate_artist"
	CategoryProducer        Category = "producer"
	CategoryCollaborator    Category = "collaborator"
)

var (
	slugPattern      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	spotifyIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)
	httpsURLPattern  = regexp.MustCompile(`^https://[^\s]+$`)
)

type Artist struct {
	Slug           string
	Name           string
	PublicName     string
	SpotifyID      string
	SpotifyIDs     []string
	Genres         []string
	Category       Category
	Roles          []Category
	Aliases        []string
	Enabled        bool
	EditorialOrder int
	Notes          string
	ExternalURL    string
	AddedAt        string
}

type EditorialCatalog struct {
	Version int
	Artists []Artist
}

type ValidationIssue struct {
	Field   string
	Message string
}

func (v ValidationIssue) Error() string {
	if v.Field == "" {
		return v.Message
	}

	return fmt.Sprintf("%s: %s", v.Field, v.Message)
}

func ValidateEditorialCatalog(c EditorialCatalog) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	if c.Version != 1 {
		issues = append(issues, ValidationIssue{Field: "version", Message: "must be 1"})
	}
	if len(c.Artists) == 0 {
		issues = append(issues, ValidationIssue{Field: "artists", Message: "must contain at least one artist"})
	}

	slugs := map[string]int{}
	names := map[string]int{}
	spotifyIDs := map[string]int{}
	editorialOrders := map[int]int{}
	publicNames := map[string]int{}
	aliases := map[string]int{}

	for i, artist := range c.Artists {
		prefix := fmt.Sprintf("artists[%d]", i)
		issues = append(issues, validateArtist(prefix, artist)...)

		addDuplicateIssue := func(index map[string]int, key, field, message string) {
			if key == "" {
				return
			}
			if previous, ok := index[key]; ok {
				issues = append(issues, ValidationIssue{
					Field:   field,
					Message: fmt.Sprintf("%s; first seen at artists[%d]", message, previous),
				})
				return
			}
			index[key] = i
		}

		addDuplicateIssue(slugs, artist.Slug, prefix+".slug", "duplicate slug")
		addDuplicateIssue(names, normalizeIdentity(artist.Name), prefix+".name", "duplicate name")
		addDuplicateIssue(publicNames, normalizeIdentity(artist.PublicName), prefix+".public_name", "duplicate public name")
		for idIndex, spotifyID := range artist.AllSpotifyIDs() {
			field := prefix + ".spotify_id"
			if idIndex > 0 || artist.SpotifyID == "" {
				field = fmt.Sprintf("%s.spotify_ids[%d]", prefix, idIndex)
			}
			addDuplicateIssue(spotifyIDs, spotifyID, field, "duplicate Spotify ID")
		}

		if artist.EditorialOrder > 0 {
			if previous, ok := editorialOrders[artist.EditorialOrder]; ok {
				issues = append(issues, ValidationIssue{
					Field:   prefix + ".editorial_order",
					Message: fmt.Sprintf("duplicate editorial order; first seen at artists[%d]", previous),
				})
			} else {
				editorialOrders[artist.EditorialOrder] = i
			}
		}

		for aliasIndex, alias := range artist.Aliases {
			key := normalizeIdentity(alias)
			if key == "" {
				continue
			}
			if previous, ok := aliases[key]; ok && previous != i {
				issues = append(issues, ValidationIssue{
					Field:   fmt.Sprintf("%s.aliases[%d]", prefix, aliasIndex),
					Message: fmt.Sprintf("alias duplicates alias from artists[%d]", previous),
				})
			} else if !ok {
				aliases[key] = i
			}
			if previous, ok := names[key]; ok && previous != i {
				issues = append(issues, ValidationIssue{
					Field:   fmt.Sprintf("%s.aliases[%d]", prefix, aliasIndex),
					Message: fmt.Sprintf("alias duplicates artist name from artists[%d]", previous),
				})
			}
		}
	}

	return issues
}

func validateArtist(prefix string, artist Artist) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	if strings.TrimSpace(artist.Slug) == "" {
		issues = append(issues, ValidationIssue{Field: prefix + ".slug", Message: "is required"})
	} else if !slugPattern.MatchString(artist.Slug) {
		issues = append(issues, ValidationIssue{Field: prefix + ".slug", Message: "must use lowercase letters, numbers and single hyphens"})
	}

	if strings.TrimSpace(artist.Name) == "" {
		issues = append(issues, ValidationIssue{Field: prefix + ".name", Message: "is required"})
	}

	if strings.TrimSpace(artist.PublicName) != "" && normalizeIdentity(artist.PublicName) == normalizeIdentity(artist.Name) {
		issues = append(issues, ValidationIssue{Field: prefix + ".public_name", Message: "must be omitted when equal to name"})
	}

	if !IsKnownCategory(artist.Category) {
		issues = append(issues, ValidationIssue{Field: prefix + ".category", Message: "is unknown"})
	}

	roleSet := map[Category]int{}
	for roleIndex, role := range artist.Roles {
		if !IsKnownCategory(role) {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("%s.roles[%d]", prefix, roleIndex),
				Message: "is unknown",
			})
		}
		if previous, ok := roleSet[role]; ok {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("%s.roles[%d]", prefix, roleIndex),
				Message: fmt.Sprintf("duplicate role; first seen at roles[%d]", previous),
			})
		}
		roleSet[role] = roleIndex
	}
	if len(artist.Roles) > 0 {
		if _, ok := roleSet[artist.Category]; !ok {
			issues = append(issues, ValidationIssue{Field: prefix + ".roles", Message: "must include primary category"})
		}
	}

	if artist.SpotifyID != "" && !spotifyIDPattern.MatchString(artist.SpotifyID) {
		issues = append(issues, ValidationIssue{Field: prefix + ".spotify_id", Message: "has invalid format"})
	}
	for index, spotifyID := range artist.SpotifyIDs {
		if spotifyID == "" {
			issues = append(issues, ValidationIssue{Field: fmt.Sprintf("%s.spotify_ids[%d]", prefix, index), Message: "must not be empty"})
			continue
		}
		if !spotifyIDPattern.MatchString(spotifyID) {
			issues = append(issues, ValidationIssue{Field: fmt.Sprintf("%s.spotify_ids[%d]", prefix, index), Message: "has invalid format"})
		}
	}
	if artist.SpotifyID != "" {
		for index, spotifyID := range artist.SpotifyIDs {
			if spotifyID == artist.SpotifyID {
				issues = append(issues, ValidationIssue{Field: fmt.Sprintf("%s.spotify_ids[%d]", prefix, index), Message: "duplicates primary Spotify ID"})
			}
		}
	}
	seenSpotifyIDs := map[string]int{}
	for index, spotifyID := range artist.SpotifyIDs {
		if spotifyID == "" {
			continue
		}
		if previous, ok := seenSpotifyIDs[spotifyID]; ok {
			issues = append(issues, ValidationIssue{Field: fmt.Sprintf("%s.spotify_ids[%d]", prefix, index), Message: fmt.Sprintf("duplicate Spotify ID; first seen at spotify_ids[%d]", previous)})
			continue
		}
		seenSpotifyIDs[spotifyID] = index
	}

	genres := map[string]int{}
	for genreIndex, genre := range artist.Genres {
		key := normalizeIdentity(genre)
		if key == "" {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("%s.genres[%d]", prefix, genreIndex),
				Message: "must not be empty",
			})
			continue
		}
		if previous, ok := genres[key]; ok {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("%s.genres[%d]", prefix, genreIndex),
				Message: fmt.Sprintf("duplicate genre; first seen at genres[%d]", previous),
			})
			continue
		}
		genres[key] = genreIndex
	}

	if artist.Enabled && len(artist.AllSpotifyIDs()) == 0 {
		issues = append(issues, ValidationIssue{Field: prefix + ".spotify_id", Message: "is required when artist is enabled"})
	}

	aliases := map[string]int{}
	for aliasIndex, alias := range artist.Aliases {
		key := normalizeIdentity(alias)
		if key == "" {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("%s.aliases[%d]", prefix, aliasIndex),
				Message: "must not be empty",
			})
			continue
		}
		if previous, ok := aliases[key]; ok {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("%s.aliases[%d]", prefix, aliasIndex),
				Message: fmt.Sprintf("duplicate alias; first seen at aliases[%d]", previous),
			})
			continue
		}
		aliases[key] = aliasIndex
	}

	if artist.ExternalURL != "" && !httpsURLPattern.MatchString(artist.ExternalURL) {
		issues = append(issues, ValidationIssue{Field: prefix + ".external_url", Message: "must be an https URL"})
	}
	if artist.AddedAt != "" {
		if _, err := time.Parse(time.DateOnly, artist.AddedAt); err != nil {
			issues = append(issues, ValidationIssue{Field: prefix + ".added_at", Message: "must use YYYY-MM-DD"})
		}
	}

	return issues
}

func (a Artist) AllSpotifyIDs() []string {
	ids := make([]string, 0, 1+len(a.SpotifyIDs))
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(a.SpotifyID)
	for _, id := range a.SpotifyIDs {
		add(id)
	}
	return ids
}

func (a Artist) HasSpotifyID(id string) bool {
	for _, existing := range a.AllSpotifyIDs() {
		if existing == id {
			return true
		}
	}
	return false
}

func IsKnownCategory(category Category) bool {
	switch category {
	case CategoryCore, CategoryAffiliateGroup, CategoryAffiliateArtist, CategoryProducer, CategoryCollaborator:
		return true
	default:
		return false
	}
}

func normalizeIdentity(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
