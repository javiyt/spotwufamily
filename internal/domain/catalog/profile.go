package catalog

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ProfileStatus is the editorial review state of one Spotify profile linked to
// (or considered for) an artist. It is independent from Artist.Enabled.
type ProfileStatus string

const (
	ProfileUnreviewed  ProfileStatus = "unreviewed"
	ProfileCandidate   ProfileStatus = "candidate"
	ProfileVerified    ProfileStatus = "verified"
	ProfileRejected    ProfileStatus = "rejected"
	ProfileNeedsReview ProfileStatus = "needs_review"
)

// ProfileSyncPolicy decides which review states may be ingested by sync.
type ProfileSyncPolicy string

const (
	// SyncVerifiedOnly ingests verified profiles only. It is the default when
	// the catalog does not declare a policy.
	SyncVerifiedOnly ProfileSyncPolicy = "verified_only"
	// SyncAllowUnreviewed additionally ingests unreviewed legacy profiles. It
	// is a deliberate, catalog-wide migration switch; candidate, needs_review
	// and rejected profiles are never ingested.
	SyncAllowUnreviewed ProfileSyncPolicy = "allow_unreviewed"
)

// EvidenceType classifies why a profile is believed to belong to an artist.
type EvidenceType string

const (
	// EvidenceNameMatch and EvidenceCredit are informational only: identical
	// names and release/track credits do not prove identity or membership.
	EvidenceNameMatch EvidenceType = "name_match"
	EvidenceCredit    EvidenceType = "credit"

	EvidenceSpotifyProfile EvidenceType = "spotify_profile"
	EvidenceMusicBrainz    EvidenceType = "musicbrainz"
	EvidenceWikidata       EvidenceType = "wikidata"
	EvidenceWikipedia      EvidenceType = "wikipedia"
	EvidenceOfficialSite   EvidenceType = "official_site"
	EvidenceLabel          EvidenceType = "label"
	EvidencePress          EvidenceType = "press"
	EvidenceSocial         EvidenceType = "social"
)

const (
	maxRationaleLength = 1000
	maxEvidenceURL     = 2048
	maxEvidenceItems   = 20
)

// Evidence is one human-readable reason supporting a review decision.
type Evidence struct {
	Type      EvidenceType
	URL       string
	ReleaseID string
	TrackID   string
	Rationale string
}

// SpotifyProfile is the editorial metadata of one Spotify profile. Timestamps
// are RFC 3339 UTC strings; they stay empty when unknown rather than invented.
type SpotifyProfile struct {
	ID            string
	Status        ProfileStatus
	Evidence      []Evidence
	ReviewedAt    string
	Reviewer      string
	LastCheckedAt string
}

func IsKnownProfileStatus(status ProfileStatus) bool {
	switch status {
	case ProfileUnreviewed, ProfileCandidate, ProfileVerified, ProfileRejected, ProfileNeedsReview:
		return true
	default:
		return false
	}
}

func IsKnownProfileSyncPolicy(policy ProfileSyncPolicy) bool {
	switch policy {
	case "", SyncVerifiedOnly, SyncAllowUnreviewed:
		return true
	default:
		return false
	}
}

func isKnownEvidenceType(t EvidenceType) bool {
	switch t {
	case EvidenceNameMatch, EvidenceCredit, EvidenceSpotifyProfile, EvidenceMusicBrainz, EvidenceWikidata,
		EvidenceWikipedia, EvidenceOfficialSite, EvidenceLabel, EvidencePress, EvidenceSocial:
		return true
	default:
		return false
	}
}

// Independent reports whether the evidence can support identity on its own.
// Name matches and credits never can.
func (e Evidence) Independent() bool {
	return isKnownEvidenceType(e.Type) && e.Type != EvidenceNameMatch && e.Type != EvidenceCredit
}

func (e Evidence) key() string {
	return strings.Join([]string{string(e.Type), e.URL, e.ReleaseID, e.TrackID}, "\x00")
}

// ProfileFor returns the explicit review metadata of a Spotify ID.
func (a Artist) ProfileFor(id string) (SpotifyProfile, bool) {
	for _, profile := range a.SpotifyProfiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return SpotifyProfile{}, false
}

// StatusOf returns the review state of a Spotify ID; linked IDs without
// metadata are unreviewed.
func (a Artist) StatusOf(id string) ProfileStatus {
	if profile, ok := a.ProfileFor(id); ok && profile.Status != "" {
		return profile.Status
	}
	return ProfileUnreviewed
}

