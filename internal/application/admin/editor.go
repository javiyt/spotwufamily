package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

var (
	// ErrReadOnly is returned when the catalog store cannot do locked updates.
	ErrReadOnly = errors.New("catalog store is read-only")
	// ErrNotFound is returned for an unknown artist or relationship.
	ErrNotFound = errors.New("not found")
)

// CatalogUpdater is the outbound port for safe writes: mutate runs against the
// freshly loaded catalog while writers are serialised, and nothing is stored
// when it returns an error. Implementations must write atomically.
type CatalogUpdater interface {
	Update(ctx context.Context, path string, mutate func(catalog.EditorialCatalog) (catalog.EditorialCatalog, error)) error
}

// Editable reports whether the configured store accepts edits.
func (s Service) Editable() bool { return s.updater != nil }

// ArtistInput is the editorial part of a record. Spotify identity (IDs,
// reviewed profiles, genres, images) is deliberately absent: it changes only
// through the profile-review workflow and is always carried over unchanged.
type ArtistInput struct {
	Slug           string
	Name           string
	PublicName     string
	Category       catalog.Category
	Roles          []catalog.Category
	Aliases        []string
	Enabled        bool
	EditorialOrder int
	Notes          string
	ExternalURL    string
}

// InputFrom projects the editable fields of an artist.
func InputFrom(a catalog.Artist) ArtistInput {
	return ArtistInput{
		Slug: a.Slug, Name: a.Name, PublicName: a.PublicName, Category: a.Category,
		Roles: append([]catalog.Category(nil), a.Roles...), Aliases: append([]string(nil), a.Aliases...),
		Enabled: a.Enabled, EditorialOrder: a.EditorialOrder, Notes: a.Notes, ExternalURL: a.ExternalURL,
	}
}

func (in ArtistInput) normalized() ArtistInput {
	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	in.PublicName = strings.TrimSpace(in.PublicName)
	in.Notes = strings.TrimSpace(in.Notes)
	in.ExternalURL = strings.TrimSpace(in.ExternalURL)
	aliases := make([]string, 0, len(in.Aliases))
	for _, alias := range in.Aliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			aliases = append(aliases, alias)
		}
	}
	in.Aliases = aliases
	// Roles always include the primary category so the editor never has to
	// repeat it.
	if len(in.Roles) > 0 {
		has := false
		for _, r := range in.Roles {
			has = has || r == in.Category
		}
		if !has {
			in.Roles = append([]catalog.Category{in.Category}, in.Roles...)
		}
	}
	return in
}

func (in ArtistInput) applyTo(a catalog.Artist) catalog.Artist {
	a.Slug, a.Name, a.PublicName, a.Category = in.Slug, in.Name, in.PublicName, in.Category
	a.Roles, a.Aliases = in.Roles, in.Aliases
	a.Enabled, a.EditorialOrder, a.Notes, a.ExternalURL = in.Enabled, in.EditorialOrder, in.Notes, in.ExternalURL
	return a
}

// RelationshipView is a relationship seen from one artist.
type RelationshipView struct {
	Relationship catalog.Relationship
	Key          catalog.RelationshipKey
	Revision     string
	OtherSlug    string
	OtherName    string
	// Direction is "outbound" when the artist is the From side, else "inbound".
	Direction string
}

// ArtistDetail is everything the editor shows for one record.
type ArtistDetail struct {
	Artist        catalog.Artist
	Revision      string
	IsGroup       bool
	IDState       IDState
	Relationships []RelationshipView
	Editable      bool
}

// Artist loads one record with its revision and relationships. The revision
// must be sent back with an edit; the snapshot lets a later conflict name the
// fields another editor changed.
func (s Service) Artist(ctx context.Context, slug string) (ArtistDetail, error) {
	c, err := s.load(ctx)
	if err != nil {
		return ArtistDetail{}, err
	}
	index := c.FindArtist(slug)
	if index < 0 {
		return ArtistDetail{}, fmt.Errorf("%w: artist %q", ErrNotFound, slug)
	}
	a := c.Artists[index]
	rev := ArtistRevision(a)
	s.snapshots.put(rev, a)
	return ArtistDetail{
		Artist: a, Revision: rev, IsGroup: a.IsGroup() || len(membersOf(c, slug)) > 0, IDState: idState(a),
		Relationships: relationshipViews(c, slug), Editable: s.Editable(),
	}, nil
}

