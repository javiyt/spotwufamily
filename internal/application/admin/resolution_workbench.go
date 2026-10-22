package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	maxSearchCandidates = 8
	maxCardReleases     = 8
	maxCardTracks       = 3
	maxCardTrackReleses = 2
	externalTimeout     = 25 * time.Second
)

// Notice tells the reviewer what a source could or could not answer. A source
// that failed is missing evidence, never negative evidence.
type Notice struct {
	Level      string // info | warn | error
	Source     string // spotify | musicbrainz | cache | catalog
	Message    string
	RetryAfter time.Duration
}

// EvidenceSuggestion is independent evidence found by the audit that the
// reviewer may attach to a decision.
type EvidenceSuggestion struct {
	Type      catalog.EvidenceType
	URL       string
	Rationale string
}

// TrackSample is a representative track with its credits.
type TrackSample struct {
	Release string
	Name    string
	Credits []string
}

// ActionOption is a decision the reviewer may take on a card.
type ActionOption struct {
	Name  string
	Label string
}

// Card is one Spotify profile shown next to the editorial artist: an attached
// ID, a ranked candidate, a manually entered ID or a past rejection.
type Card struct {
	SpotifyID string
	URI       string
	URL       string
	Name      string
	ImageURL  string
	Genres    []string
	Followers int
	// Linked is true when the artist currently links this ID.
	Linked bool
	Role   artists.ProfileRole
	Status catalog.ProfileStatus
	// Stored review provenance.
	Reviewer   string
	ReviewedAt string
	Evidence   []catalog.Evidence
	// Owner is another artist that links or tracks this ID.
	Owner string
	// Ranking, present for candidates found by search.
	Ranked     bool
	Score      int
	Confidence string
	Reason     string
	Gate       *catalog.EligibilityDecision
	// Manual marks an ID the reviewer typed in rather than one ranked by search.
	Manual bool

	Audit       artists.ProfileAuditResult
	Albums      []artists.ProfileRelease
	Singles     []artists.ProfileRelease
	Tracks      []TrackSample
	Suggestions []EvidenceSuggestion
	// Credits are tracks shared with verified editorial groups: supporting,
	// non-independent evidence the reviewer may attach.
	Credits  CreditReport
	Warnings []string
	Notices  []Notice
	Actions  []ActionOption
}

// Workbench is everything shown for one artist's profile review.
type Workbench struct {
	Artist   catalog.Artist
	Revision string
	Editable bool
	// Attached are the currently linked IDs, primary first.
	Attached []Card
	// History are rejected or quarantined profiles that are no longer linked.
	History []Card
	// Candidates are search results (or the single manually entered ID).
	Candidates []Card
	Query      string
	Searched   bool
	// SearchAvailable is false when no Spotify credentials are configured.
	SearchAvailable bool
	Notices         []Notice
}

