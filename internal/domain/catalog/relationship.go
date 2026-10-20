package catalog

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RelationshipType classifies an editorial edge between two catalog artists.
// Membership is only ever created by a reviewer: release or track credits and
// generic association never imply it.
type RelationshipType string

const (
	// RelMemberOf is directed: From is a member of the group To. Former
	// membership is the same type with an End date (or era) set.
	RelMemberOf RelationshipType = "member_of"
	// RelProducerFor is directed: From (a producer) produced for To.
	RelProducerFor RelationshipType = "producer_for"
	// RelCollectiveOrImprint is directed: From belongs to the collective,
	// label or imprint To without being an official group member.
	RelCollectiveOrImprint RelationshipType = "collective_or_imprint"
	// RelCollaboratedWith is undirected: the pair is stored once, ordered by slug.
	RelCollaboratedWith RelationshipType = "collaborated_with"
	// RelAssociatedWith is undirected and the weakest claim: a documented
	// affiliation that is neither membership nor collaboration.
	RelAssociatedWith RelationshipType = "associated_with"
)

// RelationshipStatus is the editorial review state of a relationship. Only
// verified relationships are presented as established facts.
type RelationshipStatus string

const (
	RelationshipProposed RelationshipStatus = "proposed"
	RelationshipVerified RelationshipStatus = "verified"
	RelationshipRejected RelationshipStatus = "rejected"
)

var (
	// ErrRelationshipNotFound is returned when an edge does not exist.
	ErrRelationshipNotFound = errors.New("relationship not found")
	// ErrRelationshipExists is returned when an edge is already recorded.
	ErrRelationshipExists = errors.New("relationship already exists")
	// ErrUnknownArtist is returned when a slug is not in the catalog.
	ErrUnknownArtist = errors.New("artist not found")
	// ErrArtistReferenced is returned when an artist is still used by a relationship.
	ErrArtistReferenced = errors.New("artist is referenced by relationships")

	partialDatePattern = regexp.MustCompile(`^\d{4}(?:-\d{2}(?:-\d{2})?)?$`)
)

const maxCommentaryLength = 1000

// Relationship is one typed, reviewable edge between two editorial slugs.
// Dates are partial ISO dates (YYYY, YYYY-MM or YYYY-MM-DD) and stay empty
// when unknown rather than invented.
type Relationship struct {
	Type RelationshipType
	From string
	To   string
	// Start and End bound the validity. End on a member_of makes it a former
	// membership. Era is free text for periods that cannot be dated exactly.
	Start      string
	End        string
	Era        string
	Evidence   []Evidence
	Status     RelationshipStatus
	Confidence CandidateConfidence
	Commentary string
	Reviewer   string
	ReviewedAt string
}

// RelationshipKey identifies an edge. Undirected types normalise the pair so
// A-B and B-A are the same edge.
type RelationshipKey struct {
	Type RelationshipType
	From string
	To   string
}

func (k RelationshipKey) String() string {
	return fmt.Sprintf("%s %s -> %s", k.Type, k.From, k.To)
}

func IsKnownRelationshipType(t RelationshipType) bool {
	switch t {
	case RelMemberOf, RelProducerFor, RelCollectiveOrImprint, RelCollaboratedWith, RelAssociatedWith:
		return true
	default:
		return false
	}
}

func IsKnownRelationshipStatus(s RelationshipStatus) bool {
	switch s {
	case RelationshipProposed, RelationshipVerified, RelationshipRejected:
		return true
	default:
		return false
	}
}

// Directed reports whether the type has a meaningful From -> To direction.
func (t RelationshipType) Directed() bool {
	return t != RelCollaboratedWith && t != RelAssociatedWith
}

// Key returns the canonical identity of the edge.
func (r Relationship) Key() RelationshipKey {
	return NewRelationshipKey(r.Type, r.From, r.To)
}

func NewRelationshipKey(t RelationshipType, from, to string) RelationshipKey {
	if !t.Directed() && from > to {
		from, to = to, from
	}
	return RelationshipKey{Type: t, From: from, To: to}
}