// SyncableSpotifyIDs returns the linked IDs that policy allows to be ingested.
func (a Artist) SyncableSpotifyIDs(policy ProfileSyncPolicy) []string {
	ids := make([]string, 0, 1+len(a.SpotifyIDs))
	for _, id := range a.AllSpotifyIDs() {
		if ProfileSyncAllowed(a.StatusOf(id), policy) {
			ids = append(ids, id)
		}
	}
	return ids
}

// ProfileSyncAllowed is the single gate deciding whether a profile may be ingested.
func ProfileSyncAllowed(status ProfileStatus, policy ProfileSyncPolicy) bool {
	switch status {
	case ProfileVerified:
		return true
	case ProfileUnreviewed:
		return policy == SyncAllowUnreviewed
	default:
		return false
	}
}

// UnlinkSpotifyIDs removes every linked ID. Rejections are kept so the
// decision keeps blocking the profile; other review metadata would describe
// profiles that are no longer linked, so it is dropped.
func (a *Artist) UnlinkSpotifyIDs() {
	kept := a.SpotifyProfiles[:0:0]
	for _, profile := range a.SpotifyProfiles {
		if profile.Status == ProfileRejected {
			kept = append(kept, profile)
		}
	}
	a.SpotifyID = ""
	a.SpotifyIDs = nil
	a.SpotifyProfiles = kept
	if len(kept) == 0 {
		a.SpotifyProfiles = nil
	}
}

// ProfileOwner finds the artist that links or tracks a Spotify ID (any status).
func (c EditorialCatalog) ProfileOwner(id string) (slug string, status ProfileStatus, ok bool) {
	for _, artist := range c.Artists {
		if artist.HasSpotifyID(id) {
			return artist.Slug, artist.StatusOf(id), true
		}
		if profile, found := artist.ProfileFor(id); found {
			return artist.Slug, profile.Status, true
		}
	}
	return "", "", false
}

// IsRejectedSpotifyID reports whether any artist has rejected the profile.
func (c EditorialCatalog) IsRejectedSpotifyID(id string) bool {
	_, status, ok := c.ProfileOwner(id)
	return ok && status == ProfileRejected
}

// ReviewRequest describes one requested change of a profile's review state.
type ReviewRequest struct {
	Status   ProfileStatus
	Evidence []Evidence
	// Manual marks a decision taken by a human reviewer. Automation (name
	// matching, resolve, discovery) must leave it false.
	Manual     bool
	Reviewer   string
	ReviewedAt time.Time
}

var (
	ErrInvalidTransition = errors.New("invalid profile transition")
)

var allowedTransitions = map[ProfileStatus][]ProfileStatus{
	ProfileUnreviewed:  {ProfileCandidate, ProfileVerified, ProfileRejected, ProfileNeedsReview},
	ProfileCandidate:   {ProfileVerified, ProfileRejected, ProfileNeedsReview},
	ProfileVerified:    {ProfileNeedsReview, ProfileRejected},
	ProfileNeedsReview: {ProfileCandidate, ProfileVerified, ProfileRejected},
	ProfileRejected:    {ProfileCandidate, ProfileNeedsReview, ProfileVerified},
}