// ResolveWorkbench builds the review page for an artist. Network access happens
// only to answer an explicit search or to fill a card the cache cannot, and a
// failing source degrades to a notice instead of an error.
func (s Service) ResolveWorkbench(ctx context.Context, slug, query string) (Workbench, error) {
	if s.res == nil {
		return Workbench{}, ErrReadOnly
	}
	c, err := s.load(ctx)
	if err != nil {
		return Workbench{}, err
	}
	index := c.FindArtist(slug)
	if index < 0 {
		return Workbench{}, fmt.Errorf("%w: artist %q", ErrNotFound, slug)
	}
	artist := c.Artists[index]
	deps := s.res.deps
	rev := ArtistRevision(artist)
	s.snapshots.put(rev, artist)

	ctx, cancel := context.WithTimeout(ctx, externalTimeout)
	defer cancel()
	src := newMemoSource(deps.Source)
	wb := Workbench{
		Artist: artist, Revision: rev, Editable: s.Editable(),
		Query: strings.TrimSpace(query), SearchAvailable: deps.Searcher != nil,
	}
	if !deps.Live {
		wb.Notices = append(wb.Notices, Notice{Level: "info", Source: "spotify", Message: "Spotify is not configured (no credentials): the cards use the local cache only, and nothing can be searched online. Cached data may be out of date."})
	}
	if deps.MusicBrainz == nil {
		wb.Notices = append(wb.Notices, Notice{Level: "info", Source: "musicbrainz", Message: "MusicBrainz is offline: release comparison is skipped, which is missing evidence, not a contradiction."})
	}

	ids := artist.AllSpotifyIDs()
	wb.Attached = make([]Card, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		role := artists.ProfileRoleSecondary
		if i == 0 {
			role = artists.ProfileRolePrimary
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			wb.Attached[i] = s.buildCard(ctx, c, artist, cardInput{id: id, role: role}, src)
		}()
	}
	wg.Wait()

	for _, p := range artist.SpotifyProfiles {
		if artist.HasSpotifyID(p.ID) || (p.Status != catalog.ProfileRejected && p.Status != catalog.ProfileNeedsReview) {
			continue
		}
		wb.History = append(wb.History, s.historyCard(c, artist, p))
	}
	sort.SliceStable(wb.History, func(i, j int) bool { return wb.History[i].SpotifyID < wb.History[j].SpotifyID })

	if wb.Query != "" {
		wb.Searched = true
		s.searchInto(ctx, &wb, c, artist, src)
	}
	wb.Notices = append(wb.Notices, src.notices()...)
	return wb, nil
}

// searchInto answers the search box: a Spotify ID, URI or open.spotify.com
// artist link is parsed locally and looked up by ID; anything else is a name
// search through the same ranking and evidence gate as the CLI. Input is only
// ever parsed, never fetched.
func (s Service) searchInto(ctx context.Context, wb *Workbench, c catalog.EditorialCatalog, artist catalog.Artist, src *memoSource) {
	deps := s.res.deps
	query := wb.Query
	if len(query) > 200 {
		query = query[:200]
	}

	if id, err := catalog.ParseSpotifyArtistRef(query); err == nil {
		if artist.HasSpotifyID(id) {
			wb.Notices = append(wb.Notices, Notice{Level: "info", Source: "catalog", Message: "That Spotify ID is already linked to this artist; see its card above."})
			return
		}
		wb.Candidates = []Card{s.buildCard(ctx, c, artist, cardInput{id: id, manual: true}, src)}
		return
	}
	if strings.Contains(query, "://") || strings.HasPrefix(strings.ToLower(query), "spotify:") {
		wb.Notices = append(wb.Notices, Notice{Level: "error", Source: "spotify", Message: "That is not a Spotify artist link, URI or 22-character ID. Only open.spotify.com/artist/… links are accepted, and they are never fetched, only read for the ID."})
		return
	}

	if deps.Searcher == nil {
		wb.Notices = append(wb.Notices, Notice{Level: "warn", Source: "spotify", Message: "Name search needs Spotify credentials. Paste a Spotify artist link or ID to review a profile from the cache instead."})
		return
	}
	probe := artist
	probe.Name = query
	found, err := searchCandidates(ctx, deps.Searcher, probe, c)
	if err != nil {
		wb.Notices = append(wb.Notices, sourceNotice("spotify", "search", err))
		return
	}
	policy := catalog.EligibilityPolicy{IsRejected: c.IsRejectedSpotifyID}
	matches := catalog.RankCandidates(artist, found)
	kept := matches[:0:0]
	for _, m := range matches {
		if m.Candidate.SpotifyID != "" && !artist.HasSpotifyID(m.Candidate.SpotifyID) {
			kept = append(kept, m)
		}
	}
	if len(kept) > maxSearchCandidates {
		kept = kept[:maxSearchCandidates]
	}
	if len(kept) == 0 {
		wb.Notices = append(wb.Notices, Notice{Level: "info", Source: "spotify", Message: "Spotify returned no new candidates for that search."})
		return
	}

	wb.Candidates = make([]Card, len(kept))
	var wg sync.WaitGroup
	for i, m := range kept {
		// Each candidate is judged as if it were the best match, with the others
		// as competition, exactly as `artists resolve` would judge the best one.
		ordered := append([]catalog.CandidateMatch{m}, append(append([]catalog.CandidateMatch(nil), kept[:i]...), kept[i+1:]...)...)
		decision := catalog.EvaluateEligibility(artist, ordered, policy)
		wg.Add(1)
		go func() {
			defer wg.Done()
			match := m
			wb.Candidates[i] = s.buildCard(ctx, c, artist, cardInput{id: m.Candidate.SpotifyID, match: &match, gate: &decision}, src)
		}()
	}
	wg.Wait()
}