// Normalized returns the edge with undirected pairs ordered by slug.
func (r Relationship) Normalized() Relationship {
	key := r.Key()
	r.From, r.To = key.From, key.To
	return r
}

// Former reports whether a membership has an end date.
func (r Relationship) Former() bool {
	return r.Type == RelMemberOf && r.End != ""
}

// Involves reports whether slug is either endpoint.
func (r Relationship) Involves(slug string) bool {
	return r.From == slug || r.To == slug
}

// HasRole reports whether the artist is tagged with the editorial category.
func (a Artist) HasRole(category Category) bool {
	if a.Category == category {
		return true
	}
	for _, role := range a.Roles {
		if role == category {
			return true
		}
	}
	return false
}

// IsGroup reports whether the artist is a group identity. A group is an
// editorial artist carrying the affiliate_group category or role; an individual
// is never the same entity as the group they belong to.
func (a Artist) IsGroup() bool {
	return a.HasRole(CategoryAffiliateGroup)
}

// ValidateRelationships checks the edge set against the artists: referential
// integrity, self edges, duplicates (including undirected pairs), type rules,
// evidence and membership cycles. Issues are ordered deterministically.
func ValidateRelationships(c EditorialCatalog) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	artists := make(map[string]Artist, len(c.Artists))
	for _, artist := range c.Artists {
		artists[artist.Slug] = artist
	}

	seen := map[RelationshipKey]int{}
	for i, rel := range c.Relationships {
		prefix := fmt.Sprintf("relationships[%d]", i)
		issues = append(issues, validateRelationship(prefix, rel, artists)...)

		key := rel.Key()
		if previous, ok := seen[key]; ok {
			issues = append(issues, ValidationIssue{
				Field:   prefix,
				Message: fmt.Sprintf("duplicate %s; first seen at relationships[%d]", key, previous),
			})
			continue
		}
		seen[key] = i
	}

	// Membership is exclusive with the softer undirected types for one pair.
	member := map[[2]string]int{}
	for i, rel := range c.Relationships {
		if rel.Type == RelMemberOf {
			member[orderedPair(rel.From, rel.To)] = i
		}
	}
	for i, rel := range c.Relationships {
		if rel.Type != RelCollaboratedWith && rel.Type != RelAssociatedWith {
			continue
		}
		if previous, ok := member[orderedPair(rel.From, rel.To)]; ok {
			issues = append(issues, ValidationIssue{
				Field:   fmt.Sprintf("relationships[%d]", i),
				Message: fmt.Sprintf("%s duplicates the membership at relationships[%d]; membership and collaboration are separate claims", rel.Type, previous),
			})
		}
	}

	issues = append(issues, membershipCycles(c.Relationships)...)

	return issues
}

