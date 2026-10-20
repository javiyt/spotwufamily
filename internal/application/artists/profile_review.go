package artists

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type ReviewProfileInput struct {
	Slug       string
	SpotifyID  string
	Status     catalog.ProfileStatus
	Evidence   []catalog.Evidence
	Reviewer   string
	ReviewedAt time.Time
	// Link adds the profile to the artist's linked IDs when it is not linked yet.
	Link   bool
	DryRun bool
}

type ReviewProfileResult struct {
	Slug      string
	SpotifyID string
	From      catalog.ProfileStatus
	To        catalog.ProfileStatus
	Linked    bool
	Unlinked  bool
	Saved     bool
}

type ReviewProfile struct {
	store CatalogStore
}

func NewReviewProfile(store CatalogStore) ReviewProfile {
	return ReviewProfile{store: store}
}

// Run applies a manual review decision. It is the only path that can verify or
// reject a profile, or lift a rejection.
func (r ReviewProfile) Run(ctx context.Context, path string, in ReviewProfileInput) (ReviewProfileResult, error) {
	c, err := r.store.Load(ctx, path)
	if err != nil {
		return ReviewProfileResult{}, fmt.Errorf("load artist catalog: %w", err)
	}

	index := -1
	for i := range c.Artists {
		if c.Artists[i].Slug == in.Slug {
			index = i
			break
		}
	}
	if index < 0 {
		return ReviewProfileResult{}, fmt.Errorf("artist %q not found", in.Slug)
	}
	artist := &c.Artists[index]

	if owner, _, ok := c.ProfileOwner(in.SpotifyID); ok && owner != artist.Slug {
		return ReviewProfileResult{}, fmt.Errorf("Spotify ID %s already belongs to %s", in.SpotifyID, owner)
	}

	current, tracked := artist.ProfileFor(in.SpotifyID)
	if !tracked {
		current = catalog.SpotifyProfile{ID: in.SpotifyID, Status: catalog.ProfileUnreviewed}
		if !artist.HasSpotifyID(in.SpotifyID) && !in.Link && in.Status != catalog.ProfileRejected {
			return ReviewProfileResult{}, fmt.Errorf("Spotify ID %s is not linked to %s; use --link to add it", in.SpotifyID, artist.Slug)
		}
	}

	result := ReviewProfileResult{Slug: artist.Slug, SpotifyID: in.SpotifyID, From: current.Status}
	updated, err := catalog.ReviewProfile(current, catalog.ReviewRequest{
		Status:     in.Status,
		Evidence:   in.Evidence,
		Manual:     true,
		Reviewer:   in.Reviewer,
		ReviewedAt: in.ReviewedAt,
	})
	if err != nil {
		return result, err
	}
	result.To = updated.Status

	if updated.Status == catalog.ProfileRejected {
		result.Unlinked = unlinkSpotifyID(artist, in.SpotifyID)
		if len(artist.AllSpotifyIDs()) == 0 {
			// An enabled artist must keep a Spotify ID; rejecting the last one disables it.
			artist.Enabled = false
		}
	} else if !artist.HasSpotifyID(in.SpotifyID) {
		linkSpotifyID(artist, in.SpotifyID)
		result.Linked = true
	}

	if tracked {
		for i := range artist.SpotifyProfiles {
			if artist.SpotifyProfiles[i].ID == in.SpotifyID {
				artist.SpotifyProfiles[i] = updated
			}
		}
	} else {
		artist.SpotifyProfiles = append(artist.SpotifyProfiles, updated)
	}

	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, issue := range issues {
			msgs = append(msgs, issue.Error())
		}
		return result, fmt.Errorf("reviewed catalog is invalid: %s", strings.Join(msgs, "; "))
	}
	if in.DryRun {
		return result, nil
	}
	if err := r.store.Save(ctx, path, c); err != nil {
		return result, fmt.Errorf("save artist catalog: %w", err)
	}
	result.Saved = true

	return result, nil
}

func linkSpotifyID(artist *catalog.Artist, id string) {
	if artist.SpotifyID == "" {
		artist.SpotifyID = id
		return
	}
	artist.SpotifyIDs = append(artist.SpotifyIDs, id)
}

func unlinkSpotifyID(artist *catalog.Artist, id string) bool {
	if !artist.HasSpotifyID(id) {
		return false
	}
	extras := make([]string, 0, len(artist.SpotifyIDs))
	for _, extra := range artist.SpotifyIDs {
		if extra != id {
			extras = append(extras, extra)
		}
	}
	if artist.SpotifyID == id {
		artist.SpotifyID = ""
		if len(extras) > 0 {
			artist.SpotifyID, extras = extras[0], extras[1:]
		}
	}
	artist.SpotifyIDs = extras
	if len(extras) == 0 {
		artist.SpotifyIDs = nil
	}
	return true
}