func searchCandidates(ctx context.Context, searcher artists.CandidateSearcher, artist catalog.Artist, c catalog.EditorialCatalog) ([]catalog.ArtistCandidate, error) {
	if contextual, ok := searcher.(artists.CatalogAwareCandidateSearcher); ok {
		return contextual.SearchArtistCandidatesWithCatalog(ctx, artist, c)
	}
	return searcher.SearchArtistCandidates(ctx, artist)
}

type cardInput struct {
	id     string
	role   artists.ProfileRole
	manual bool
	match  *catalog.CandidateMatch
	gate   *catalog.EligibilityDecision
}

func (s Service) buildCard(ctx context.Context, c catalog.EditorialCatalog, artist catalog.Artist, in cardInput, src *memoSource) Card {
	deps := s.res.deps
	card := Card{
		SpotifyID: in.id, URI: "spotify:artist:" + in.id, URL: catalog.SpotifyArtistURL(in.id),
		Linked: artist.HasSpotifyID(in.id), Role: in.role, Status: artist.StatusOf(in.id), Manual: in.manual, Gate: in.gate,
	}
	if !card.Linked {
		card.Status = catalog.ProfileUnreviewed
	}
	if p, ok := artist.ProfileFor(in.id); ok {
		card.Status, card.Reviewer, card.ReviewedAt = p.Status, p.Reviewer, p.ReviewedAt
		card.Evidence = append([]catalog.Evidence(nil), p.Evidence...)
	}
	if owner, _, ok := c.ProfileOwner(in.id); ok && owner != artist.Slug {
		card.Owner = owner
	}
	if in.match != nil {
		cand := in.match.Candidate
		card.Ranked, card.Score, card.Confidence, card.Reason = true, in.match.Score, in.match.Confidence, in.match.Reason
		fillProfile(&card, cand)
	} else if profile, err := src.GetArtist(ctx, in.id); err == nil {
		fillProfile(&card, profile)
	}

	live := deps.MusicBrainz
	var mb artists.MusicBrainzReleaseGroupSearcher = offlineMusicBrainz{}
	if live != nil {
		mb = live
	}
	audit := artists.NewAuditProfiles(s.store, src, mb)
	role := in.role
	if role == "" {
		role = artists.ProfileRoleSecondary
	}
	result, err := audit.EvaluateProfile(ctx, c, artist.Slug, in.id, role, artists.AuditProfilesOptions{Offline: deps.MusicBrainz == nil, Now: deps.Now})
	if err != nil {
		card.Notices = append(card.Notices, Notice{Level: "warn", Source: "catalog", Message: "Evidence could not be gathered: " + err.Error()})
	} else {
		card.Audit = result
		if card.Name == "" {
			card.Name = result.ProfileName
		}
		if len(card.Genres) == 0 {
			card.Genres = result.Genres
		}
		card.Albums = withSafeReleaseLinks(result.Releases)
		card.Suggestions = suggestionsFrom(result)
	}

	card.Singles = s.singlesOf(ctx, in.id, src, &card)
	card.Tracks = s.tracksOf(ctx, in.id, src, result)
	if in.match != nil {
		// Ranked search results are not scanned: that would fan out one tracklist
		// read per candidate. Opening a profile by ID checks its credits.
		card.Credits = CreditReport{Status: CreditsNotChecked, Message: "Credits are not checked for search results. Look the profile up by its link or ID to check them."}
	} else {
		card.Credits = s.creditsOf(ctx, c, artist, card, src)
	}
	card.Warnings = cardWarnings(c, artist, card)
	if card.Audit.SpotifyStatus == artists.SourceRateLimited {
		card.Notices = append(card.Notices, Notice{Level: "warn", Source: "spotify", Message: "Spotify is rate limiting requests, so this card is incomplete.", RetryAfter: src.retryAfter()})
	}
	card.Actions = cardActions(artist, card)
	return card
}