func membersOf(c catalog.EditorialCatalog, slug string) []catalog.Relationship {
	return c.RelationshipsFor(slug, catalog.RelMemberOf)
}

func relationshipViews(c catalog.EditorialCatalog, slug string) []RelationshipView {
	names := make(map[string]string, len(c.Artists))
	for _, a := range c.Artists {
		names[a.Slug] = a.Name
	}
	views := make([]RelationshipView, 0)
	for _, rel := range c.RelationshipsFor(slug) {
		rel = rel.Normalized()
		v := RelationshipView{Relationship: rel, Key: rel.Key(), Revision: relationshipRevision(rel), Direction: "outbound", OtherSlug: rel.To}
		if rel.To == slug {
			v.Direction, v.OtherSlug = "inbound", rel.From
		}
		v.OtherName = names[v.OtherSlug]
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i], views[j]
		if a.Key.Type != b.Key.Type {
			return a.Key.Type < b.Key.Type
		}
		return a.OtherSlug < b.OtherSlug
	})
	return views
}

// Choice is an artist an editor can pick as a relationship endpoint.
type Choice struct {
	Slug  string
	Name  string
	Group bool
}

// Choices lists every non-archived artist ordered by name.
func (s Service) Choices(ctx context.Context) ([]Choice, error) {
	c, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	groups := groupSlugs(c)
	out := make([]Choice, 0, len(c.Artists))
	for _, a := range c.Artists {
		if a.Archived {
			continue
		}
		_, g := groups[a.Slug]
		out = append(out, Choice{Slug: a.Slug, Name: a.Name, Group: g})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// ArtistRevision is the optimistic-concurrency token of a record: a hash of
// everything stored for it, including reviewed Spotify profiles.
func ArtistRevision(a catalog.Artist) string {
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func relationshipRevision(r catalog.Relationship) string {
	raw, _ := json.Marshal(r.Normalized())
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// ChangeKind names an artist mutation.
type ChangeKind string

const (
	ChangeCreate  ChangeKind = "create"
	ChangeUpdate  ChangeKind = "update"
	ChangeArchive ChangeKind = "archive"
	ChangeRestore ChangeKind = "restore"
	ChangeDelete  ChangeKind = "delete"
)

// ChangeRequest describes one artist mutation. With DryRun the change is
// validated and diffed exactly as a save would, but nothing is written.
type ChangeRequest struct {
	Kind ChangeKind
	// Slug and Revision identify the record being changed (not for create).
	Slug     string
	Revision string
	Input    ArtistInput

	ConfirmSlugChange bool
	ConfirmDelete     bool
	DryRun            bool
}

// FieldChange is one differing field, rendered as text.
type FieldChange struct {
	Field  string
	Before string
	After  string
}

// ChangeResult reports what a change did, or with DryRun would do.
type ChangeResult struct {
	Kind     ChangeKind
	DryRun   bool
	Slug     string
	Revision string
	Changes  []FieldChange
	Warnings []string
	// References are the relationships touching the record.
	References []RelationshipView
}

// ValidationError carries field-level issues. Artist fields are relative to
// the edited record (for example "external_url" or "aliases[1]").
type ValidationError struct {
	Issues []catalog.ValidationIssue
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.Error())
	}
	return "change rejected, it would make the catalog invalid: " + strings.Join(parts, "; ")
}

// ConflictError means the record changed since the editor loaded it.
type ConflictError struct {
	Slug            string
	CurrentRevision string
	// Changed lists fields another editor changed (Before is what this editor
	// saw, After is what is stored now). When BaseKnown is false the original
	// is no longer available and Changed compares the submission with the
	// stored record instead.
	Changed   []FieldChange
	BaseKnown bool
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%q was changed by someone else; reload before saving", e.Slug)
}

// ConfirmationError means the change needs explicit confirmation.
type ConfirmationError struct {
	Action     string // slug_change | delete
	References []RelationshipView
}

func (e *ConfirmationError) Error() string {
	return fmt.Sprintf("%s needs explicit confirmation (%d relationship reference(s))", strings.ReplaceAll(e.Action, "_", " "), len(e.References))
}

// ReferencedError means a delete is blocked by inbound references.
type ReferencedError struct {
	Slug       string
	References []RelationshipView
}

func (e *ReferencedError) Error() string {
	return fmt.Sprintf("%q is referenced by %d relationship(s); archive it instead or remove them first", e.Slug, len(e.References))
}

// ApplyChange validates and, unless DryRun, persists one artist mutation. The
// whole catalog is validated before anything is written and only issues the
// change introduces block it, so unrelated legacy problems do not lock editing
// out.
func (s Service) ApplyChange(ctx context.Context, req ChangeRequest) (ChangeResult, error) {
	var result ChangeResult
	err := s.update(ctx, req.DryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, error) {
		next, res, err := s.applyChange(c, req)
		result = res
		return next, err
	})
	if err != nil {
		return ChangeResult{}, err
	}
	result.DryRun = req.DryRun
	return result, nil
}

// update runs fn in a locked read-modify-write, or on a throwaway copy for
// previews.
func (s Service) update(ctx context.Context, dryRun bool, fn func(catalog.EditorialCatalog) (catalog.EditorialCatalog, error)) error {
	if s.updater == nil {
		return ErrReadOnly
	}
	if dryRun {
		c, err := s.load(ctx)
		if err != nil {
			return err
		}
		_, err = fn(c.Clone())
		return err
	}
	if err := s.updater.Update(ctx, s.catalogPath, fn); err != nil {
		return err
	}
	return nil
}

func (s Service) applyChange(c catalog.EditorialCatalog, req ChangeRequest) (catalog.EditorialCatalog, ChangeResult, error) {
	res := ChangeResult{Kind: req.Kind}
	next := c.Clone()
	in := req.Input.normalized()
	var before, after catalog.Artist
	target := req.Slug

	if req.Kind == ChangeCreate {
		if in.Slug == "" {
			in.Slug = catalog.Slugify(in.Name)
		}
		after = in.applyTo(catalog.Artist{})
		next.Artists = append(next.Artists, after)
		target = in.Slug
	} else {
		index := next.FindArtist(req.Slug)
		if index < 0 {
			return c, res, fmt.Errorf("%w: artist %q", ErrNotFound, req.Slug)
		}
		before = next.Artists[index]
		if rev := ArtistRevision(before); rev != req.Revision {
			return c, res, s.conflict(before, rev, req)
		}
		refs := relationshipViews(c, req.Slug)
		after = before

		switch req.Kind {
		case ChangeUpdate:
			after = in.applyTo(before)
			if in.Slug == "" {
				after.Slug = before.Slug
			}
			if after.Slug != before.Slug {
				if !req.ConfirmSlugChange {
					return c, res, &ConfirmationError{Action: "slug_change", References: refs}
				}
				renamed, err := next.RenameArtistSlug(before.Slug, after.Slug)
				if err != nil {
					return c, res, &ValidationError{Issues: []catalog.ValidationIssue{{Field: "slug", Message: err.Error()}}}
				}
				next = renamed
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"Slug changed from %q to %q; %d relationship(s) were rewritten. Run `artists reconcile` so synced Spotify data follows the new slug.",
					before.Slug, after.Slug, len(refs)))
				index = next.FindArtist(after.Slug)
			}
			next.Artists[index] = after
			target = after.Slug
		case ChangeArchive:
			after.Archived, after.Enabled = true, false
			next.Artists[index] = after
		case ChangeRestore:
			after.Archived = false
			next.Artists[index] = after
		case ChangeDelete:
			if len(refs) > 0 {
				return c, res, &ReferencedError{Slug: req.Slug, References: refs}
			}
			if !req.ConfirmDelete {
				return c, res, &ConfirmationError{Action: "delete"}
			}
			removed, err := next.RemoveArtist(req.Slug)
			if err != nil {
				return c, res, err
			}
			next = removed
			res.Warnings = append(res.Warnings, "Synced Spotify albums and tracks for this artist were not touched; reconcile them separately.")
		default:
			return c, res, fmt.Errorf("unknown change %q", req.Kind)
		}
	}

	if err := validateChange(c, next, target); err != nil {
		return c, res, err
	}

	res.Slug = target
	res.Changes = diffArtists(before, after)
	if req.Kind == ChangeDelete {
		res.Slug = req.Slug
	} else {
		res.Revision = ArtistRevision(after)
		res.References = relationshipViews(next, target)
	}
	return next, res, nil
}

