package artists

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// ErrInvalidMapping marks a mapping request the catalog rules refuse.
var ErrInvalidMapping = errors.New("invalid Spotify profile mapping")

// MappingAction is one human decision about a Spotify profile of an artist.
type MappingAction string

const (
	// MapAcceptPrimary links the profile as the artist's primary ID. The
	// previous primary stays linked as a secondary.
	MapAcceptPrimary MappingAction = "accept_primary"
	// MapAddSecondary links the profile as an additional ID.
	MapAddSecondary MappingAction = "add_secondary"
	// MapReject records that the profile is not this artist and unlinks it.
	MapReject MappingAction = "reject"
	// MapInconclusive keeps the profile out of sync (needs_review) with a note.
	MapInconclusive MappingAction = "inconclusive"
	// MapQuarantine unlinks a profile but keeps it tracked as needs_review.
	MapQuarantine MappingAction = "quarantine"
	// MapRemove unlinks a profile and forgets its review metadata. Rejections
	// are never forgotten.
	MapRemove MappingAction = "remove"
	// MapReorderPrimary makes an already linked ID the primary one.
	MapReorderPrimary MappingAction = "reorder_primary"
)

// IsKnownMappingAction reports whether a is a supported action.
func IsKnownMappingAction(a MappingAction) bool {
	switch a {
	case MapAcceptPrimary, MapAddSecondary, MapReject, MapInconclusive, MapQuarantine, MapRemove, MapReorderPrimary:
		return true
	}
	return false
}

// ChangesMapping reports whether the action alters which IDs are linked or
// their order, which is what can later orphan synced rows.
func (a MappingAction) ChangesMapping() bool {
	switch a {
	case MapInconclusive:
		return false
	}
	return true
}

// MappingRequest is the single input shared by the CLI and the admin UI.
type MappingRequest struct {
	Slug      string
	SpotifyID string
	Action    MappingAction
	Reviewer  string
	At        time.Time
	// Note explains the decision; it is required to reject and to mark a
	// profile inconclusive or quarantined.
	Note string
	// Evidence is the human-supplied source material for the decision.
	Evidence []catalog.Evidence
	// Verify promotes an accepted profile to verified. It needs independent
	// evidence; without it the profile is only linked as a candidate and sync
	// keeps ignoring it.
	Verify bool
	// OverrideReason records an audited override of a failed or missing
	// automated check. It never replaces the independent evidence a verified
	// profile needs.
	OverrideReason string
	// Enable turns the artist on, and only when the profile ends verified.
	Enable bool
}

// MappingResult describes what ApplyMapping changed.
type MappingResult struct {
	Slug      string
	SpotifyID string
	Action    MappingAction
	From      catalog.ProfileStatus
	To        catalog.ProfileStatus
	Linked    bool
	Unlinked  bool
	// Demoted is the former primary that became a secondary.
	Demoted  string
	Enabled  bool
	Disabled bool
}