func validateRelationship(prefix string, rel Relationship, artists map[string]Artist) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	add := func(sub, message string) {
		issues = append(issues, ValidationIssue{Field: prefix + sub, Message: message})
	}

	if !IsKnownRelationshipType(rel.Type) {
		add(".type", "is unknown")
	}
	if !IsKnownRelationshipStatus(rel.Status) {
		add(".status", "must be proposed, verified or rejected")
	}
	switch rel.Confidence {
	case "", ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
	default:
		add(".confidence", "must be low, medium or high")
	}

	from, fromOK := artists[rel.From]
	to, toOK := artists[rel.To]
	if !fromOK {
		add(".from", fmt.Sprintf("references unknown artist %q", rel.From))
	}
	if !toOK {
		add(".to", fmt.Sprintf("references unknown artist %q", rel.To))
	}
	if rel.From != "" && rel.From == rel.To {
		add(".to", "must differ from from (self relationship)")
	}

	if fromOK && toOK && rel.From != rel.To && IsKnownRelationshipType(rel.Type) {
		switch rel.Type {
		case RelMemberOf, RelCollectiveOrImprint:
			if !to.IsGroup() {
				add(".to", fmt.Sprintf("%s requires a group target; %q does not have the affiliate_group category", rel.Type, rel.To))
			}
		case RelProducerFor:
			if !from.HasRole(CategoryProducer) {
				add(".from", fmt.Sprintf("producer_for requires a producer; %q does not have the producer category", rel.From))
			}
		}
	}
	if !rel.Type.Directed() && rel.From > rel.To {
		add("", "undirected relationships must list the lower slug in from")
	}

	if rel.Start != "" && !validPartialDate(rel.Start) {
		add(".start", "must use YYYY, YYYY-MM or YYYY-MM-DD")
	}
	if rel.End != "" && !validPartialDate(rel.End) {
		add(".end", "must use YYYY, YYYY-MM or YYYY-MM-DD")
	}
	if rel.Start != "" && rel.End != "" && validPartialDate(rel.Start) && validPartialDate(rel.End) && rel.End < rel.Start {
		add(".end", "must not be before start")
	}
	if len(rel.Commentary) > maxCommentaryLength {
		add(".commentary", fmt.Sprintf("must be at most %d characters", maxCommentaryLength))
	}

	independent := 0
	seenEvidence := map[string]int{}
	for i, evidence := range rel.Evidence {
		field := fmt.Sprintf(".evidence[%d]", i)
		if !isKnownEvidenceType(evidence.Type) {
			add(field+".type", "is unknown")
		}
		if evidence.URL != "" && !httpsURLPattern.MatchString(evidence.URL) {
			add(field+".url", "must be an https URL")
		}
		if strings.TrimSpace(evidence.Rationale) == "" {
			add(field+".rationale", "is required")
		}
		if previous, ok := seenEvidence[evidence.key()]; ok {
			add(field, fmt.Sprintf("duplicate evidence; first seen at evidence[%d]", previous))
		} else {
			seenEvidence[evidence.key()] = i
		}
		if evidence.Independent() {
			independent++
		}
	}

	if rel.Status == RelationshipVerified {
		if independent == 0 {
			add(".evidence", "verified requires at least one independent evidence item (credits and name matches do not count)")
		}
		if strings.TrimSpace(rel.Reviewer) == "" {
			add(".reviewer", "is required when verified")
		}
		if rel.Confidence == "" {
			add(".confidence", "is required when verified")
		}
	}
	if rel.Status == RelationshipRejected && len(rel.Evidence) == 0 && strings.TrimSpace(rel.Commentary) == "" {
		add(".commentary", "rejected requires commentary or evidence explaining why")
	}
	if rel.ReviewedAt != "" {
		if _, err := time.Parse(time.RFC3339, rel.ReviewedAt); err != nil {
			add(".reviewed_at", "must use RFC 3339")
		}
	}

	return issues
}

func validPartialDate(value string) bool {
	if !partialDatePattern.MatchString(value) {
		return false
	}
	switch len(value) {
	case 4:
		return true
	case 7:
		_, err := time.Parse("2006-01", value)
		return err == nil
	default:
		_, err := time.Parse(time.DateOnly, value)
		return err == nil
	}
}