// conflict builds the error for a stale revision.
func (s Service) conflict(current catalog.Artist, currentRev string, req ChangeRequest) error {
	if base, ok := s.snapshots.get(req.Revision); ok {
		return &ConflictError{Slug: current.Slug, CurrentRevision: currentRev, BaseKnown: true, Changed: diffArtists(base, current)}
	}
	return &ConflictError{Slug: current.Slug, CurrentRevision: currentRev, Changed: diffArtists(req.Input.normalized().applyTo(current), current)}
}

var issueIndex = regexp.MustCompile(`^(artists|relationships)\[(\d+)\]`)

// validateChange rejects the change when it introduces catalog issues. Issues
// are keyed by slug rather than position so removing an entry does not make
// old problems look new.
func validateChange(before, after catalog.EditorialCatalog, target string) error {
	known := map[catalog.ValidationIssue]struct{}{}
	for _, issue := range labelIssues(before, catalog.ValidateEditorialCatalog(before)) {
		known[issue] = struct{}{}
	}
	var introduced []catalog.ValidationIssue
	for _, issue := range labelIssues(after, catalog.ValidateEditorialCatalog(after)) {
		if _, ok := known[issue]; !ok {
			introduced = append(introduced, issue)
		}
	}
	if len(introduced) == 0 {
		return nil
	}
	prefix := fmt.Sprintf("artists[%s]", target)
	for i, issue := range introduced {
		switch {
		case issue.Field == prefix:
			introduced[i].Field = ""
		case strings.HasPrefix(issue.Field, prefix+"."):
			introduced[i].Field = strings.TrimPrefix(issue.Field, prefix+".")
		}
	}
	return &ValidationError{Issues: introduced}
}

