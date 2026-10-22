package artists

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// ChangeKind names one category of editorial change to an artist.
type ChangeKind string

const (
	ChangeAdded         ChangeKind = "added"
	ChangeRemoved       ChangeKind = "removed"
	ChangeIDsAdded      ChangeKind = "ids_added"
	ChangeIDsRemoved    ChangeKind = "ids_removed"
	ChangeIDsReordered  ChangeKind = "ids_reordered"
	ChangeVerified      ChangeKind = "verified"
	ChangeEnabled       ChangeKind = "enabled"
	ChangeRelationships ChangeKind = "relationships"
	ChangeMetadata      ChangeKind = "metadata"
)

// ArtistChange is the classified difference for one artist.
type ArtistChange struct {
	Slug           string
	Kinds          []ChangeKind
	NeedsReconcile bool
	NeedsSync      bool
}

// FingerprintCatalog builds the canonical applied-catalog summary. Artists are
// sorted by slug; Spotify ID order is preserved because it is semantic.
// Verified means every linked profile may be ingested under the sync policy.
func FingerprintCatalog(c catalog.EditorialCatalog) AppliedCatalog {
	rels := map[string][]string{}
	for _, rel := range c.Relationships {
		rel = normalizeRelationship(rel)
		raw, _ := json.Marshal(rel)
		sum := sha256.Sum256(raw)
		entry := fmt.Sprintf("%s:%s>%s#%s", rel.Type, rel.From, rel.To, hex.EncodeToString(sum[:4]))
		rels[rel.From] = append(rels[rel.From], entry)
		if rel.To != rel.From {
			rels[rel.To] = append(rels[rel.To], entry)
		}
	}

	artists := make([]ArtistFingerprint, 0, len(c.Artists))
	for _, a := range c.Artists {
		ids := a.AllSpotifyIDs()
		syncable := a.SyncableSpotifyIDs(c.ProfileSyncPolicy)
		r := rels[a.Slug]
		sort.Strings(r)
		if r == nil {
			r = []string{}
		}
		artists = append(artists, ArtistFingerprint{
			Slug:          a.Slug,
			SpotifyIDs:    ids,
			Verified:      len(ids) > 0 && len(syncable) == len(ids),
			Enabled:       a.Enabled && !a.Archived,
			Relationships: r,
			Metadata:      artistMetadataHash(a),
		})
	}
	sort.Slice(artists, func(i, j int) bool { return artists[i].Slug < artists[j].Slug })

	return AppliedCatalog{Digest: fingerprintsDigest(artists), Artists: artists}
}

