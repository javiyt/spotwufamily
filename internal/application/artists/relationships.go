package artists

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// RelationshipChange describes the outcome of a mutating operation. With
// DryRun set the catalog is validated and the change reported but never saved.
type RelationshipChange struct {
	DryRun bool
	Before *catalog.Relationship
	After  *catalog.Relationship
	// Rewritten lists the edges whose endpoints changed (slug renames).
	Rewritten []catalog.RelationshipKey
}

// ManageRelationships holds the use cases over artist relationships. It only
// depends on the catalog store port, so it is usable from any adapter.
type ManageRelationships struct {
	store CatalogStore
	now   func() time.Time
}

func NewManageRelationships(store CatalogStore) ManageRelationships {
	return ManageRelationships{store: store, now: time.Now}
}

// WithClock overrides the review clock; tests use it for deterministic dates.
func (m ManageRelationships) WithClock(now func() time.Time) ManageRelationships {
	m.now = now
	return m
}

// Add records a new relationship. An existing edge with the same key is an
// error; use Update to change it.
func (m ManageRelationships) Add(ctx context.Context, path string, rel catalog.Relationship, dryRun bool) (RelationshipChange, error) {
	return m.mutate(ctx, path, dryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, RelationshipChange, error) {
		rel = m.stamp(rel.Normalized())
		if c.FindRelationship(rel.Key()) >= 0 {
			return c, RelationshipChange{}, fmt.Errorf("%w: %s", catalog.ErrRelationshipExists, rel.Key())
		}
		c.Relationships = append(c.Relationships, rel)
		catalog.SortRelationships(c.Relationships)
		return c, RelationshipChange{After: &rel}, nil
	})
}

// Update replaces the edge identified by key with rel. The type and endpoints
// of rel may differ from key (for example to correct a direction).
func (m ManageRelationships) Update(ctx context.Context, path string, key catalog.RelationshipKey, rel catalog.Relationship, dryRun bool) (RelationshipChange, error) {
	return m.mutate(ctx, path, dryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, RelationshipChange, error) {
		key = catalog.NewRelationshipKey(key.Type, key.From, key.To)
		index := c.FindRelationship(key)
		if index < 0 {
			return c, RelationshipChange{}, fmt.Errorf("%w: %s", catalog.ErrRelationshipNotFound, key)
		}
		before := c.Relationships[index]
		rel = m.stamp(rel.Normalized())
		if rel.Key() != key && c.FindRelationship(rel.Key()) >= 0 {
			return c, RelationshipChange{}, fmt.Errorf("%w: %s", catalog.ErrRelationshipExists, rel.Key())
		}
		c.Relationships[index] = rel
		catalog.SortRelationships(c.Relationships)
		return c, RelationshipChange{Before: &before, After: &rel}, nil
	})
}

// Remove deletes one edge. Artists are never touched.
func (m ManageRelationships) Remove(ctx context.Context, path string, key catalog.RelationshipKey, dryRun bool) (RelationshipChange, error) {
	return m.mutate(ctx, path, dryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, RelationshipChange, error) {
		key = catalog.NewRelationshipKey(key.Type, key.From, key.To)
		index := c.FindRelationship(key)
		if index < 0 {
			return c, RelationshipChange{}, fmt.Errorf("%w: %s", catalog.ErrRelationshipNotFound, key)
		}
		before := c.Relationships[index]
		c.Relationships = append(c.Relationships[:index], c.Relationships[index+1:]...)
		return c, RelationshipChange{Before: &before}, nil
	})
}

// RenameArtist renames a slug and rewrites every relationship that points to it.
func (m ManageRelationships) RenameArtist(ctx context.Context, path, oldSlug, newSlug string, dryRun bool) (RelationshipChange, error) {
	return m.mutate(ctx, path, dryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, RelationshipChange, error) {
		affected := c.RelationshipsFor(oldSlug)
		next, err := c.RenameArtistSlug(oldSlug, newSlug)
		if err != nil {
			return c, RelationshipChange{}, err
		}
		change := RelationshipChange{}
		for _, rel := range affected {
			change.Rewritten = append(change.Rewritten, rel.Key())
		}
		return next, change, nil
	})
}

// RemoveArtist deletes an artist only when no relationship references it.
func (m ManageRelationships) RemoveArtist(ctx context.Context, path, slug string, dryRun bool) (RelationshipChange, error) {
	return m.mutate(ctx, path, dryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, RelationshipChange, error) {
		next, err := c.RemoveArtist(slug)
		return next, RelationshipChange{}, err
	})
}

// stamp fills the review time of a verified edge when the caller left it empty.
func (m ManageRelationships) stamp(rel catalog.Relationship) catalog.Relationship {
	if rel.Status == catalog.RelationshipVerified && rel.ReviewedAt == "" {
		rel.ReviewedAt = m.now().UTC().Format(time.RFC3339)
	}
	return rel
}

func (m ManageRelationships) mutate(
	ctx context.Context,
	path string,
	dryRun bool,
	apply func(catalog.EditorialCatalog) (catalog.EditorialCatalog, RelationshipChange, error),
) (RelationshipChange, error) {
	current, err := m.store.Load(ctx, path)
	if err != nil {
		return RelationshipChange{}, fmt.Errorf("load artist catalog: %w", err)
	}

	next, change, err := apply(current.Clone())
	if err != nil {
		return RelationshipChange{}, err
	}
	change.DryRun = dryRun

	// Only issues introduced by this change block it: a catalog that already
	// has unrelated problems can still be edited safely.
	if introduced := newIssues(catalog.ValidateEditorialCatalog(current), catalog.ValidateEditorialCatalog(next)); len(introduced) > 0 {
		return RelationshipChange{}, &InvalidRelationshipError{Issues: introduced}
	}
	if dryRun {
		return change, nil
	}
	if err := m.store.Save(ctx, path, next); err != nil {
		return RelationshipChange{}, fmt.Errorf("save artist catalog: %w", err)
	}

	return change, nil
}

