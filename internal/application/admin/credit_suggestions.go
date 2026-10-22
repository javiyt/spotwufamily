package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	maxCreditReleases  = 4
	maxCreditsPerGroup = 3
	maxCreditNameLen   = 120
)

// CreditClass says how far a credit suggestion can be trusted.
type CreditClass string

// CreditSupporting is the only class: a shared credit corroborates identity
// and helps ranking, but is not proof of group membership and never satisfies
// the independent-evidence rule.
const CreditSupporting CreditClass = "supporting"

// CreditStatus says how completely a card's credits were inspected, so a gap is
// never shown as "no credits".
type CreditStatus string

const (
	CreditsChecked     CreditStatus = "checked"
	CreditsPartial     CreditStatus = "partial"
	CreditsNotChecked  CreditStatus = "not_checked"
	CreditsRateLimited CreditStatus = "rate_limited"
)

// CreditSuggestion is a track on which the profile is credited next to a
// verified editorial group, matched by exact Spotify artist ID.
type CreditSuggestion struct {
	TargetProfileID string
	GroupSlug       string
	GroupName       string
	GroupSpotifyID  string
	TrackID         string
	TrackName       string
	ReleaseID       string
	ReleaseName     string
	URL             string
	ReleaseURL      string
	Provenance      string
	Classification  CreditClass
	Rationale       string
}

// Evidence is the manual `credit` evidence a reviewer may attach. It is never
// persisted unless the reviewer submits a decision with it.
func (s CreditSuggestion) Evidence() catalog.Evidence {
	return catalog.Evidence{Type: catalog.EvidenceCredit, URL: s.URL, ReleaseID: s.ReleaseID, TrackID: s.TrackID, Rationale: s.Rationale}
}

// CreditReport is the "Credits with verified groups" section of a card.
type CreditReport struct {
	Status      CreditStatus
	Message     string
	RetryAfter  time.Duration
	Suggestions []CreditSuggestion
	// Omitted counts further credits left out to keep the card short.
	Omitted int
}

type verifiedGroup struct {
	slug, name, spotifyID string
}

// verifiedGroups lists the reviewed, verified Spotify profiles of editorial
// groups, keyed by exact Spotify ID. The artist under review is excluded.
func verifiedGroups(c catalog.EditorialCatalog, artist catalog.Artist, target string) map[string]verifiedGroup {
	out := map[string]verifiedGroup{}
	for _, other := range c.Artists {
		if other.Slug == artist.Slug || !isGroup(other) {
			continue
		}
		for _, id := range other.AllSpotifyIDs() {
			if id != target && other.StatusOf(id) == catalog.ProfileVerified {
				out[id] = verifiedGroup{slug: other.Slug, name: other.Name, spotifyID: id}
			}
		}
	}
	return out
}

func isGroup(a catalog.Artist) bool {
	if a.Category == catalog.CategoryCore || a.Category == catalog.CategoryAffiliateGroup {
		return true
	}
	for _, role := range a.Roles {
		if role == catalog.CategoryCore || role == catalog.CategoryAffiliateGroup {
			return true
		}
	}
	return false
}

// creditsOf looks for tracks that credit the card's profile and a verified
// group by exact Spotify ID. It reads the first few releases through the
// page's memoised source, so tracklists already read for the card cost nothing.
func (s Service) creditsOf(ctx context.Context, c catalog.EditorialCatalog, artist catalog.Artist, card Card, src *memoSource) CreditReport {
	groups := verifiedGroups(c, artist, card.SpotifyID)
	if len(groups) == 0 {
		return CreditReport{Status: CreditsChecked, Message: "No verified group in the catalog to compare with."}
	}
	if card.Audit.SpotifyStatus == artists.SourceRateLimited {
		return CreditReport{Status: CreditsRateLimited, Message: "Spotify is rate limiting requests, so credits were not checked.", RetryAfter: src.retryAfter()}
	}
	releases := creditReleases(card)
	if len(releases) == 0 {
		return CreditReport{Status: CreditsNotChecked, Message: "No releases are known for this profile, so credits were not checked."}
	}

	var (
		found     []CreditSuggestion
		unchecked int
		limited   bool
	)
	for _, rel := range releases {
		if src.isLimited() {
			limited = true
			break
		}
		tracks, err := src.GetAlbumTracks(ctx, rel.ID)
		if err != nil {
			if errors.Is(err, artists.ErrSourceRateLimited) {
				limited = true
				break
			}
			unchecked++
			continue
		}
		found = append(found, matchCredits(card.SpotifyID, rel, tracks, groups)...)
	}

	report := CreditReport{Status: CreditsChecked}
	report.Suggestions, report.Omitted = boundCredits(found)
	switch {
	case limited:
		report.Status, report.RetryAfter = CreditsRateLimited, src.retryAfter()
		report.Message = "Spotify is rate limiting requests, so credits may be incomplete."
	case unchecked == len(releases):
		report.Status = CreditsNotChecked
		report.Message = "Track credits are not in the local cache (cache unavailable), so they were not checked."
	case unchecked > 0:
		report.Status = CreditsPartial
		report.Message = fmt.Sprintf("Track credits of %d of %d releases are not in the local cache.", unchecked, len(releases))
	}
	return report
}