// fingerprintsDigest is the digest of fingerprints already sorted by slug.
func fingerprintsDigest(artists []ArtistFingerprint) string {
	raw, _ := json.Marshal(artists)
	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

// MergeApplied returns prev with the fingerprints of slugs taken from used
// (dropped when used no longer has the artist) and the digest recomputed.
// Every other artist keeps its previously applied fingerprint. AppliedAt is
// left empty for the caller to set.
func MergeApplied(prev, used AppliedCatalog, slugs []string) AppliedCatalog {
	byUsed := map[string]ArtistFingerprint{}
	for _, a := range used.Artists {
		byUsed[a.Slug] = a
	}
	replace := map[string]bool{}
	for _, s := range slugs {
		replace[s] = true
	}
	out := make([]ArtistFingerprint, 0, len(prev.Artists)+len(slugs))
	for _, a := range prev.Artists {
		if !replace[a.Slug] {
			out = append(out, a)
		}
	}
	for s := range replace {
		if a, ok := byUsed[s]; ok {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })

	return AppliedCatalog{Digest: fingerprintsDigest(out), Artists: out}
}

func normalizeRelationship(rel catalog.Relationship) catalog.Relationship {
	if (rel.Type == catalog.RelCollaboratedWith || rel.Type == catalog.RelAssociatedWith) && rel.From > rel.To {
		rel.From, rel.To = rel.To, rel.From
	}

	return rel
}

func sortedStrings(in []string) []string {
	out := slices.Clone(in)
	if out == nil {
		out = []string{}
	}
	sort.Strings(out)

	return out
}

// Profile review state is not part of metadata: ID changes and verified state
// are fingerprinted separately and the review trail lives in the reviews log.
func artistMetadataHash(a catalog.Artist) string {
	roles := make([]string, 0, len(a.Roles))
	for _, r := range a.Roles {
		roles = append(roles, string(r))
	}
	raw, _ := json.Marshal(struct {
		Name, PublicName string
		Genres, Aliases  []string
		Category         catalog.Category
		Roles            []string
		Archived         bool
		EditorialOrder   int
		Notes            string
		ExternalURL      string
		ImageURL         string
		AddedAt          string
	}{
		a.Name, a.PublicName, sortedStrings(a.Genres), sortedStrings(a.Aliases), a.Category, sortedStrings(roles),
		a.Archived, a.EditorialOrder, a.Notes, a.ExternalURL, a.ImageURL, a.AddedAt,
	})
	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

// DiffCatalog classifies per-artist differences between the applied and the
// current catalog, sorted by slug. Reconcile and sync follow the editorial
// rules: only ID set/order, verified/enabled state and artist add/remove touch
// the database; relationship and metadata edits need neither.
func DiffCatalog(applied, current AppliedCatalog) []ArtistChange {
	old := map[string]ArtistFingerprint{}
	for _, a := range applied.Artists {
		old[a.Slug] = a
	}
	seen := map[string]bool{}
	changes := []ArtistChange{}

	for _, cur := range current.Artists {
		seen[cur.Slug] = true
		prev, ok := old[cur.Slug]
		if !ok {
			ch := ArtistChange{Slug: cur.Slug, Kinds: []ChangeKind{ChangeAdded}}
			if len(cur.SpotifyIDs) > 0 {
				ch.NeedsReconcile, ch.NeedsSync = true, true
			}
			changes = append(changes, ch)
			continue
		}
		if ch, changed := diffFingerprints(prev, cur); changed {
			changes = append(changes, ch)
		}
	}
	for _, prev := range applied.Artists {
		if !seen[prev.Slug] {
			changes = append(changes, ArtistChange{
				Slug: prev.Slug, Kinds: []ChangeKind{ChangeRemoved},
				NeedsReconcile: true, NeedsSync: len(prev.SpotifyIDs) > 0,
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Slug < changes[j].Slug })

	return changes
}

func diffFingerprints(prev, cur ArtistFingerprint) (ArtistChange, bool) {
	ch := ArtistChange{Slug: cur.Slug}
	if !slices.Equal(prev.SpotifyIDs, cur.SpotifyIDs) {
		added := slices.ContainsFunc(cur.SpotifyIDs, func(id string) bool { return !slices.Contains(prev.SpotifyIDs, id) })
		removed := slices.ContainsFunc(prev.SpotifyIDs, func(id string) bool { return !slices.Contains(cur.SpotifyIDs, id) })
		if added {
			ch.Kinds = append(ch.Kinds, ChangeIDsAdded)
		}
		if removed {
			ch.Kinds = append(ch.Kinds, ChangeIDsRemoved)
		}
		if !added && !removed {
			ch.Kinds = append(ch.Kinds, ChangeIDsReordered)
		}
		ch.NeedsReconcile, ch.NeedsSync = true, true
	}
	if prev.Verified != cur.Verified {
		ch.Kinds = append(ch.Kinds, ChangeVerified)
		ch.NeedsReconcile, ch.NeedsSync = true, true
	}
	if prev.Enabled != cur.Enabled {
		ch.Kinds = append(ch.Kinds, ChangeEnabled)
		ch.NeedsReconcile, ch.NeedsSync = true, true
	}
	if !slices.Equal(prev.Relationships, cur.Relationships) {
		ch.Kinds = append(ch.Kinds, ChangeRelationships)
	}
	if prev.Metadata != cur.Metadata {
		ch.Kinds = append(ch.Kinds, ChangeMetadata)
	}

	return ch, len(ch.Kinds) > 0
}