func labelIssues(c catalog.EditorialCatalog, issues []catalog.ValidationIssue) []catalog.ValidationIssue {
	out := make([]catalog.ValidationIssue, 0, len(issues))
	for _, issue := range issues {
		if m := issueIndex.FindStringSubmatch(issue.Field); m != nil && m[1] == "artists" {
			if i, err := strconv.Atoi(m[2]); err == nil && i < len(c.Artists) {
				issue.Field = "artists[" + c.Artists[i].Slug + "]" + strings.TrimPrefix(issue.Field, m[0])
			}
		}
		out = append(out, issue)
	}
	return out
}

type fieldPair struct{ name, value string }

func artistFields(a catalog.Artist) []fieldPair {
	profiles := make([]string, 0, len(a.SpotifyProfiles))
	for _, p := range a.SpotifyProfiles {
		profiles = append(profiles, p.ID+":"+string(p.Status))
	}
	roles := make([]string, 0, len(a.Roles))
	for _, r := range a.Roles {
		roles = append(roles, string(r))
	}
	return []fieldPair{
		{"slug", a.Slug}, {"name", a.Name}, {"public_name", a.PublicName}, {"category", string(a.Category)},
		{"roles", strings.Join(roles, ", ")}, {"aliases", strings.Join(a.Aliases, " | ")},
		{"enabled", strconv.FormatBool(a.Enabled)}, {"archived", strconv.FormatBool(a.Archived)},
		{"editorial_order", strconv.Itoa(a.EditorialOrder)}, {"notes", a.Notes}, {"external_url", a.ExternalURL},
		{"spotify_ids", strings.Join(a.AllSpotifyIDs(), ", ")}, {"spotify_profiles", strings.Join(profiles, ", ")},
		{"genres", strings.Join(a.Genres, ", ")}, {"image_url", a.ImageURL},
	}
}

// diffArtists lists differing fields in record order. For a create or delete
// the absent side is the zero record, whose defaults never show as changes.
func diffArtists(before, after catalog.Artist) []FieldChange {
	changes := make([]FieldChange, 0)
	b, a := artistFields(before), artistFields(after)
	for i := range a {
		if a[i].value != b[i].value {
			changes = append(changes, FieldChange{Field: a[i].name, Before: b[i].value, After: a[i].value})
		}
	}
	return changes
}