func creditReleases(card Card) []artists.ProfileRelease {
	var out []artists.ProfileRelease
	seen := map[string]bool{}
	for _, r := range append(append([]artists.ProfileRelease(nil), card.Albums...), card.Singles...) {
		if len(out) >= maxCreditReleases {
			break
		}
		if seen[r.ID] || !catalog.IsSpotifyID(r.ID) {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	return out
}

// matchCredits keeps a group only when the track credits both the target and
// the group by ID, and no credit of that track contradicts the match.
func matchCredits(target string, rel artists.ProfileRelease, tracks []catalog.Track, groups map[string]verifiedGroup) []CreditSuggestion {
	var out []CreditSuggestion
	for _, track := range tracks {
		trackURL := catalog.SpotifyTrackURL(track.SpotifyID)
		if trackURL == "" {
			continue
		}
		names, ok := creditNames(track)
		if !ok {
			continue
		}
		if _, credited := names[target]; !credited {
			continue
		}
		for id, group := range groups {
			creditName, credited := names[id]
			if !credited || nameClash(track, id, creditName, group.name) {
				continue
			}
			s := CreditSuggestion{
				TargetProfileID: target, GroupSlug: group.slug, GroupName: group.name, GroupSpotifyID: id,
				TrackID: track.SpotifyID, TrackName: track.Name, ReleaseID: rel.ID, ReleaseName: rel.Name,
				URL: trackURL, ReleaseURL: rel.URL, Classification: CreditSupporting,
				Provenance: "Spotify track credits (local cache first)",
			}
			s.Rationale = creditRationale(s)
			if len(catalog.ValidateEvidence(s.Evidence())) > 0 {
				continue
			}
			out = append(out, s)
		}
	}
	return out
}

// creditNames maps credited artist IDs to their name. A track whose credits
// list one ID under two names is ambiguous and yields nothing.
func creditNames(track catalog.Track) (map[string]string, bool) {
	names := map[string]string{}
	for _, credit := range track.Artists {
		if !catalog.IsSpotifyID(credit.SpotifyID) {
			continue
		}
		if prev, ok := names[credit.SpotifyID]; ok && normalizeName(prev) != normalizeName(credit.Name) {
			return nil, false
		}
		names[credit.SpotifyID] = credit.Name
	}
	return names, true
}

// nameClash reports another credit on the track that carries the group's name
// under a different (or missing) ID, which makes the match ambiguous.
func nameClash(track catalog.Track, groupID string, creditName, groupName string) bool {
	keys := map[string]bool{normalizeName(creditName): true, normalizeName(groupName): true}
	for _, credit := range track.Artists {
		if credit.SpotifyID != groupID && keys[normalizeName(credit.Name)] {
			return true
		}
	}
	return false
}

func creditRationale(s CreditSuggestion) string {
	return fmt.Sprintf("Spotify credits this profile and the verified group %q (%s) on the track %q from %q. Supporting evidence, not independent: a shared credit does not prove membership of the group.",
		clip(s.GroupName), s.GroupSlug, clip(s.TrackName), clip(s.ReleaseName))
}

func clip(s string) string {
	if r := []rune(s); len(r) > maxCreditNameLen {
		return string(r[:maxCreditNameLen]) + "…"
	}
	return s
}

// boundCredits dedupes by group and track, orders deterministically and keeps
// the first few per group.
func boundCredits(found []CreditSuggestion) ([]CreditSuggestion, int) {
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.GroupSlug != b.GroupSlug {
			return a.GroupSlug < b.GroupSlug
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return a.ReleaseID < b.ReleaseID
	})
	var out []CreditSuggestion
	perGroup := map[string]int{}
	seen := map[string]bool{}
	omitted := 0
	for _, s := range found {
		key := s.GroupSlug + "|" + s.TrackID
		if seen[key] {
			continue
		}
		seen[key] = true
		if perGroup[s.GroupSlug] >= maxCreditsPerGroup {
			omitted++
			continue
		}
		perGroup[s.GroupSlug]++
		out = append(out, s)
	}
	return out, omitted
}