// InvalidRelationshipError lists the validation issues a change would introduce.
type InvalidRelationshipError struct {
	Issues []catalog.ValidationIssue
}

func (e *InvalidRelationshipError) Error() string {
	message := "change rejected, it would make the catalog invalid:"
	for _, issue := range e.Issues {
		message += "\n  - " + issue.Error()
	}
	return message
}

func newIssues(before, after []catalog.ValidationIssue) []catalog.ValidationIssue {
	known := make(map[catalog.ValidationIssue]struct{}, len(before))
	for _, issue := range before {
		known[issue] = struct{}{}
	}
	introduced := make([]catalog.ValidationIssue, 0)
	for _, issue := range after {
		if _, ok := known[issue]; !ok {
			introduced = append(introduced, issue)
		}
	}
	return introduced
}

// IsInvalidRelationship reports whether err carries validation issues.
func IsInvalidRelationship(err error) bool {
	var invalid *InvalidRelationshipError
	return errors.As(err, &invalid)
}

// RelationshipFilter narrows List. Zero values match everything.
type RelationshipFilter struct {
	Types []catalog.RelationshipType
	Slug  string
	// EstablishedOnly keeps verified relationships, the only ones that may be
	// presented as fact.
	EstablishedOnly bool
}

// RelationshipEntry is a relationship joined with the artist on the other side.
type RelationshipEntry struct {
	Artist       catalog.Artist
	Relationship catalog.Relationship
}

// QueryRelationships answers reads without touching the store's write path.
type QueryRelationships struct {
	store CatalogStore
}

func NewQueryRelationships(store CatalogStore) QueryRelationships {
	return QueryRelationships{store: store}
}

func (q QueryRelationships) load(ctx context.Context, path string) (catalog.EditorialCatalog, error) {
	c, err := q.store.Load(ctx, path)
	if err != nil {
		return catalog.EditorialCatalog{}, fmt.Errorf("load artist catalog: %w", err)
	}
	return c, nil
}

// List returns relationships in canonical order.
func (q QueryRelationships) List(ctx context.Context, path string, filter RelationshipFilter) ([]catalog.Relationship, error) {
	c, err := q.load(ctx, path)
	if err != nil {
		return nil, err
	}
	return ListRelationships(c, filter), nil
}

// MembersOf lists the artists that are members of a group (reverse lookup),
// ordered by slug. Former members are included and report Former().
func (q QueryRelationships) MembersOf(ctx context.Context, path, group string, establishedOnly bool) ([]RelationshipEntry, error) {
	c, err := q.load(ctx, path)
	if err != nil {
		return nil, err
	}
	return MembersOf(c, group, establishedOnly)
}

// GroupsFor lists the groups an artist belongs to.
func (q QueryRelationships) GroupsFor(ctx context.Context, path, artist string, establishedOnly bool) ([]RelationshipEntry, error) {
	c, err := q.load(ctx, path)
	if err != nil {
		return nil, err
	}
	return GroupsFor(c, artist, establishedOnly)
}

func ListRelationships(c catalog.EditorialCatalog, filter RelationshipFilter) []catalog.Relationship {
	out := make([]catalog.Relationship, 0)
	for _, rel := range c.Relationships {
		if filter.Slug != "" && !rel.Involves(filter.Slug) {
			continue
		}
		if len(filter.Types) > 0 && !hasType(filter.Types, rel.Type) {
			continue
		}
		if filter.EstablishedOnly && !rel.Established() {
			continue
		}
		out = append(out, rel.Normalized())
	}
	catalog.SortRelationships(out)
	return out
}

func MembersOf(c catalog.EditorialCatalog, group string, establishedOnly bool) ([]RelationshipEntry, error) {
	if c.FindArtist(group) < 0 {
		return nil, fmt.Errorf("%w: %q", catalog.ErrUnknownArtist, group)
	}
	return joinEntries(c, establishedOnly, func(rel catalog.Relationship) (string, bool) {
		return rel.From, rel.Type == catalog.RelMemberOf && rel.To == group
	}), nil
}

func GroupsFor(c catalog.EditorialCatalog, artist string, establishedOnly bool) ([]RelationshipEntry, error) {
	if c.FindArtist(artist) < 0 {
		return nil, fmt.Errorf("%w: %q", catalog.ErrUnknownArtist, artist)
	}
	return joinEntries(c, establishedOnly, func(rel catalog.Relationship) (string, bool) {
		return rel.To, rel.Type == catalog.RelMemberOf && rel.From == artist
	}), nil
}

func joinEntries(c catalog.EditorialCatalog, establishedOnly bool, pick func(catalog.Relationship) (string, bool)) []RelationshipEntry {
	entries := make([]RelationshipEntry, 0)
	for _, rel := range c.Relationships {
		slug, ok := pick(rel)
		if !ok || (establishedOnly && !rel.Established()) {
			continue
		}
		if index := c.FindArtist(slug); index >= 0 {
			entries = append(entries, RelationshipEntry{Artist: c.Artists[index], Relationship: rel})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Artist.Slug < entries[j].Artist.Slug })
	return entries
}

func hasType(types []catalog.RelationshipType, t catalog.RelationshipType) bool {
	for _, candidate := range types {
		if candidate == t {
			return true
		}
	}
	return false
}
