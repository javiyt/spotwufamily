package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// ReviewLogEntry is one audited mapping decision.
type ReviewLogEntry struct {
	At             time.Time
	Actor          string
	Slug           string
	SpotifyID      string
	Action         string
	From           string
	To             string
	Note           string
	OverrideReason string
	EvidenceURLs   []string
	IDsBefore      []string
	IDsAfter       []string
}

// ReviewLog is the outbound port that keeps an append-only audit trail of
// mapping decisions, next to the per-profile reviewer and date in the catalog.
type ReviewLog interface {
	Append(ctx context.Context, entry ReviewLogEntry) error
}

// ResolutionDeps are the optional outbound ports behind the profile review
// workbench. Every one may be nil: the workbench then works from the catalog
// and the local cache only, and says what it could not check.
type ResolutionDeps struct {
	// Searcher ranks Spotify candidates for a name; nil disables name search.
	Searcher artists.CandidateSearcher
	// Source answers profile, album and track reads for the cards, cache first.
	Source artists.SpotifyProfileSource
	// Cached is a cache-only source used to flag suspect profiles in the queue.
	Cached artists.SpotifyProfileSource
	// MusicBrainz compares releases; nil means MusicBrainz is offline.
	MusicBrainz artists.MusicBrainzReleaseGroupSearcher
	// Log receives the audit trail; nil skips it.
	Log ReviewLog
	// Live reports that Spotify can be reached (credentials are configured).
	Live bool
	Now  func() time.Time
}

type resolution struct {
	deps  ResolutionDeps
	queue *queueCache
}

// WithResolution enables the profile review workbench.
func (s Service) WithResolution(deps ResolutionDeps) Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s.res = &resolution{deps: deps, queue: &queueCache{}}
	return s
}

// Resolvable reports whether the profile review workbench is available.
func (s Service) Resolvable() bool { return s.res != nil }

// QueueFilter selects one reason an artist is waiting for profile review.
type QueueFilter string

const (
	QueueAll         QueueFilter = ""
	QueueUnresolved  QueueFilter = "unresolved"
	QueueSuspect     QueueFilter = "suspect"
	QueueNeedsReview QueueFilter = "needs_review"
	QueueUnverified  QueueFilter = "unverified"
	QueueRejected    QueueFilter = "rejected"
	QueueMultiple    QueueFilter = "multiple"
)

// QueueFilters lists every filter in display order.
func QueueFilters() []QueueFilter {
	return []QueueFilter{QueueAll, QueueUnresolved, QueueSuspect, QueueNeedsReview, QueueUnverified, QueueRejected, QueueMultiple}
}

// IsKnownQueueFilter reports whether f is a supported filter.
func IsKnownQueueFilter(f QueueFilter) bool {
	for _, known := range QueueFilters() {
		if f == known {
			return true
		}
	}
	return false
}

// QueueRow is one artist waiting for a profile decision.
type QueueRow struct {
	Slug    string
	Name    string
	Order   int
	Enabled bool
	Primary string
	IDs     int
	Flags   []QueueFilter
	Reasons []string
}

// QueueCounts are whole-queue totals that ignore the active filter and search.
type QueueCounts struct {
	All, Unresolved, Suspect, NeedsReview, Unverified, Rejected, Multiple int
}

// ResolveQueueQuery filters the queue.
type ResolveQueueQuery struct {
	ListQuery
	Filter QueueFilter
}

// ResolveQueuePage is one page of the queue with its counts.
type ResolveQueuePage struct {
	Page   Page[QueueRow]
	Counts QueueCounts
	Filter QueueFilter
	// SuspectKnown is false when no cached evidence exists to flag suspects.
	SuspectKnown bool
	Live         bool
}