// ReviewProfile applies req to current and returns the updated profile. It
// enforces the transition table, the human-only verified/rejected decisions
// and the rule that a rejection can only be lifted manually, with new
// independent evidence.
func ReviewProfile(current SpotifyProfile, req ReviewRequest) (SpotifyProfile, error) {
	from := current.Status
	if from == "" {
		from = ProfileUnreviewed
	}
	to := req.Status
	if !IsKnownProfileStatus(to) {
		return current, fmt.Errorf("%w: unknown status %q", ErrInvalidTransition, to)
	}

	if from != to {
		allowed := false
		for _, next := range allowedTransitions[from] {
			if next == to {
				allowed = true
				break
			}
		}
		if !allowed {
			return current, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
		}
	}

	known := map[string]struct{}{}
	for _, evidence := range current.Evidence {
		known[evidence.key()] = struct{}{}
	}
	newEvidence := make([]Evidence, 0, len(req.Evidence))
	newIndependent := 0
	for _, evidence := range req.Evidence {
		if _, ok := known[evidence.key()]; ok {
			continue
		}
		known[evidence.key()] = struct{}{}
		newEvidence = append(newEvidence, evidence)
		if evidence.Independent() {
			newIndependent++
		}
	}

	if (to == ProfileVerified || to == ProfileRejected) && from != to {
		if !req.Manual {
			return current, fmt.Errorf("%w: %s requires a manual review", ErrInvalidTransition, to)
		}
		if strings.TrimSpace(req.Reviewer) == "" || req.ReviewedAt.IsZero() {
			return current, fmt.Errorf("%w: %s requires reviewer and review time", ErrInvalidTransition, to)
		}
	}
	if from == ProfileRejected && to != ProfileRejected {
		if !req.Manual {
			return current, fmt.Errorf("%w: a rejected profile cannot be reapproved automatically", ErrInvalidTransition)
		}
		if newIndependent == 0 {
			return current, fmt.Errorf("%w: leaving rejected requires new independent evidence", ErrInvalidTransition)
		}
	}

	next := current
	next.Status = to
	next.Evidence = append(append([]Evidence(nil), current.Evidence...), newEvidence...)
	if from != to && (to == ProfileVerified || to == ProfileRejected) {
		next.Reviewer = strings.TrimSpace(req.Reviewer)
		next.ReviewedAt = req.ReviewedAt.UTC().Format(time.RFC3339)
	}
	if from != to && (to == ProfileUnreviewed || to == ProfileCandidate || to == ProfileNeedsReview) {
		// A new non-final state invalidates the previous decision's provenance.
		next.Reviewer = ""
		next.ReviewedAt = ""
	}
	if !req.ReviewedAt.IsZero() {
		next.LastCheckedAt = req.ReviewedAt.UTC().Format(time.RFC3339)
	}
	if to == ProfileVerified {
		independent := 0
		for _, evidence := range next.Evidence {
			if evidence.Independent() {
				independent++
			}
		}
		if independent == 0 {
			return current, fmt.Errorf("%w: verified requires at least one independent evidence item", ErrInvalidTransition)
		}
	}
	if to == ProfileRejected && len(next.Evidence) == 0 {
		return current, fmt.Errorf("%w: rejected requires at least one evidence item explaining why", ErrInvalidTransition)
	}

	return next, nil
}

func validateProfiles(prefix string, artist Artist) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	seen := map[string]int{}

	for i, profile := range artist.SpotifyProfiles {
		field := fmt.Sprintf("%s.spotify_profiles[%d]", prefix, i)
		add := func(sub, message string) {
			issues = append(issues, ValidationIssue{Field: field + sub, Message: message})
		}

		if !spotifyIDPattern.MatchString(profile.ID) {
			add(".id", "has invalid format")
		} else if previous, ok := seen[profile.ID]; ok {
			add(".id", fmt.Sprintf("duplicate Spotify profile; first seen at spotify_profiles[%d]", previous))
		} else {
			seen[profile.ID] = i
		}

		if !IsKnownProfileStatus(profile.Status) {
			add(".status", "is unknown")
		}
		linked := artist.HasSpotifyID(profile.ID)
		if profile.Status == ProfileRejected && linked {
			add(".status", "rejected profile must not be linked in spotify_id or spotify_ids")
		}
		if profile.Status == ProfileVerified && !linked {
			add(".status", "verified profile must be linked in spotify_id or spotify_ids")
		}

		for _, ts := range []struct{ name, value string }{{"reviewed_at", profile.ReviewedAt}, {"last_checked_at", profile.LastCheckedAt}} {
			if ts.value != "" && !isUTCTimestamp(ts.value) {
				add("."+ts.name, "must be an RFC 3339 UTC timestamp such as 2026-01-02T15:04:05Z")
			}
		}
		switch profile.Status {
		case ProfileVerified, ProfileRejected:
			if strings.TrimSpace(profile.Reviewer) == "" {
				add(".reviewer", "is required for verified and rejected profiles")
			}
			if profile.ReviewedAt == "" {
				add(".reviewed_at", "is required for verified and rejected profiles")
			}
		case ProfileUnreviewed:
			if profile.Reviewer != "" || profile.ReviewedAt != "" {
				add(".status", "unreviewed profile must not carry reviewer or reviewed_at")
			}
		}

		if len(profile.Evidence) > maxEvidenceItems {
			add(".evidence", fmt.Sprintf("must contain at most %d items", maxEvidenceItems))
		}
		independent := 0
		for j, evidence := range profile.Evidence {
			issues = append(issues, validateEvidence(fmt.Sprintf("%s.evidence[%d]", field, j), evidence)...)
			if evidence.Independent() {
				independent++
			}
		}
		if profile.Status == ProfileVerified && independent == 0 {
			add(".evidence", "verified profile needs at least one independent evidence item (name matches and credits are not enough)")
		}
		if profile.Status == ProfileRejected && len(profile.Evidence) == 0 {
			add(".evidence", "rejected profile needs at least one evidence item explaining why")
		}
	}

	return issues
}