func orderedPair(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// membershipCycles reports cycles over the directed membership-like edges
// (member_of, collective_or_imprint), where a group cannot contain itself.
func membershipCycles(rels []Relationship) []ValidationIssue {
	graph := map[string][]string{}
	for _, rel := range rels {
		if (rel.Type == RelMemberOf || rel.Type == RelCollectiveOrImprint) && rel.From != rel.To {
			graph[rel.From] = append(graph[rel.From], rel.To)
		}
	}
	nodes := make([]string, 0, len(graph))
	for node, next := range graph {
		sort.Strings(next)
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var issues []ValidationIssue
	var path []string
	var visit func(string)
	visit = func(node string) {
		state[node] = visiting
		path = append(path, node)
		for _, next := range graph[node] {
			switch state[next] {
			case visiting:
				start := 0
				for i, p := range path {
					if p == next {
						start = i
						break
					}
				}
				cycle := append(append([]string(nil), path[start:]...), next)
				issues = append(issues, ValidationIssue{
					Field:   "relationships",
					Message: "membership cycle: " + strings.Join(cycle, " -> "),
				})
			case 0:
				visit(next)
			}
		}
		path = path[:len(path)-1]
		state[node] = done
	}
	for _, node := range nodes {
		if state[node] == 0 {
			visit(node)
		}
	}
	return issues
}

// SortRelationships orders edges deterministically by type, from, to.
func SortRelationships(rels []Relationship) {
	sort.SliceStable(rels, func(i, j int) bool {
		a, b := rels[i].Key(), rels[j].Key()
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
}

// Established reports whether the relationship may be shown as a fact.
func (r Relationship) Established() bool {
	return r.Status == RelationshipVerified
}

// RelationshipsFor returns the edges involving slug, optionally filtered by
// type, in catalog order.
func (c EditorialCatalog) RelationshipsFor(slug string, types ...RelationshipType) []Relationship {
	out := make([]Relationship, 0)
	for _, rel := range c.Relationships {
		if !rel.Involves(slug) {
			continue
		}
		if len(types) > 0 && !containsType(types, rel.Type) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func containsType(types []RelationshipType, t RelationshipType) bool {
	for _, candidate := range types {
		if candidate == t {
			return true
		}
	}
	return false
}

// FindRelationship returns the index of the edge with key, or -1.
func (c EditorialCatalog) FindRelationship(key RelationshipKey) int {
	for i, rel := range c.Relationships {
		if rel.Key() == key {
			return i
		}
	}
	return -1
}

// FindArtist returns the index of the artist with slug, or -1.
func (c EditorialCatalog) FindArtist(slug string) int {
	for i, artist := range c.Artists {
		if artist.Slug == slug {
			return i
		}
	}
	return -1
}

// Clone returns a copy whose artist and relationship slices can be mutated
// without touching the original, so a failed operation never leaks changes.
func (c EditorialCatalog) Clone() EditorialCatalog {
	next := c
	next.Artists = append([]Artist(nil), c.Artists...)
	next.Relationships = append([]Relationship(nil), c.Relationships...)
	return next
}

// RenameArtistSlug returns a catalog where the artist oldSlug is renamed and
// every relationship endpoint is rewritten. It errors when the new slug is
// invalid or already taken, so no reference can dangle.
func (c EditorialCatalog) RenameArtistSlug(oldSlug, newSlug string) (EditorialCatalog, error) {
	if !slugPattern.MatchString(newSlug) {
		return c, fmt.Errorf("new slug %q must use lowercase letters, numbers and single hyphens", newSlug)
	}
	index := c.FindArtist(oldSlug)
	if index < 0 {
		return c, fmt.Errorf("%w: %q", ErrUnknownArtist, oldSlug)
	}
	if oldSlug == newSlug {
		return c, fmt.Errorf("new slug is identical to the current slug %q", oldSlug)
	}
	if c.FindArtist(newSlug) >= 0 {
		return c, fmt.Errorf("slug %q is already used by another artist", newSlug)
	}

	next := c.Clone()
	next.Artists[index].Slug = newSlug
	for i, rel := range next.Relationships {
		if rel.From == oldSlug {
			rel.From = newSlug
		}
		if rel.To == oldSlug {
			rel.To = newSlug
		}
		next.Relationships[i] = rel.Normalized()
	}
	return next, nil
}

// RemoveArtist returns a catalog without the artist. It is blocked while any
// relationship still references the slug and names every blocking edge.
func (c EditorialCatalog) RemoveArtist(slug string) (EditorialCatalog, error) {
	index := c.FindArtist(slug)
	if index < 0 {
		return c, fmt.Errorf("%w: %q", ErrUnknownArtist, slug)
	}
	if refs := c.RelationshipsFor(slug); len(refs) > 0 {
		keys := make([]string, 0, len(refs))
		for _, rel := range refs {
			keys = append(keys, rel.Key().String())
		}
		return c, fmt.Errorf("%w: remove %s first", ErrArtistReferenced, strings.Join(keys, "; "))
	}
	next := c.Clone()
	next.Artists = append(next.Artists[:index], next.Artists[index+1:]...)
	return next, nil
}
