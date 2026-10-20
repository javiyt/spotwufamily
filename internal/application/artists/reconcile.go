package artists

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// AssociationKind tells whether an association links a configured artist to an
// album or to a track.
type AssociationKind string

const (
	AssociationAlbum AssociationKind = "album"
	AssociationTrack AssociationKind = "track"
)

// Association is one active configured_artists -> album/track link together
// with the evidence needed to judge it: the Spotify artist IDs credited on the
// entity and the other configured artists that still reference it.
type Association struct {
	Kind              AssociationKind
	SpotifyID         string
	Name              string
	CreditedArtistIDs []string
	SharedWith        []string
}

// ReconcileStore is the persistence port for association reconciliation.
type ReconcileStore interface {
	// ActiveAssociations lists the active album and track associations of a
	// configured artist.
	ActiveAssociations(ctx context.Context, slug string) ([]Association, error)
	// RetireAssociations soft-retires associations of one configured artist only:
	// active=0 and missing_since=retiredAt. Shared Spotify entities are untouched.
	RetireAssociations(ctx context.Context, slug string, albumIDs, trackIDs []string, retiredAt string) error
	// RestoreAssociations reactivates the associations that a previous
	// RetireAssociations call retired with the same retiredAt.
	RestoreAssociations(ctx context.Context, slug, retiredAt string) (albums, tracks int, err error)
}

// ReconcileClass is the verdict for one association.
type ReconcileClass string

const (
	// ReconcileSupported: an entity credit points at a currently linked profile.
	ReconcileSupported ReconcileClass = "supported"
	// ReconcileOrphan: credits exist but none points at a linked profile (or the
	// artist has no linked profile at all).
	ReconcileOrphan ReconcileClass = "orphan"
	// ReconcileUnverifiable: no credits are stored, so nothing can be proven. It
	// is reported and kept.
	ReconcileUnverifiable ReconcileClass = "unverifiable"
)

type ReconcileInput struct {
	CatalogPath string
	Slug        string
	// Apply retires orphan associations. Without it the run is a dry run.
	Apply bool
	// RestoreRetiredAt reverts a previous Apply identified by its retired_at.
	RestoreRetiredAt string
	Now              time.Time
}

type ReconcileItem struct {
	Kind              AssociationKind `json:"kind"`
	SpotifyID         string          `json:"spotify_id"`
	Name              string          `json:"name"`
	Class             ReconcileClass  `json:"class"`
	CreditedArtistIDs []string        `json:"credited_artist_ids,omitempty"`
	SharedWith        []string        `json:"shared_with,omitempty"`
}

type ReconcileCounts struct {
	Albums int `json:"albums"`
	Tracks int `json:"tracks"`
}

type ReconcileReport struct {
	Slug      string   `json:"slug"`
	LinkedIDs []string `json:"linked_ids"`
	DryRun    bool     `json:"dry_run"`
	// RetiredAt is the rollback token: pass it back as RestoreRetiredAt.
	RetiredAt string          `json:"retired_at,omitempty"`
	Before    ReconcileCounts `json:"before"`
	After     ReconcileCounts `json:"after"`
	Supported ReconcileCounts `json:"supported"`
	Orphan    ReconcileCounts `json:"orphan"`
	// Unverifiable associations are kept.
	Unverifiable ReconcileCounts `json:"unverifiable"`
	// SharedOrphans are orphan entities that other configured artists still
	// reference; they stay in the catalog.
	SharedOrphans ReconcileCounts `json:"shared_orphans"`
	// UnreferencedAfter are orphan entities no configured artist references once
	// retired. They are only reported: global rows are never deleted here.
	UnreferencedAfter ReconcileCounts  `json:"unreferenced_after"`
	Restored          *ReconcileCounts `json:"restored,omitempty"`
	Items             []ReconcileItem  `json:"items"`
}

type ReconcileArtist struct {
	catalog CatalogStore
	store   ReconcileStore
}

func NewReconcileArtist(catalog CatalogStore, store ReconcileStore) ReconcileArtist {
	return ReconcileArtist{catalog: catalog, store: store}
}