func validateEvidence(prefix string, e Evidence) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	add := func(sub, message string) {
		issues = append(issues, ValidationIssue{Field: prefix + sub, Message: message})
	}

	if !isKnownEvidenceType(e.Type) {
		add(".type", "is unknown")
	}
	if strings.TrimSpace(e.Rationale) == "" {
		add(".rationale", "is required")
	} else if len(e.Rationale) > maxRationaleLength {
		add(".rationale", fmt.Sprintf("must be at most %d characters", maxRationaleLength))
	}
	if e.ReleaseID != "" && !spotifyIDPattern.MatchString(e.ReleaseID) {
		add(".release_id", "has invalid format")
	}
	if e.TrackID != "" && !spotifyIDPattern.MatchString(e.TrackID) {
		add(".track_id", "has invalid format")
	}
	if e.URL == "" {
		if e.Independent() && e.Type != EvidenceOfficialSite && e.Type != EvidenceLabel && e.Type != EvidencePress && e.Type != EvidenceSocial {
			add(".url", "is required for this evidence type")
		}
		if e.Type == EvidenceCredit && e.ReleaseID == "" && e.TrackID == "" {
			add(".release_id", "credit evidence needs a release_id, track_id or url")
		}
		return issues
	}
	if err := validateEvidenceURL(e.Type, e.URL); err != nil {
		add(".url", err.Error())
	}
	return issues
}

func validateEvidenceURL(t EvidenceType, raw string) error {
	if len(raw) > maxEvidenceURL {
		return fmt.Errorf("must be at most %d characters", maxEvidenceURL)
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return errors.New("must not contain whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("must be an https URL")
	}
	if u.User != nil {
		return errors.New("must not contain credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("must use a public host name")
	}
	if net.ParseIP(host) != nil {
		return errors.New("must use a host name, not an IP address")
	}
	if port := u.Port(); port != "" && port != "443" {
		return errors.New("must not use a custom port")
	}

	var allowed []string
	switch t {
	case EvidenceSpotifyProfile, EvidenceCredit:
		allowed = []string{"open.spotify.com"}
	case EvidenceMusicBrainz:
		allowed = []string{"musicbrainz.org"}
	case EvidenceWikidata:
		allowed = []string{"wikidata.org", "www.wikidata.org"}
	case EvidenceWikipedia:
		if !strings.HasSuffix(host, ".wikipedia.org") {
			return errors.New("must be on wikipedia.org")
		}
		return nil
	}
	if allowed != nil {
		for _, h := range allowed {
			if host == h {
				return nil
			}
		}
		return fmt.Errorf("host must be %s for %s evidence", strings.Join(allowed, " or "), t)
	}
	return nil
}

func isUTCTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return strings.HasSuffix(value, "Z") && parsed.UTC().Format(time.RFC3339) == value
}

// ValidateEvidence checks one evidence item outside of a catalog, for CLI input.
func ValidateEvidence(e Evidence) []ValidationIssue {
	return validateEvidence("evidence", e)
}

// Clone returns a copy that shares no slice with the original, so reviewing
// one profile can be abandoned half way without leaking into the source.
func (a Artist) Clone() Artist {
	a.SpotifyIDs = append([]string(nil), a.SpotifyIDs...)
	a.Genres = append([]string(nil), a.Genres...)
	a.Roles = append([]Category(nil), a.Roles...)
	a.Aliases = append([]string(nil), a.Aliases...)
	profiles := make([]SpotifyProfile, len(a.SpotifyProfiles))
	for i, p := range a.SpotifyProfiles {
		p.Evidence = append([]Evidence(nil), p.Evidence...)
		profiles[i] = p
	}
	a.SpotifyProfiles = profiles
	if len(a.SpotifyIDs) == 0 {
		a.SpotifyIDs = nil
	}
	if len(a.SpotifyProfiles) == 0 {
		a.SpotifyProfiles = nil
	}
	if len(a.Genres) == 0 {
		a.Genres = nil
	}
	if len(a.Roles) == 0 {
		a.Roles = nil
	}
	if len(a.Aliases) == 0 {
		a.Aliases = nil
	}
	return a
}