func fillProfile(card *Card, p catalog.ArtistCandidate) {
	card.Name = p.Name
	card.Genres = append([]string(nil), p.Genres...)
	card.Followers = p.Followers
	card.ImageURL = bestImage(p)
}

// bestImage picks a medium-sized image, restricted to the Spotify CDN.
func bestImage(p catalog.ArtistCandidate) string {
	best := ""
	bestSize := 0
	for _, img := range p.Images {
		safe := catalog.SafeSpotifyImageURL(img.URL)
		if safe == "" {
			continue
		}
		size := img.Width
		// Prefer the image closest to 160px.
		if best == "" || abs(size-160) < abs(bestSize-160) {
			best, bestSize = safe, size
		}
	}
	if best != "" {
		return best
	}
	return catalog.SafeSpotifyImageURL(p.ImageURL)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func suggestionsFrom(r artists.ProfileAuditResult) []EvidenceSuggestion {
	var out []EvidenceSuggestion
	for _, e := range r.Evidence {
		if e.Kind != catalog.EvidenceMusicBrainzRelease || !e.IsIndependentProof() || e.URL == "" {
			continue
		}
		rationale := "MusicBrainz release group"
		if e.ReleaseName != "" {
			rationale += fmt.Sprintf(" %q", e.ReleaseName)
			if e.ReleaseYear != "" {
				rationale += " (" + e.ReleaseYear + ")"
			}
		}
		rationale += " matches releases on this Spotify profile"
		if e.Detail != "" {
			rationale += ": " + e.Detail
		}
		ev := catalog.Evidence{Type: catalog.EvidenceMusicBrainz, URL: e.URL, Rationale: rationale}
		if len(catalog.ValidateEvidence(ev)) > 0 {
			continue
		}
		out = append(out, EvidenceSuggestion{Type: ev.Type, URL: ev.URL, Rationale: ev.Rationale})
	}
	return out
}

func (s Service) singlesOf(ctx context.Context, id string, src *memoSource, card *Card) []artists.ProfileRelease {
	if card.Audit.SpotifyStatus == artists.SourceRateLimited || card.Audit.SpotifyStatus == artists.SourceNotFound {
		return nil
	}
	releases, err := src.GetArtistAlbums(ctx, id, []string{"single"})
	if err != nil {
		if errors.Is(err, artists.ErrCacheMiss) {
			card.Notices = append(card.Notices, Notice{Level: "info", Source: "cache", Message: "Singles are not in the local cache."})
		}
		return nil
	}
	out := make([]artists.ProfileRelease, 0, maxCardReleases)
	for _, r := range releases {
		if len(out) >= maxCardReleases {
			break
		}
		out = append(out, artists.ProfileRelease{ID: r.SpotifyID, Name: r.Name, Date: r.ReleaseDate, URL: catalog.SafeSpotifyReleaseURL(r.URL, r.SpotifyID)})
	}
	return out
}

// withSafeReleaseLinks returns a copy of releases whose URL is a canonical
// Spotify album link or empty, so templates can link them unconditionally.
func withSafeReleaseLinks(releases []artists.ProfileRelease) []artists.ProfileRelease {
	out := make([]artists.ProfileRelease, len(releases))
	for i, r := range releases {
		r.URL = catalog.SafeSpotifyReleaseURL(r.URL, r.ID)
		out[i] = r
	}
	return out
}

func (s Service) tracksOf(ctx context.Context, id string, src *memoSource, r artists.ProfileAuditResult) []TrackSample {
	var out []TrackSample
	for i, release := range r.Releases {
		if i >= maxCardTrackReleses {
			break
		}
		tracks, err := src.GetAlbumTracks(ctx, release.ID)
		if err != nil {
			continue
		}
		for j, t := range tracks {
			if j >= maxCardTracks {
				break
			}
			sample := TrackSample{Release: release.Name, Name: t.Name}
			for _, credit := range t.Artists {
				name := credit.Name
				if name == "" {
					name = credit.SpotifyID
				}
				sample.Credits = append(sample.Credits, name)
			}
			out = append(out, sample)
		}
	}
	return out
}

func cardWarnings(c catalog.EditorialCatalog, artist catalog.Artist, card Card) []string {
	var out []string
	if card.Owner != "" {
		out = append(out, fmt.Sprintf("This Spotify ID already belongs to %q. It can never be shared between two artists.", card.Owner))
	}
	if c.IsRejectedSpotifyID(card.SpotifyID) && card.Status != catalog.ProfileRejected {
		out = append(out, "This profile was rejected in review. A name match is not new evidence.")
	}
	key := normalizeName(card.Name)
	if key != "" {
		for _, other := range c.Artists {
			if other.Slug == artist.Slug {
				continue
			}
			if normalizeName(other.Name) == key {
				out = append(out, fmt.Sprintf("Another catalog artist, %q, has this exact name. A shared name is not identity evidence.", other.Slug))
				break
			}
		}
	}
	if card.Audit.SpotifyStatus == artists.SourceOK && card.Audit.ReleaseCount == 0 {
		out = append(out, "The catalog has no albums for this profile, so releases cannot be compared. That is missing evidence, not proof either way.")
	}
	return out
}

func normalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func cardActions(artist catalog.Artist, card Card) []ActionOption {
	if card.Owner != "" {
		return nil
	}
	var out []ActionOption
	add := func(name, label string) { out = append(out, ActionOption{Name: name, Label: label}) }
	switch {
	case card.Linked:
		if card.Status != catalog.ProfileVerified {
			add(ActionVerify, "Verify")
		}
		if artist.SpotifyID != card.SpotifyID {
			add(string(artists.MapReorderPrimary), "Make primary")
		}
		if card.Status != catalog.ProfileNeedsReview {
			add(string(artists.MapInconclusive), "Mark inconclusive")
		}
		add(string(artists.MapReject), "Reject")
		add(string(artists.MapQuarantine), "Quarantine")
		add(string(artists.MapRemove), "Remove")
	default:
		add(string(artists.MapAcceptPrimary), "Accept as primary")
		add(string(artists.MapAddSecondary), "Add as secondary")
		if card.Status != catalog.ProfileNeedsReview {
			add(string(artists.MapInconclusive), "Mark inconclusive")
		}
		if card.Status != catalog.ProfileRejected {
			add(string(artists.MapReject), "Reject")
		}
	}
	return out
}

// ActionVerify verifies a profile that is already linked.
const ActionVerify = "verify"

func (s Service) historyCard(c catalog.EditorialCatalog, artist catalog.Artist, p catalog.SpotifyProfile) Card {
	card := Card{
		SpotifyID: p.ID, URI: "spotify:artist:" + p.ID, URL: catalog.SpotifyArtistURL(p.ID),
		Status: p.Status, Reviewer: p.Reviewer, ReviewedAt: p.ReviewedAt, Evidence: append([]catalog.Evidence(nil), p.Evidence...),
	}
	if owner, _, ok := c.ProfileOwner(p.ID); ok && owner != artist.Slug {
		card.Owner = owner
	}
	card.Warnings = cardWarnings(c, artist, card)
	card.Actions = cardActions(artist, card)
	return card
}

// sourceNotice turns a source failure into a reviewer-facing notice.
func sourceNotice(source, what string, err error) Notice {
	n := Notice{Level: "warn", Source: source}
	switch {
	case errors.Is(err, artists.ErrSourceRateLimited):
		n.Message = fmt.Sprintf("Spotify is rate limiting requests, so the %s did not run.", what)
		n.RetryAfter, _ = artists.RetryAfterOf(err)
	case errors.Is(err, artists.ErrSourceUnavailable), errors.Is(err, context.DeadlineExceeded):
		n.Message = fmt.Sprintf("%s could not be reached for the %s. Nothing was concluded from that.", titleCase(source), what)
	case errors.Is(err, artists.ErrSourceNotFound):
		n.Message = fmt.Sprintf("%s has nothing for the %s.", titleCase(source), what)
	default:
		n.Level = "error"
		n.Message = fmt.Sprintf("The %s failed.", what)
	}
	return n
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// memoSource answers each Spotify read once per page, so a profile shown on a
// card and audited for evidence costs a single request.
type memoSource struct {
	inner artists.SpotifyProfileSource

	mu       sync.Mutex
	profiles map[string]*memoCall[catalog.ArtistCandidate]
	albums   map[string]*memoCall[[]catalog.Release]
	tracks   map[string]*memoCall[[]catalog.Track]
	limited  bool
	wait     time.Duration
}

type memoCall[T any] struct {
	once sync.Once
	val  T
	err  error
}

func newMemoSource(inner artists.SpotifyProfileSource) *memoSource {
	return &memoSource{
		inner:    inner,
		profiles: map[string]*memoCall[catalog.ArtistCandidate]{},
		albums:   map[string]*memoCall[[]catalog.Release]{},
		tracks:   map[string]*memoCall[[]catalog.Track]{},
	}
}

func memo[T any](m *memoSource, table map[string]*memoCall[T], key string, fetch func() (T, error)) (T, error) {
	m.mu.Lock()
	call, ok := table[key]
	if !ok {
		call = &memoCall[T]{}
		table[key] = call
	}
	m.mu.Unlock()
	call.once.Do(func() {
		call.val, call.err = fetch()
		if errors.Is(call.err, artists.ErrSourceRateLimited) {
			m.mu.Lock()
			m.limited = true
			if wait, ok := artists.RetryAfterOf(call.err); ok && wait > m.wait {
				m.wait = wait
			}
			m.mu.Unlock()
		}
	})
	return call.val, call.err
}

func (m *memoSource) GetArtist(ctx context.Context, id string) (catalog.ArtistCandidate, error) {
	return memo(m, m.profiles, id, func() (catalog.ArtistCandidate, error) {
		if m.inner == nil {
			return catalog.ArtistCandidate{}, artists.ErrCacheMiss
		}
		return m.inner.GetArtist(ctx, id)
	})
}

func (m *memoSource) GetArtistAlbums(ctx context.Context, id string, groups []string) ([]catalog.Release, error) {
	return memo(m, m.albums, id+"|"+strings.Join(groups, ","), func() ([]catalog.Release, error) {
		if m.inner == nil {
			return nil, artists.ErrCacheMiss
		}
		return m.inner.GetArtistAlbums(ctx, id, groups)
	})
}

func (m *memoSource) GetAlbumTracks(ctx context.Context, id string) ([]catalog.Track, error) {
	return memo(m, m.tracks, id, func() ([]catalog.Track, error) {
		source, ok := m.inner.(interface {
			GetAlbumTracks(context.Context, string) ([]catalog.Track, error)
		})
		if !ok {
			return nil, artists.ErrCacheMiss
		}
		return source.GetAlbumTracks(ctx, id)
	})
}

func (m *memoSource) isLimited() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limited
}

func (m *memoSource) retryAfter() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wait
}

// notices reports a rate limit seen by any read on the page, once.
func (m *memoSource) notices() []Notice {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.limited {
		return nil
	}
	return []Notice{{Level: "warn", Source: "spotify", Message: "Spotify answered 429 (rate limited). Cards that could not load are incomplete; wait and reload instead of retrying in a loop.", RetryAfter: m.wait}}
}