// --- relationships ---

// RelationshipInput is the editable form of a relationship. At most one new
// evidence item can be attached per edit; existing evidence is kept.
type RelationshipInput struct {
	Type              catalog.RelationshipType
	From              string
	To                string
	Status            catalog.RelationshipStatus
	Confidence        catalog.CandidateConfidence
	Start             string
	End               string
	Era               string
	Commentary        string
	Reviewer          string
	EvidenceType      catalog.EvidenceType
	EvidenceURL       string
	EvidenceRationale string
}

// EvidenceTypes lists the evidence types an editor may pick.
func EvidenceTypes() []catalog.EvidenceType {
	return []catalog.EvidenceType{
		catalog.EvidenceMusicBrainz, catalog.EvidenceWikidata, catalog.EvidenceWikipedia, catalog.EvidenceOfficialSite,
		catalog.EvidenceLabel, catalog.EvidencePress, catalog.EvidenceSocial, catalog.EvidenceSpotifyProfile,
		catalog.EvidenceCredit, catalog.EvidenceNameMatch,
	}
}

// RelationshipKind names a relationship mutation.
type RelationshipKind string

const (
	RelAdd    RelationshipKind = "add"
	RelUpdate RelationshipKind = "update"
	RelRemove RelationshipKind = "remove"
)

// RelationshipRequest is one relationship mutation. Key and Revision identify
// the existing edge for update and remove.
type RelationshipRequest struct {
	Kind     RelationshipKind
	Key      catalog.RelationshipKey
	Revision string
	Input    RelationshipInput
	DryRun   bool
}

// RelationshipResult reports the edge as stored.
type RelationshipResult struct {
	Kind     RelationshipKind
	DryRun   bool
	Key      catalog.RelationshipKey
	Revision string
	Changes  []FieldChange
}

func (in RelationshipInput) relationship(existing *catalog.Relationship) catalog.Relationship {
	rel := catalog.Relationship{
		Type: in.Type, From: strings.TrimSpace(in.From), To: strings.TrimSpace(in.To), Status: in.Status,
		Confidence: in.Confidence, Start: strings.TrimSpace(in.Start), End: strings.TrimSpace(in.End),
		Era: strings.TrimSpace(in.Era), Commentary: strings.TrimSpace(in.Commentary), Reviewer: strings.TrimSpace(in.Reviewer),
	}
	if existing != nil {
		rel.Evidence = append(rel.Evidence, existing.Evidence...)
		if rel.Status == existing.Status {
			rel.ReviewedAt = existing.ReviewedAt
		}
	}
	rationale := strings.TrimSpace(in.EvidenceRationale)
	if in.EvidenceType != "" || strings.TrimSpace(in.EvidenceURL) != "" || rationale != "" {
		ev := catalog.Evidence{Type: in.EvidenceType, URL: strings.TrimSpace(in.EvidenceURL), Rationale: rationale}
		dup := false
		for _, have := range rel.Evidence {
			dup = dup || have == ev
		}
		if !dup {
			rel.Evidence = append(rel.Evidence, ev)
		}
	}
	return rel
}