// ResolveQueue lists artists that need a profile decision: unresolved, suspect
// (cached evidence contradicts a linked profile), needs_review, unverified,
// previously rejected and multi-ID artists. Order is the editorial order, then
// slug, so repeated loads are identical. A search term also finds artists that
// are not flagged, so any artist can be reviewed from here.
func (s Service) ResolveQueue(ctx context.Context, q ResolveQueueQuery) (ResolveQueuePage, error) {
	if s.res == nil {
		return ResolveQueuePage{}, ErrReadOnly
	}
	c, err := s.load(ctx)
	if err != nil {
		return ResolveQueuePage{}, err
	}
	suspects, known := s.suspectProfiles(ctx, c)

	var rows []QueueRow
	var counts QueueCounts
	for _, a := range c.Artists {
		if a.Archived {
			continue
		}
		row := QueueRow{Slug: a.Slug, Name: a.Name, Order: a.EditorialOrder, Enabled: a.Enabled, Primary: a.SpotifyID, IDs: len(a.AllSpotifyIDs())}
		flag := func(f QueueFilter, reason string) {
			row.Flags = append(row.Flags, f)
			row.Reasons = append(row.Reasons, reason)
		}
		if row.IDs == 0 {
			flag(QueueUnresolved, "no Spotify ID linked")
		}
		if reasons := suspects[a.Slug]; len(reasons) > 0 {
			flag(QueueSuspect, strings.Join(reasons, "; "))
		}
		var needs, unverified, rejected int
		for _, p := range a.SpotifyProfiles {
			switch p.Status {
			case catalog.ProfileNeedsReview:
				needs++
			case catalog.ProfileRejected:
				rejected++
			}
		}
		for _, id := range a.AllSpotifyIDs() {
			if st := a.StatusOf(id); st == catalog.ProfileUnreviewed || st == catalog.ProfileCandidate {
				unverified++
			}
		}
		if needs > 0 {
			flag(QueueNeedsReview, plural(needs, "profile")+" marked needs_review")
		}
		if unverified > 0 {
			flag(QueueUnverified, plural(unverified, "linked profile")+" not verified, so sync ignores "+pronoun(unverified))
		}
		if rejected > 0 {
			flag(QueueRejected, plural(rejected, "rejected profile"))
		}
		if row.IDs > 1 {
			flag(QueueMultiple, plural(row.IDs, "linked Spotify ID"))
		}

		if len(row.Flags) > 0 {
			counts.All++
			for _, f := range row.Flags {
				switch f {
				case QueueUnresolved:
					counts.Unresolved++
				case QueueSuspect:
					counts.Suspect++
				case QueueNeedsReview:
					counts.NeedsReview++
				case QueueUnverified:
					counts.Unverified++
				case QueueRejected:
					counts.Rejected++
				case QueueMultiple:
					counts.Multiple++
				}
			}
		}
		searching := strings.TrimSpace(q.Search) != ""
		if len(row.Flags) == 0 && !searching {
			continue
		}
		if !matches(q.Search, append([]string{a.Slug, a.Name, a.PublicName}, a.Aliases...)...) {
			continue
		}
		if q.Filter != QueueAll && !hasFlag(row.Flags, q.Filter) {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Order == 0) != (b.Order == 0) {
			return a.Order != 0
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Slug < b.Slug
	})
	filter := q.Filter
	if !IsKnownQueueFilter(filter) {
		filter = QueueAll
	}
	return ResolveQueuePage{Page: paginate(rows, q.ListQuery), Counts: counts, Filter: filter, SuspectKnown: known, Live: s.res.deps.Live}, nil
}

func hasFlag(flags []QueueFilter, want QueueFilter) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

type offlineMusicBrainz struct{}

func (offlineMusicBrainz) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return nil, artists.ErrSourceUnavailable
}

// queueCache memoises the cache-only audit behind the suspect flag, because
// re-reading every cached album on each page load would be wasteful. It is
// keyed by the linked IDs and their review states, so any mapping change
// invalidates it at once.
type queueCache struct {
	mu       sync.Mutex
	key      string
	at       time.Time
	suspects map[string][]string
}

const queueCacheTTL = 2 * time.Minute

func mappingFingerprint(c catalog.EditorialCatalog) string {
	h := sha256.New()
	for _, a := range c.Artists {
		h.Write([]byte(a.Slug))
		for _, id := range a.AllSpotifyIDs() {
			h.Write([]byte{0})
			h.Write([]byte(id + ":" + string(a.StatusOf(id))))
		}
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// suspectProfiles flags linked profiles whose cached releases contradict the
// artist. known is false when no cache is available, in which case nothing is
// flagged and the queue says so rather than implying all is well.
func (s Service) suspectProfiles(ctx context.Context, c catalog.EditorialCatalog) (map[string][]string, bool) {
	deps := s.res.deps
	if deps.Cached == nil {
		return nil, false
	}
	key := mappingFingerprint(c)
	cache := s.res.queue
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.key == key && deps.Now().Sub(cache.at) < queueCacheTTL {
		return cache.suspects, true
	}
	audit := artists.NewAuditProfiles(s.store, deps.Cached, offlineMusicBrainz{})
	report, err := audit.Run(ctx, artists.AuditProfilesOptions{CatalogPath: s.catalogPath, Offline: true, Now: deps.Now})
	if err != nil {
		return nil, false
	}
	out := map[string][]string{}
	for _, p := range report.Profiles {
		if p.Classification == artists.ClassSuspicious || p.Classification == artists.ClassAmbiguous {
			out[p.ArtistSlug] = append(out[p.ArtistSlug], p.SpotifyID+": "+p.Explanation)
		}
	}
	cache.key, cache.at, cache.suspects = key, deps.Now(), out
	return out, true
}
