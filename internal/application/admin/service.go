// Package admin holds the read-only use cases behind the local admin UI. It
// depends only on the catalog domain and ports, never on HTTP, YAML or SQLite.
package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	DefaultPageSize = 25
	MaxPageSize     = 100
)

// IDState summarises how far an artist's Spotify identity is reviewed.
type IDState string

const (
	// IDVerified: at least one linked profile has a verified review.
	IDVerified IDState = "verified"
	// IDPending: IDs are linked but none is verified yet.
	IDPending IDState = "pending"
	// IDUnresolved: no Spotify ID is linked.
	IDUnresolved IDState = "unresolved"
)

// SyncInfo describes the most recent sync run.
type SyncInfo struct {
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time
}

// OperationsReader is the outbound port for database-backed facts. Any error
// means the database is unavailable; the admin keeps serving the YAML catalog.
type OperationsReader interface {
	LastSync(ctx context.Context) (SyncInfo, bool, error)
	PendingReconciliation(ctx context.Context, c catalog.EditorialCatalog) (int, error)
}

// Service exposes the read-only admin queries.
type Service struct {
	store       artists.CatalogStore
	ops         OperationsReader
	credentials func() bool
	catalogPath string
}

// NewService builds the service. credentials reports only whether Spotify
// credentials are configured; their values never reach this layer.
func NewService(store artists.CatalogStore, ops OperationsReader, credentials func() bool, catalogPath string) Service {
	if credentials == nil {
		credentials = func() bool { return false }
	}
	return Service{store: store, ops: ops, credentials: credentials, catalogPath: catalogPath}
}

type Dashboard struct {
	Artists                   int
	Enabled                   int
	Groups                    int
	People                    int
	VerifiedIDs               int
	PendingIDs                int
	UnresolvedIDs             int
	Relationships             int
	ReviewQueue               int
	PendingReconciliation     int
	DatabaseAvailable         bool
	LastSync                  *SyncInfo
	SpotifyCredentialsPresent bool
}

type ListQuery struct {
	Search   string
	Page     int
	PageSize int
}

type Page[T any] struct {
	Items    []T
	Total    int
	Page     int
	PageSize int
	Pages    int
	Search   string
}

type ArtistRow struct {
	Slug     string
	Name     string
	Category catalog.Category
	Enabled  bool
	IDState  IDState
	IDs      int
	Group    bool
}

type GroupRow struct {
	Slug            string
	Name            string
	IDState         IDState
	VerifiedMembers int
	ProposedMembers int
}

type ReviewItem struct {
	Kind   string // spotify_profile | relationship
	Artist string
	Name   string
	Detail string
	Status string
}

func (s Service) load(ctx context.Context) (catalog.EditorialCatalog, error) {
	c, err := s.store.Load(ctx, s.catalogPath)
	if err != nil {
		return catalog.EditorialCatalog{}, fmt.Errorf("load catalog: %w", err)
	}
	return c, nil
}