// ApplyRelationship adds, updates or removes one relationship by reusing the
// graph use cases, so membership rules (independent evidence, reviewer,
// group targets, no cycles) are enforced by the domain and cannot be bypassed.
func (s Service) ApplyRelationship(ctx context.Context, req RelationshipRequest) (RelationshipResult, error) {
	var result RelationshipResult
	err := s.update(ctx, req.DryRun, func(c catalog.EditorialCatalog) (catalog.EditorialCatalog, error) {
		var existing *catalog.Relationship
		if req.Kind != RelAdd {
			key := catalog.NewRelationshipKey(req.Key.Type, req.Key.From, req.Key.To)
			index := c.FindRelationship(key)
			if index < 0 {
				return c, fmt.Errorf("%w: relationship %s", ErrNotFound, key)
			}
			cur := c.Relationships[index]
			if rev := relationshipRevision(cur); rev != req.Revision {
				return c, &ConflictError{Slug: key.String(), CurrentRevision: rev, Changed: nil}
			}
			existing = &cur
		}

		tx := &txStore{c: c}
		manage := artists.NewManageRelationships(tx)
		var change artists.RelationshipChange
		var err error
		switch req.Kind {
		case RelAdd:
			change, err = manage.Add(context.Background(), "", req.Input.relationship(nil), false)
		case RelUpdate:
			change, err = manage.Update(context.Background(), "", req.Key, req.Input.relationship(existing), false)
		case RelRemove:
			change, err = manage.Remove(context.Background(), "", req.Key, false)
		default:
			err = fmt.Errorf("unknown relationship change %q", req.Kind)
		}
		var invalid *artists.InvalidRelationshipError
		if errors.As(err, &invalid) {
			return c, &ValidationError{Issues: relativeRelationshipIssues(invalid.Issues)}
		}
		if err != nil {
			return c, err
		}

		result = RelationshipResult{Kind: req.Kind, Changes: diffRelationships(change.Before, change.After)}
		if change.After != nil {
			result.Key, result.Revision = change.After.Key(), relationshipRevision(*change.After)
		} else if change.Before != nil {
			result.Key = change.Before.Key()
		}
		return tx.saved, nil
	})
	if err != nil {
		return RelationshipResult{}, err
	}
	result.DryRun = req.DryRun
	return result, nil
}

// txStore lets the graph use cases run inside the locked update: they read the
// catalog the lock holder loaded and hand the result back instead of saving.
type txStore struct {
	c     catalog.EditorialCatalog
	saved catalog.EditorialCatalog
}

func (t *txStore) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return t.c.Clone(), nil
}

func (t *txStore) Save(_ context.Context, _ string, c catalog.EditorialCatalog) error {
	t.saved = c
	return nil
}

var relIndex = regexp.MustCompile(`^relationships\[\d+\]\.?`)

func relativeRelationshipIssues(issues []catalog.ValidationIssue) []catalog.ValidationIssue {
	out := make([]catalog.ValidationIssue, 0, len(issues))
	for _, issue := range issues {
		issue.Field = relIndex.ReplaceAllString(issue.Field, "")
		out = append(out, issue)
	}
	return out
}

func relationshipFields(r *catalog.Relationship) map[string]string {
	if r == nil {
		return map[string]string{}
	}
	evidence := make([]string, 0, len(r.Evidence))
	for _, e := range r.Evidence {
		evidence = append(evidence, fmt.Sprintf("%s %s", e.Type, e.URL))
	}
	return map[string]string{
		"type": string(r.Type), "from": r.From, "to": r.To, "status": string(r.Status),
		"confidence": string(r.Confidence), "start": r.Start, "end": r.End, "era": r.Era,
		"commentary": r.Commentary, "reviewer": r.Reviewer, "evidence": strings.Join(evidence, " | "),
	}
}

func diffRelationships(before, after *catalog.Relationship) []FieldChange {
	b, a := relationshipFields(before), relationshipFields(after)
	names := []string{"type", "from", "to", "status", "confidence", "start", "end", "era", "commentary", "reviewer", "evidence"}
	changes := make([]FieldChange, 0)
	for _, name := range names {
		if b[name] != a[name] {
			changes = append(changes, FieldChange{Field: name, Before: b[name], After: a[name]})
		}
	}
	return changes
}

// --- snapshots ---

// snapshotCache remembers recently served records by revision so a conflict
// can say which fields changed. It is only a diagnostic aid: a miss degrades
// the message, never the safety of the save.
type snapshotCache struct {
	mu    sync.Mutex
	items map[string]catalog.Artist
	order []string
}

const snapshotLimit = 256

func newSnapshotCache() *snapshotCache {
	return &snapshotCache{items: map[string]catalog.Artist{}}
}

func (c *snapshotCache) put(rev string, a catalog.Artist) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[rev]; ok {
		return
	}
	c.items[rev] = a
	c.order = append(c.order, rev)
	if len(c.order) > snapshotLimit {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

func (c *snapshotCache) get(rev string) (catalog.Artist, bool) {
	if c == nil {
		return catalog.Artist{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.items[rev]
	return a, ok
}