// ApplyMapping applies req to c in place. It enforces the review rules shared
// by every entry point: only a human can verify or reject, verified needs
// independent evidence, an ID owned by another artist is never taken over, and
// nothing is enabled unless the profile ends verified. The caller validates the
// whole catalog and persists it.
func ApplyMapping(c *catalog.EditorialCatalog, req MappingRequest) (res MappingResult, err error) {
	res = MappingResult{Slug: req.Slug, SpotifyID: req.SpotifyID, Action: req.Action}
	fail := func(format string, args ...any) (MappingResult, error) {
		return res, fmt.Errorf("%w: %s", ErrInvalidMapping, fmt.Sprintf(format, args...))
	}

	if !IsKnownMappingAction(req.Action) {
		return fail("unknown action %q", req.Action)
	}
	if !catalog.IsSpotifyID(req.SpotifyID) {
		return fail("%q is not a Spotify artist ID", req.SpotifyID)
	}
	if strings.TrimSpace(req.Reviewer) == "" || req.At.IsZero() {
		return fail("a reviewer and a review time are required")
	}
	index := c.FindArtist(req.Slug)
	if index < 0 {
		return fail("artist %q not found", req.Slug)
	}
	// Work on a deep copy and publish it only on success, so a request that
	// fails half way leaves the catalog (and any shallow clone of it) intact.
	working := c.Artists[index].Clone()
	artist := &working
	defer func() {
		if err == nil {
			c.Artists[index] = working
		}
	}()

	// A shared name is not identity: an ID tracked by another artist is never
	// moved or merged here, whatever its status.
	if owner, _, ok := c.ProfileOwner(req.SpotifyID); ok && owner != artist.Slug {
		return fail("Spotify ID %s already belongs to %s; resolve that mapping first", req.SpotifyID, owner)
	}

	current, tracked := artist.ProfileFor(req.SpotifyID)
	if !tracked {
		current = catalog.SpotifyProfile{ID: req.SpotifyID, Status: catalog.ProfileUnreviewed}
	}
	linked := artist.HasSpotifyID(req.SpotifyID)
	res.From = artist.StatusOf(req.SpotifyID)
	res.To = res.From
	note := strings.TrimSpace(req.Note)

	review := func(status catalog.ProfileStatus, evidence []catalog.Evidence) (catalog.SpotifyProfile, error) {
		updated, err := catalog.ReviewProfile(current, catalog.ReviewRequest{
			Status: status, Evidence: evidence, Manual: true, Reviewer: req.Reviewer, ReviewedAt: req.At,
		})
		if err != nil {
			return current, fmt.Errorf("%w: %w", ErrInvalidMapping, err)
		}
		return updated, nil
	}
	store := func(p catalog.SpotifyProfile) {
		for i := range artist.SpotifyProfiles {
			if artist.SpotifyProfiles[i].ID == req.SpotifyID {
				artist.SpotifyProfiles[i] = p
				return
			}
		}
		artist.SpotifyProfiles = append(artist.SpotifyProfiles, p)
	}

	switch req.Action {
	case MapAcceptPrimary, MapAddSecondary:
		if linked && req.Action == MapAddSecondary && artist.SpotifyID == req.SpotifyID {
			return fail("%s is already the primary ID; use reorder instead", req.SpotifyID)
		}
		if linked && req.Action == MapAcceptPrimary && artist.SpotifyID == req.SpotifyID && !req.Verify && !tracked {
			return fail("%s is already the primary ID", req.SpotifyID)
		}
		target := catalog.ProfileCandidate
		switch {
		case req.Verify:
			target = catalog.ProfileVerified
		case current.Status == catalog.ProfileVerified:
			target = catalog.ProfileVerified
		}
		evidence := append([]catalog.Evidence(nil), req.Evidence...)
		if reason := strings.TrimSpace(req.OverrideReason); reason != "" {
			evidence = append(evidence, catalog.Evidence{Type: catalog.EvidenceNameMatch, Rationale: "Audited override by " + strings.TrimSpace(req.Reviewer) + ": " + reason})
		}
		updated, err := review(target, evidence)
		if err != nil {
			return res, err
		}
		store(updated)
		res.To = updated.Status

		if req.Action == MapAcceptPrimary {
			res.Linked = !linked
			res.Demoted = makePrimary(artist, req.SpotifyID)
		} else if !linked {
			linkSpotifyID(artist, req.SpotifyID)
			res.Linked = true
		}
		if req.Enable {
			if updated.Status != catalog.ProfileVerified {
				return fail("the artist can only be enabled once its profile is verified")
			}
			if !artist.Enabled {
				artist.Enabled = true
				res.Enabled = true
			}
		}

	case MapReject:
		if note == "" {
			return fail("rejecting needs a note explaining why")
		}
		evidence := append([]catalog.Evidence(nil), req.Evidence...)
		if len(evidence) == 0 {
			evidence = []catalog.Evidence{{Type: catalog.EvidenceSpotifyProfile, URL: catalog.SpotifyArtistURL(req.SpotifyID), Rationale: note}}
		} else if evidence[0].Rationale == "" {
			evidence[0].Rationale = note
		}
		updated, err := review(catalog.ProfileRejected, evidence)
		if err != nil {
			return res, err
		}
		store(updated)
		res.To = updated.Status
		res.Unlinked = unlinkSpotifyID(artist, req.SpotifyID)

	case MapInconclusive:
		if note == "" {
			return fail("marking a profile inconclusive needs a note")
		}
		updated, err := review(catalog.ProfileNeedsReview, []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "Inconclusive review by " + strings.TrimSpace(req.Reviewer) + ": " + note}})
		if err != nil {
			return res, err
		}
		store(updated)
		res.To = updated.Status

	case MapQuarantine:
		if note == "" {
			return fail("quarantining needs a note")
		}
		if !linked && !tracked {
			return fail("%s is not linked to or tracked by %s", req.SpotifyID, artist.Slug)
		}
		updated, err := review(catalog.ProfileNeedsReview, []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "Quarantined by " + strings.TrimSpace(req.Reviewer) + ": " + note}})
		if err != nil {
			return res, err
		}
		store(updated)
		res.To = updated.Status
		res.Unlinked = unlinkSpotifyID(artist, req.SpotifyID)

	case MapRemove:
		if !linked {
			return fail("%s is not linked to %s", req.SpotifyID, artist.Slug)
		}
		res.Unlinked = unlinkSpotifyID(artist, req.SpotifyID)
		if current.Status != catalog.ProfileRejected {
			dropProfile(artist, req.SpotifyID)
			res.To = ""
		}

	case MapReorderPrimary:
		if !linked {
			return fail("%s is not linked to %s", req.SpotifyID, artist.Slug)
		}
		if artist.SpotifyID == req.SpotifyID {
			return fail("%s is already the primary ID", req.SpotifyID)
		}
		res.Demoted = makePrimary(artist, req.SpotifyID)
	}

	if len(artist.AllSpotifyIDs()) == 0 && artist.Enabled {
		// An enabled artist must keep a Spotify ID.
		artist.Enabled = false
		res.Disabled = true
	}
	return res, nil
}

// makePrimary puts id first, keeping the former primary as the first secondary.
// It returns the demoted ID.
func makePrimary(artist *catalog.Artist, id string) string {
	if artist.SpotifyID == id {
		return ""
	}
	extras := make([]string, 0, len(artist.SpotifyIDs)+1)
	demoted := artist.SpotifyID
	if demoted != "" {
		extras = append(extras, demoted)
	}
	for _, extra := range artist.SpotifyIDs {
		if extra != id {
			extras = append(extras, extra)
		}
	}
	artist.SpotifyID = id
	artist.SpotifyIDs = extras
	if len(extras) == 0 {
		artist.SpotifyIDs = nil
	}
	return demoted
}

func dropProfile(artist *catalog.Artist, id string) {
	kept := make([]catalog.SpotifyProfile, 0, len(artist.SpotifyProfiles))
	for _, p := range artist.SpotifyProfiles {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	artist.SpotifyProfiles = kept
	if len(kept) == 0 {
		artist.SpotifyProfiles = nil
	}
}