func (s Service) Dashboard(ctx context.Context) (Dashboard, error) {
	c, err := s.load(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	d := Dashboard{
		Artists:                   len(c.Artists),
		Relationships:             len(c.Relationships),
		SpotifyCredentialsPresent: s.credentials(),
	}
	groups := groupSlugs(c)
	for _, a := range c.Artists {
		if a.Enabled {
			d.Enabled++
		}
		if _, ok := groups[a.Slug]; ok {
			d.Groups++
		} else {
			d.People++
		}
		switch idState(a) {
		case IDVerified:
			d.VerifiedIDs++
		case IDPending:
			d.PendingIDs++
		default:
			d.UnresolvedIDs++
		}
	}
	d.ReviewQueue = len(reviewItems(c))

	if s.ops != nil {
		if info, ok, err := s.ops.LastSync(ctx); err == nil {
			d.DatabaseAvailable = true
			if ok {
				d.LastSync = &info
			}
			if n, err := s.ops.PendingReconciliation(ctx, c); err == nil {
				d.PendingReconciliation = n
			}
		}
	}
	return d, nil
}

func (s Service) ListArtists(ctx context.Context, q ListQuery) (Page[ArtistRow], error) {
	c, err := s.load(ctx)
	if err != nil {
		return Page[ArtistRow]{}, err
	}
	groups := groupSlugs(c)
	rows := make([]ArtistRow, 0, len(c.Artists))
	for _, a := range c.Artists {
		if !matches(q.Search, a.Slug, a.Name, a.PublicName) {
			continue
		}
		_, isGroup := groups[a.Slug]
		rows = append(rows, ArtistRow{
			Slug: a.Slug, Name: a.Name, Category: a.Category, Enabled: a.Enabled,
			IDState: idState(a), IDs: len(a.AllSpotifyIDs()), Group: isGroup,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return paginate(rows, q), nil
}

func (s Service) ListGroups(ctx context.Context, q ListQuery) (Page[GroupRow], error) {
	c, err := s.load(ctx)
	if err != nil {
		return Page[GroupRow]{}, err
	}
	groups := groupSlugs(c)
	rows := make([]GroupRow, 0, len(groups))
	for _, a := range c.Artists {
		if _, ok := groups[a.Slug]; !ok || !matches(q.Search, a.Slug, a.Name, a.PublicName) {
			continue
		}
		row := GroupRow{Slug: a.Slug, Name: a.Name, IDState: idState(a)}
		for _, r := range c.Relationships {
			if r.Type != catalog.RelMemberOf || r.To != a.Slug {
				continue
			}
			switch r.Status {
			case catalog.RelationshipVerified:
				row.VerifiedMembers++
			case catalog.RelationshipProposed:
				row.ProposedMembers++
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return paginate(rows, q), nil
}

func (s Service) ReviewQueue(ctx context.Context, q ListQuery) (Page[ReviewItem], error) {
	c, err := s.load(ctx)
	if err != nil {
		return Page[ReviewItem]{}, err
	}
	var items []ReviewItem
	for _, it := range reviewItems(c) {
		if matches(q.Search, it.Artist, it.Name, it.Detail) {
			items = append(items, it)
		}
	}
	return paginate(items, q), nil
}

func reviewItems(c catalog.EditorialCatalog) []ReviewItem {
	names := make(map[string]string, len(c.Artists))
	for _, a := range c.Artists {
		names[a.Slug] = a.Name
	}
	var items []ReviewItem
	for _, a := range c.Artists {
		status := map[string]catalog.ProfileStatus{}
		for _, p := range a.SpotifyProfiles {
			status[p.ID] = p.Status
		}
		for _, id := range a.AllSpotifyIDs() {
			st, ok := status[id]
			if !ok {
				st = catalog.ProfileUnreviewed
			}
			if st != catalog.ProfileVerified && st != catalog.ProfileRejected {
				items = append(items, ReviewItem{Kind: "spotify_profile", Artist: a.Slug, Name: a.Name, Detail: id, Status: string(st)})
			}
		}
		for _, p := range a.SpotifyProfiles {
			if a.HasSpotifyID(p.ID) {
				continue
			}
			if p.Status == catalog.ProfileCandidate || p.Status == catalog.ProfileNeedsReview {
				items = append(items, ReviewItem{Kind: "spotify_profile", Artist: a.Slug, Name: a.Name, Detail: p.ID, Status: string(p.Status)})
			}
		}
	}
	for _, r := range c.Relationships {
		if r.Status != catalog.RelationshipProposed {
			continue
		}
		items = append(items, ReviewItem{
			Kind: "relationship", Artist: r.From, Name: names[r.From],
			Detail: fmt.Sprintf("%s %s", r.Type, firstNonEmpty(names[r.To], r.To)), Status: string(r.Status),
		})
	}
	return items
}

// groupSlugs returns every artist acting as a group: tagged as a group or the
// target of a member_of edge. Identical names alone never make a group.
func groupSlugs(c catalog.EditorialCatalog) map[string]struct{} {
	groups := map[string]struct{}{}
	for _, a := range c.Artists {
		if a.IsGroup() {
			groups[a.Slug] = struct{}{}
		}
	}
	for _, r := range c.Relationships {
		if r.Type == catalog.RelMemberOf && r.Status != catalog.RelationshipRejected {
			groups[r.To] = struct{}{}
		}
	}
	return groups
}

func idState(a catalog.Artist) IDState {
	ids := a.AllSpotifyIDs()
	if len(ids) == 0 {
		return IDUnresolved
	}
	for _, p := range a.SpotifyProfiles {
		if p.Status == catalog.ProfileVerified && a.HasSpotifyID(p.ID) {
			return IDVerified
		}
	}
	return IDPending
}

func matches(search string, fields ...string) bool {
	search = strings.ToLower(strings.TrimSpace(search))
	if search == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), search) {
			return true
		}
	}
	return false
}

func paginate[T any](rows []T, q ListQuery) Page[T] {
	size := q.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	pages := (len(rows) + size - 1) / size
	if pages == 0 {
		pages = 1
	}
	page := q.Page
	if page < 1 || page > pages {
		page = 1
	}
	start := (page - 1) * size
	end := start + size
	if end > len(rows) {
		end = len(rows)
	}
	return Page[T]{Items: rows[start:end], Total: len(rows), Page: page, PageSize: size, Pages: pages, Search: strings.TrimSpace(q.Search)}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