// Run classifies the active associations of one configured artist against its
// currently linked Spotify profiles, and optionally retires the orphans.
func (r ReconcileArtist) Run(ctx context.Context, in ReconcileInput) (ReconcileReport, error) {
	if in.Apply && in.RestoreRetiredAt != "" {
		return ReconcileReport{}, fmt.Errorf("apply and restore cannot be combined")
	}
	c, err := r.catalog.Load(ctx, in.CatalogPath)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("load artist catalog: %w", err)
	}
	var linked []string
	found := false
	for _, artist := range c.Artists {
		if artist.Slug == in.Slug {
			found = true
			linked = artist.AllSpotifyIDs()
			break
		}
	}
	if !found {
		return ReconcileReport{}, fmt.Errorf("artist %q not found", in.Slug)
	}

	report := ReconcileReport{Slug: in.Slug, LinkedIDs: linked, DryRun: !in.Apply && in.RestoreRetiredAt == ""}

	if in.RestoreRetiredAt != "" {
		albums, tracks, err := r.store.RestoreAssociations(ctx, in.Slug, in.RestoreRetiredAt)
		if err != nil {
			return report, fmt.Errorf("restore associations: %w", err)
		}
		report.Restored = &ReconcileCounts{Albums: albums, Tracks: tracks}
		report.RetiredAt = in.RestoreRetiredAt
		return report, nil
	}

	associations, err := r.store.ActiveAssociations(ctx, in.Slug)
	if err != nil {
		return report, fmt.Errorf("read associations: %w", err)
	}

	linkedSet := make(map[string]struct{}, len(linked))
	for _, id := range linked {
		linkedSet[id] = struct{}{}
	}

	var retireAlbums, retireTracks []string
	for _, a := range associations {
		item := ReconcileItem{
			Kind:              a.Kind,
			SpotifyID:         a.SpotifyID,
			Name:              a.Name,
			Class:             classifyAssociation(a, linkedSet),
			CreditedArtistIDs: sortedCopy(a.CreditedArtistIDs),
			SharedWith:        sortedCopy(a.SharedWith),
		}
		report.Items = append(report.Items, item)
		counts := countsFor(&report.Before, a.Kind)
		*counts++

		switch item.Class {
		case ReconcileSupported:
			*countsFor(&report.Supported, a.Kind)++
		case ReconcileUnverifiable:
			*countsFor(&report.Unverifiable, a.Kind)++
		case ReconcileOrphan:
			*countsFor(&report.Orphan, a.Kind)++
			if len(a.SharedWith) > 0 {
				*countsFor(&report.SharedOrphans, a.Kind)++
			} else {
				*countsFor(&report.UnreferencedAfter, a.Kind)++
			}
			if a.Kind == AssociationAlbum {
				retireAlbums = append(retireAlbums, a.SpotifyID)
			} else {
				retireTracks = append(retireTracks, a.SpotifyID)
			}
		}
	}
	sort.Slice(report.Items, func(i, j int) bool {
		a, b := report.Items[i], report.Items[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.SpotifyID < b.SpotifyID
	})

	report.After = ReconcileCounts{
		Albums: report.Before.Albums - len(retireAlbums),
		Tracks: report.Before.Tracks - len(retireTracks),
	}
	if report.DryRun || (len(retireAlbums) == 0 && len(retireTracks) == 0) {
		return report, nil
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	report.RetiredAt = now.UTC().Format(time.RFC3339)
	if err := r.store.RetireAssociations(ctx, in.Slug, retireAlbums, retireTracks, report.RetiredAt); err != nil {
		return report, fmt.Errorf("retire associations: %w", err)
	}

	return report, nil
}

func classifyAssociation(a Association, linked map[string]struct{}) ReconcileClass {
	for _, id := range a.CreditedArtistIDs {
		if _, ok := linked[id]; ok {
			return ReconcileSupported
		}
	}
	if len(linked) == 0 || len(a.CreditedArtistIDs) > 0 {
		return ReconcileOrphan
	}

	return ReconcileUnverifiable
}

func countsFor(c *ReconcileCounts, kind AssociationKind) *int {
	if kind == AssociationAlbum {
		return &c.Albums
	}

	return &c.Tracks
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)

	return out
}
