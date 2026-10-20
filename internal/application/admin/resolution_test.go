package admin_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	streetOriginals = "0FCYBhXjpfQIQWNKiVEDg5" // rejected homonym profile
	streetReal      = "7CrVv4zLga71gdlMOMfjXL" // verified replacement
	kgbMixed        = "2gKwx0Lm0JoOrN2mY9Z0PG" // quarantined homonym
	kgbCandidate    = "7Jab8mHFYj7jSkFwKtPpxB" // unverified candidate
	wuID            = "34Dl2Pj3d2hTpkb1VtkhVe"
	newcomerID      = "5rN8PqXk2mTz9LwYc3VbHa"
)

var resolveNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// fakeSpotify is an offline Spotify/cache fixture with injectable failures.
type fakeSpotify struct {
	mu      sync.Mutex
	profile map[string]catalog.ArtistCandidate
	albums  map[string][]catalog.Release
	tracks  map[string][]catalog.Track
	err     error // returned by every read when set
	calls   int
}

func (f *fakeSpotify) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return catalog.ArtistCandidate{}, f.err
	}
	p, ok := f.profile[id]
	if !ok {
		return catalog.ArtistCandidate{}, artists.ErrSourceNotFound
	}
	return p, nil
}

func (f *fakeSpotify) GetArtistAlbums(_ context.Context, id string, groups []string) ([]catalog.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []catalog.Release
	for _, r := range f.albums[id] {
		if len(groups) == 0 || contains(groups, r.AlbumGroup) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeSpotify) GetAlbumTracks(_ context.Context, id string) ([]catalog.Track, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.tracks[id], nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

type fakeSearcher struct {
	candidates []catalog.ArtistCandidate
	err        error
	queries    []string
}

func (f *fakeSearcher) SearchArtistCandidates(_ context.Context, a catalog.Artist) ([]catalog.ArtistCandidate, error) {
	f.queries = append(f.queries, a.Name)
	return f.candidates, f.err
}

type fakeMB struct {
	groups []artists.MusicBrainzReleaseGroup
	err    error
}

func (f fakeMB) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return f.groups, f.err
}

type memLog struct {
	entries []admin.ReviewLogEntry
	err     error
}

func (m *memLog) Append(_ context.Context, e admin.ReviewLogEntry) error {
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, e)
	return nil
}

type rateLimited struct{ wait time.Duration }

func (e rateLimited) Error() string             { return "Spotify rate limit exceeded" }
func (e rateLimited) Unwrap() error             { return artists.ErrSourceRateLimited }
func (e rateLimited) RetryAfter() time.Duration { return e.wait }

func release(id, name, date string, owner string) catalog.Release {
	return catalog.Release{
		SpotifyID: id, Name: name, ReleaseDate: date, AlbumGroup: "album", URL: "https://open.spotify.com/album/" + id,
		Artists: []catalog.ArtistCandidate{{SpotifyID: owner, Name: "x"}},
	}
}

func seededCatalog() catalog.EditorialCatalog {
	reject := func(id, why string) catalog.SpotifyProfile {
		return catalog.SpotifyProfile{
			ID: id, Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-10-20T08:20:12Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceSpotifyProfile, URL: "https://open.spotify.com/artist/" + id, Rationale: why}},
		}
	}
	mb := []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/e70e310e-8e92-4da7-9aba-071bc6032a34", Rationale: "credited with Method Man"}}
	return catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", SpotifyID: wuID, Category: catalog.CategoryCore, Enabled: true, EditorialOrder: 1,
			SpotifyProfiles: []catalog.SpotifyProfile{{ID: wuID, Status: catalog.ProfileVerified, Reviewer: "javi", ReviewedAt: "2026-10-01T00:00:00Z", Evidence: mb}}},
		{Slug: "streetlife", Name: "Streetlife", SpotifyID: streetReal, Category: catalog.CategoryAffiliateArtist, Enabled: true, EditorialOrder: 74,
			SpotifyProfiles: []catalog.SpotifyProfile{
				{ID: streetReal, Status: catalog.ProfileVerified, Reviewer: "javi", ReviewedAt: "2026-10-20T08:20:12Z", Evidence: mb},
				reject(streetOriginals, "Streetlife Originals is a nu jazz act"),
			}},
		{Slug: "k-g-b", Name: "K.G.B.", Category: catalog.CategoryAffiliateGroup, EditorialOrder: 19,
			SpotifyProfiles: []catalog.SpotifyProfile{reject(kgbMixed, "mixed homonym profile")}},
		{Slug: "multi", Name: "Multi", SpotifyID: "1111111111111111111111", SpotifyIDs: []string{"2222222222222222222222"}, Category: catalog.CategoryAffiliateArtist, EditorialOrder: 30},
		{Slug: "newcomer", Name: "Newcomer", Category: catalog.CategoryAffiliateArtist, EditorialOrder: 40},
		{Slug: "pending", Name: "Pending One", SpotifyID: "3333333333333333333333", Category: catalog.CategoryAffiliateArtist,
			SpotifyProfiles: []catalog.SpotifyProfile{{ID: "3333333333333333333333", Status: catalog.ProfileNeedsReview}}},
	}}
}

type resolveFixture struct {
	store    *memStore
	svc      admin.Service
	spotify  *fakeSpotify
	searcher *fakeSearcher
	log      *memLog
}

func newResolveFixture(t *testing.T, mutate func(*resolveFixture, *admin.ResolutionDeps)) *resolveFixture {
	t.Helper()
	f := &resolveFixture{
		store: &memStore{c: seededCatalog()},
		spotify: &fakeSpotify{
			profile: map[string]catalog.ArtistCandidate{
				newcomerID:      {SpotifyID: newcomerID, Name: "Newcomer", Genres: []string{"hip hop"}, Followers: 1200, Images: []catalog.Image{{URL: "https://i.scdn.co/image/abc", Width: 160}, {URL: "https://evil.example/x.png", Width: 160}}},
				streetOriginals: {SpotifyID: streetOriginals, Name: "Streetlife Originals", Genres: []string{"nu jazz"}},
				streetReal:      {SpotifyID: streetReal, Name: "Street Life", Genres: []string{"hip hop"}},
				wuID:            {SpotifyID: wuID, Name: "Wu-Tang Clan"},
			},
			albums: map[string][]catalog.Release{
				newcomerID: {release("A1111111111111111111aa", "First Light", "2020-05-01", newcomerID)},
				wuID:       {release("A2222222222222222222aa", "Enter the Wu-Tang", "1993-11-09", wuID)},
			},
			tracks: map[string][]catalog.Track{
				"A1111111111111111111aa": {{SpotifyID: "T1", Name: "Opener", Artists: []catalog.ArtistCandidate{{SpotifyID: newcomerID, Name: "Newcomer"}, {SpotifyID: wuID, Name: "Wu-Tang Clan"}}}},
			},
		},
		searcher: &fakeSearcher{},
		log:      &memLog{},
	}
	deps := admin.ResolutionDeps{Searcher: f.searcher, Source: f.spotify, Cached: f.spotify, Log: f.log, Live: true, Now: func() time.Time { return resolveNow }}
	if mutate != nil {
		mutate(f, &deps)
	}
	f.svc = admin.NewService(f.store, nil, nil, "data/artists.yaml").WithResolution(deps)
	return f
}

func slugs(rows []admin.QueueRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Slug)
	}
	return out
}

func TestResolveQueueFlagsFiltersAndOrdering(t *testing.T) {
	f := newResolveFixture(t, nil)
	ctx := context.Background()

	page, err := f.svc.ResolveQueue(ctx, admin.ResolveQueueQuery{})
	require.NoError(t, err)
	// Editorial order, unordered entries last; wu-tang-clan is fully verified.
	require.Equal(t, []string{"k-g-b", "multi", "newcomer", "streetlife", "pending"}, slugs(page.Page.Items))
	require.Equal(t, admin.QueueCounts{All: 5, Unresolved: 2, NeedsReview: 1, Unverified: 1, Rejected: 2, Multiple: 1}, page.Counts)

	again, err := f.svc.ResolveQueue(ctx, admin.ResolveQueueQuery{})
	require.NoError(t, err)
	require.Equal(t, page.Page.Items, again.Page.Items, "ordering must be stable")

	for filter, want := range map[admin.QueueFilter][]string{
		admin.QueueRejected:    {"k-g-b", "streetlife"},
		admin.QueueUnresolved:  {"k-g-b", "newcomer"},
		admin.QueueMultiple:    {"multi"},
		admin.QueueNeedsReview: {"pending"},
		admin.QueueUnverified:  {"multi"},
	} {
		got, err := f.svc.ResolveQueue(ctx, admin.ResolveQueueQuery{Filter: filter})
		require.NoError(t, err)
		require.Equal(t, want, slugs(got.Page.Items), string(filter))
		require.Equal(t, page.Counts, got.Counts, "counts ignore the filter")
	}

	// Searching reaches artists that are not flagged, so any artist can be reviewed.
	found, err := f.svc.ResolveQueue(ctx, admin.ResolveQueueQuery{ListQuery: admin.ListQuery{Search: "wu"}})
	require.NoError(t, err)
	require.Equal(t, []string{"wu-tang-clan"}, slugs(found.Page.Items))
	require.Empty(t, found.Page.Items[0].Flags)
}

func TestResolveQueueSuspectComesFromCachedEvidence(t *testing.T) {
	f := newResolveFixture(t, func(f *resolveFixture, _ *admin.ResolutionDeps) {
		// The same Spotify ID linked from two artists is ambiguous ownership.
		f.store.c.Artists[3].SpotifyIDs = []string{wuID}
	})
	page, err := f.svc.ResolveQueue(context.Background(), admin.ResolveQueueQuery{Filter: admin.QueueSuspect})
	require.NoError(t, err)
	require.True(t, page.SuspectKnown)
	require.Equal(t, []string{"wu-tang-clan", "multi"}, slugs(page.Page.Items))
	require.Equal(t, 2, page.Counts.Suspect)

	noCache := newResolveFixture(t, func(_ *resolveFixture, d *admin.ResolutionDeps) { d.Cached = nil })
	page, err = noCache.svc.ResolveQueue(context.Background(), admin.ResolveQueueQuery{})
	require.NoError(t, err)
	require.False(t, page.SuspectKnown, "without a cache the queue must not imply nothing is suspect")
}

func TestWorkbenchShowsAttachedAndRejectedHistory(t *testing.T) {
	f := newResolveFixture(t, nil)
	wb, err := f.svc.ResolveWorkbench(context.Background(), "streetlife", "")
	require.NoError(t, err)
	require.Len(t, wb.Attached, 1)
	require.Equal(t, streetReal, wb.Attached[0].SpotifyID)
	require.Equal(t, artists.ProfileRolePrimary, wb.Attached[0].Role)
	require.Len(t, wb.History, 1, "the seeded rejection stays visible instead of being silently replaced")
	h := wb.History[0]
	require.Equal(t, streetOriginals, h.SpotifyID)
	require.Equal(t, catalog.ProfileRejected, h.Status)
	require.Equal(t, "javi", h.Reviewer)
	require.Contains(t, h.Evidence[0].Rationale, "nu jazz")
	require.False(t, wb.Searched)
	require.Empty(t, f.searcher.queries, "opening a page must not search Spotify")
}

func TestWorkbenchEachSecondaryIDGetsItsOwnCard(t *testing.T) {
	f := newResolveFixture(t, nil)
	wb, err := f.svc.ResolveWorkbench(context.Background(), "multi", "")
	require.NoError(t, err)
	require.Len(t, wb.Attached, 2)
	require.Equal(t, "1111111111111111111111", wb.Attached[0].SpotifyID)
	require.Equal(t, artists.ProfileRolePrimary, wb.Attached[0].Role)
	require.Equal(t, "2222222222222222222222", wb.Attached[1].SpotifyID)
	require.Equal(t, artists.ProfileRoleSecondary, wb.Attached[1].Role)
}

func TestWorkbenchManualIDCardHasEvidenceAndSafeImage(t *testing.T) {
	f := newResolveFixture(t, func(_ *resolveFixture, d *admin.ResolutionDeps) {
		d.MusicBrainz = fakeMB{groups: []artists.MusicBrainzReleaseGroup{{ID: "mb1", Title: "First Light", FirstReleaseDate: "2020-05-01", URL: "https://musicbrainz.org/release-group/mb1"}}}
	})
	wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", "https://open.spotify.com/artist/"+newcomerID+"?si=zz")
	require.NoError(t, err)
	require.Len(t, wb.Candidates, 1)
	card := wb.Candidates[0]
	require.True(t, card.Manual)
	require.Equal(t, newcomerID, card.SpotifyID)
	require.Equal(t, "spotify:artist:"+newcomerID, card.URI)
	require.Equal(t, "https://open.spotify.com/artist/"+newcomerID, card.URL)
	require.Equal(t, "https://i.scdn.co/image/abc", card.ImageURL, "only the Spotify CDN image is allowed")
	require.Equal(t, 1200, card.Followers)
	require.Len(t, card.Albums, 1)
	require.Len(t, card.Tracks, 1)
	require.Equal(t, []string{"Newcomer", "Wu-Tang Clan"}, card.Tracks[0].Credits)
	require.Equal(t, artists.SourceOK, card.Audit.MusicBrainz.Status)
	require.NotEmpty(t, card.Suggestions, "a MusicBrainz release match is offered as independent evidence")
	require.Equal(t, catalog.EvidenceMusicBrainz, card.Suggestions[0].Type)
	// A manual ID is never pre-verified or auto-approved.
	require.Equal(t, catalog.ProfileUnreviewed, card.Status)
	require.Nil(t, card.Gate)
	names := []string{}
	for _, a := range card.Actions {
		names = append(names, a.Name)
	}
	require.Equal(t, []string{"accept_primary", "add_secondary", "inconclusive", "reject"}, names)
	require.Empty(t, f.searcher.queries, "an ID is looked up, never name-searched")
}

func TestWorkbenchRejectsUnsafeReferencesWithoutFetching(t *testing.T) {
	f := newResolveFixture(t, nil)
	for _, q := range []string{"https://evil.example/artist/" + newcomerID, "http://169.254.169.254/latest/meta-data", "spotify:album:" + newcomerID, "file:///etc/passwd"} {
		wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", q)
		require.NoError(t, err, q)
		require.Empty(t, wb.Candidates, q)
		require.NotEmpty(t, wb.Notices, q)
	}
	require.Zero(t, f.spotify.calls, "nothing was fetched")
	require.Empty(t, f.searcher.queries)
}

func TestWorkbenchNameSearchUsesSharedRankingAndGate(t *testing.T) {
	f := newResolveFixture(t, func(f *resolveFixture, _ *admin.ResolutionDeps) {
		f.searcher.candidates = []catalog.ArtistCandidate{
			{SpotifyID: newcomerID, Name: "Newcomer", Genres: []string{"hip hop"}},
			{SpotifyID: streetOriginals, Name: "Newcomer Originals"},
			{SpotifyID: wuID, Name: "Newcomer"},
		}
	})
	wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", "Newcomer")
	require.NoError(t, err)
	require.Equal(t, []string{"Newcomer"}, f.searcher.queries)
	require.NotEmpty(t, wb.Candidates)
	for _, c := range wb.Candidates {
		require.True(t, c.Ranked)
		require.NotNil(t, c.Gate)
		require.NotEqual(t, catalog.EligibilityApproved, c.Gate.Status, "no independent evidence, so nothing is approved: %+v", c.Gate)
		require.NotEmpty(t, c.Gate.Reasons)
	}
	// The wu-tang-clan profile is owned by another artist: no actions at all.
	for _, c := range wb.Candidates {
		if c.SpotifyID == wuID {
			require.Equal(t, "wu-tang-clan", c.Owner)
			require.Empty(t, c.Actions)
			require.NotEmpty(t, c.Warnings)
		}
	}
	// A previously rejected profile is flagged, not offered silently.
	for _, c := range wb.Candidates {
		if c.SpotifyID == streetOriginals {
			require.Equal(t, "streetlife", c.Owner)
		}
	}
}

func TestWorkbenchSourceFailuresAreNoticesNotVerdicts(t *testing.T) {
	t.Run("spotify rate limited surfaces Retry-After", func(t *testing.T) {
		f := newResolveFixture(t, func(f *resolveFixture, _ *admin.ResolutionDeps) {
			f.spotify.err = rateLimited{wait: 42 * time.Second}
			f.searcher.err = rateLimited{wait: 42 * time.Second}
		})
		wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", "Newcomer")
		require.NoError(t, err)
		var found bool
		for _, n := range wb.Notices {
			if n.RetryAfter == 42*time.Second {
				found = true
			}
		}
		require.True(t, found, "notices: %+v", wb.Notices)

		wb, err = f.svc.ResolveWorkbench(context.Background(), "multi", "")
		require.NoError(t, err)
		for _, card := range wb.Attached {
			require.Equal(t, artists.ClassUnavailable, card.Audit.Classification)
			require.NotEqual(t, artists.ClassVerified, card.Audit.Classification, "no answer must never read as verified")
		}
	})

	t.Run("musicbrainz 503 is missing evidence", func(t *testing.T) {
		f := newResolveFixture(t, func(_ *resolveFixture, d *admin.ResolutionDeps) {
			d.MusicBrainz = fakeMB{err: errors.New("HTTP 503")}
		})
		wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", newcomerID)
		require.NoError(t, err)
		card := wb.Candidates[0]
		require.Equal(t, artists.SourceUnavailable, card.Audit.MusicBrainz.Status)
		// A failed comparison is neither a contradiction nor support.
		require.NotEqual(t, artists.ClassSuspicious, card.Audit.Classification)
		require.Contains(t, strings.Join(card.Audit.MissingEvidence, " "), "MusicBrainz")
		require.Empty(t, card.Suggestions)
	})

	t.Run("offline without credentials still serves cached cards", func(t *testing.T) {
		f := newResolveFixture(t, func(_ *resolveFixture, d *admin.ResolutionDeps) {
			d.Searcher, d.Live, d.MusicBrainz = nil, false, nil
		})
		wb, err := f.svc.ResolveWorkbench(context.Background(), "wu-tang-clan", "Wu Tang")
		require.NoError(t, err)
		require.False(t, wb.SearchAvailable)
		require.Len(t, wb.Attached, 1)
		require.NotEmpty(t, wb.Notices)
		require.Equal(t, artists.SourceOffline, wb.Attached[0].Audit.MusicBrainz.Status)
	})

	t.Run("no catalog albums is reported, not concluded", func(t *testing.T) {
		f := newResolveFixture(t, nil)
		wb, err := f.svc.ResolveWorkbench(context.Background(), "streetlife", streetReal)
		require.NoError(t, err)
		card := wb.Attached[0]
		require.Zero(t, card.Audit.ReleaseCount)
		var warned bool
		for _, w := range card.Warnings {
			warned = warned || strings.Contains(w, "no albums")
		}
		require.True(t, warned, "%v", card.Warnings)
	})
}

func req(slug, id, action string) admin.ResolveRequest {
	return admin.ResolveRequest{Slug: slug, SpotifyID: id, Action: action, Reviewer: "javi", Confirm: true}
}

var mbEv = catalog.Evidence{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/e70e310e-8e92-4da7-9aba-071bc6032a34", Rationale: "credited with Method Man on Street Education"}

func TestResolutionEndToEndFromUnknownIDToVerified(t *testing.T) {
	f := newResolveFixture(t, nil)
	ctx := context.Background()

	// 1. open the artist and look up an unknown ID by link
	wb, err := f.svc.ResolveWorkbench(ctx, "newcomer", "spotify:artist:"+newcomerID)
	require.NoError(t, err)
	require.Len(t, wb.Candidates, 1)

	// 2. accept it as a candidate: linked, but sync ignores it
	r := req("newcomer", newcomerID, "accept_primary")
	r.Revision = wb.Revision
	res, err := f.svc.ApplyResolution(ctx, r)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileCandidate, res.Preview.Result.To)
	got := f.store.c.Artists[f.store.c.FindArtist("newcomer")]
	require.Equal(t, newcomerID, got.SpotifyID)
	require.False(t, got.Enabled, "acceptance never auto-enables")
	require.Empty(t, got.SyncableSpotifyIDs(f.store.c.ProfileSyncPolicy))

	// 3. verify with independent evidence and enable it explicitly
	wb, err = f.svc.ResolveWorkbench(ctx, "newcomer", "")
	require.NoError(t, err)
	v := req("newcomer", newcomerID, admin.ActionVerify)
	v.Revision, v.Evidence, v.Enable = wb.Revision, []catalog.Evidence{mbEv}, true
	res, err = f.svc.ApplyResolution(ctx, v)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileVerified, res.Preview.Result.To)
	got = f.store.c.Artists[f.store.c.FindArtist("newcomer")]
	require.True(t, got.Enabled)
	require.Equal(t, []string{newcomerID}, got.SyncableSpotifyIDs(f.store.c.ProfileSyncPolicy))
	p, _ := got.ProfileFor(newcomerID)
	require.Equal(t, "javi", p.Reviewer)
	require.Equal(t, "2026-10-20T12:00:00Z", p.ReviewedAt)
	require.Empty(t, catalog.ValidateEditorialCatalog(f.store.c))

	// 4. both decisions are in the audit trail
	require.Len(t, f.log.entries, 2)
	require.Equal(t, admin.ActionVerify, f.log.entries[1].Action)
	require.Equal(t, "javi", f.log.entries[1].Actor)
	require.Equal(t, []string{mbEv.URL}, f.log.entries[1].EvidenceURLs)
	require.Equal(t, []string{newcomerID}, f.log.entries[1].IDsAfter)
}

func TestResolutionVerifyWithoutIndependentEvidenceIsRefused(t *testing.T) {
	f := newResolveFixture(t, nil)
	r := req("multi", "1111111111111111111111", admin.ActionVerify)
	_, err := f.svc.ApplyResolution(context.Background(), r)
	require.ErrorIs(t, err, artists.ErrInvalidMapping)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)

	// An override reason is not a substitute for evidence either.
	r.OverrideReason = "I just know"
	_, err = f.svc.ApplyResolution(context.Background(), r)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	require.Equal(t, catalog.ProfileUnreviewed, f.store.c.Artists[3].StatusOf("1111111111111111111111"))
	require.Zero(t, f.store.saves)
	require.Empty(t, f.log.entries)
}

func TestResolutionContradictingEvidenceNeedsAuditedOverride(t *testing.T) {
	f := newResolveFixture(t, func(f *resolveFixture, d *admin.ResolutionDeps) {
		d.MusicBrainz = fakeMB{groups: []artists.MusicBrainzReleaseGroup{{ID: "mb1", Title: "Something Else Entirely", FirstReleaseDate: "2001-01-01", URL: "https://musicbrainz.org/release-group/mb1"}}}
	})
	r := req("newcomer", newcomerID, "accept_primary")
	r.Verify, r.Evidence = true, []catalog.Evidence{mbEv}
	_, err := f.svc.ApplyResolution(context.Background(), r)
	require.ErrorIs(t, err, admin.ErrOverrideRequired)
	require.Zero(t, f.store.saves)

	prev, err := f.svc.PreviewResolution(context.Background(), r)
	require.ErrorIs(t, err, admin.ErrOverrideRequired)
	require.True(t, prev.NeedsOverride)

	r.OverrideReason = "MusicBrainz lists the same release under a different title"
	res, err := f.svc.ApplyResolution(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileVerified, res.Preview.Result.To)
	require.Equal(t, r.OverrideReason, f.log.entries[0].OverrideReason)
	p, _ := f.store.c.Artists[f.store.c.FindArtist("newcomer")].ProfileFor(newcomerID)
	require.Contains(t, p.Evidence[len(p.Evidence)-1].Rationale, "Audited override by javi")
}

func TestResolutionNeedsConfirmationAndPreviewDoesNotWrite(t *testing.T) {
	f := newResolveFixture(t, nil)
	r := req("newcomer", newcomerID, "accept_primary")
	r.Confirm = false
	var confirm *admin.ConfirmationError
	_, err := f.svc.ApplyResolution(context.Background(), r)
	require.ErrorAs(t, err, &confirm)

	prev, err := f.svc.PreviewResolution(context.Background(), r)
	require.NoError(t, err)
	require.Zero(t, f.store.saves)
	require.Empty(t, f.log.entries)
	require.Equal(t, []string{newcomerID}, prev.Impact.LinkedAfter)
	require.Empty(t, prev.Impact.LinkedBefore)
	require.Empty(t, prev.Impact.SyncAdded, "a candidate is not synced")
	require.Contains(t, strings.Join(prev.Warnings, " "), "candidate")
	require.NotEmpty(t, prev.Changes)
}

func TestResolutionPreviewPredictsDownstreamImpact(t *testing.T) {
	f := newResolveFixture(t, nil)
	r := req("streetlife", streetReal, "reject")
	r.Note = "this is the wrong act after all"
	prev, err := f.svc.PreviewResolution(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, []string{streetReal}, prev.Impact.SyncRemoved)
	require.True(t, prev.Impact.EnabledBefore)
	require.False(t, prev.Impact.EnabledAfter, "the last ID is gone, so the artist is disabled")
	require.Contains(t, strings.Join(prev.Warnings, " "), "disabled")
	require.Contains(t, strings.Join(prev.Impact.Notes, " "), "artists reconcile --artist streetlife")
}

func TestResolutionReportsPendingReconciliation(t *testing.T) {
	store := &memStore{c: seededCatalog()}
	ops := &perArtistOps{orphans: map[string]map[string]int{"streetlife": {streetReal: 0}}}
	svc := admin.NewService(store, ops, nil, "data/artists.yaml").WithResolution(admin.ResolutionDeps{Now: func() time.Time { return resolveNow }})
	r := req("streetlife", streetReal, "remove")
	prev, err := svc.PreviewResolution(context.Background(), r)
	require.NoError(t, err)
	require.True(t, prev.Impact.ReconcileKnown)
	require.Equal(t, 0, prev.Impact.ReconcileBefore)
	require.Equal(t, 7, prev.Impact.ReconcileAfter)
	require.Contains(t, strings.Join(prev.Impact.Notes, " "), "7 more synced link(s)")
}

// perArtistOps reports 7 orphans for any artist that no longer links streetReal.
type perArtistOps struct{ orphans map[string]map[string]int }

func (perArtistOps) LastSync(context.Context) (admin.SyncInfo, bool, error) {
	return admin.SyncInfo{}, false, nil
}

func (perArtistOps) PendingReconciliation(_ context.Context, c catalog.EditorialCatalog) (int, error) {
	total := 0
	for _, a := range c.Artists {
		if a.Slug == "streetlife" && !a.HasSpotifyID(streetReal) {
			total += 7
		}
	}
	return total, nil
}

func TestResolutionNeverMergesAnotherArtistsProfile(t *testing.T) {
	f := newResolveFixture(t, nil)
	_, err := f.svc.ApplyResolution(context.Background(), req("newcomer", wuID, "add_secondary"))
	require.ErrorIs(t, err, artists.ErrInvalidMapping)
	require.Contains(t, err.Error(), "wu-tang-clan")
	_, err = f.svc.ApplyResolution(context.Background(), req("newcomer", streetOriginals, "add_secondary"))
	require.ErrorIs(t, err, artists.ErrInvalidMapping)
	require.Zero(t, f.store.saves)
}

func TestResolutionRejectKeepsRejectedProfileOutOfSyncAndCandidates(t *testing.T) {
	f := newResolveFixture(t, nil)
	r := req("multi", "2222222222222222222222", "reject")
	r.Note = "different artist with the same name"
	_, err := f.svc.ApplyResolution(context.Background(), r)
	require.NoError(t, err)
	got := f.store.c.Artists[f.store.c.FindArtist("multi")]
	require.Equal(t, []string{"1111111111111111111111"}, got.AllSpotifyIDs())
	require.True(t, f.store.c.IsRejectedSpotifyID("2222222222222222222222"))

	// The rejection cannot be undone by accepting it again without new independent evidence.
	_, err = f.svc.ApplyResolution(context.Background(), req("multi", "2222222222222222222222", "add_secondary"))
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)
}

func TestResolutionDetectsConcurrentEdit(t *testing.T) {
	f := newResolveFixture(t, nil)
	wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", "")
	require.NoError(t, err)
	f.store.c.Artists[f.store.c.FindArtist("newcomer")].Notes = "edited elsewhere"

	r := req("newcomer", newcomerID, "accept_primary")
	r.Revision = wb.Revision
	var conflict *admin.ConflictError
	_, err = f.svc.ApplyResolution(context.Background(), r)
	require.ErrorAs(t, err, &conflict)
	require.Zero(t, f.store.saves)
}

func TestResolutionAuditLogFailureDoesNotLoseTheDecision(t *testing.T) {
	f := newResolveFixture(t, nil)
	f.log.err = errors.New("disk full")
	res, err := f.svc.ApplyResolution(context.Background(), req("newcomer", newcomerID, "accept_primary"))
	require.NoError(t, err)
	require.NotEmpty(t, res.LogError)
	require.Equal(t, newcomerID, f.store.c.Artists[f.store.c.FindArtist("newcomer")].SpotifyID)
	require.NotContains(t, res.LogError, "disk full", "internal error text stays out of the UI")
}

func TestResolutionReorderQuarantineAndInconclusive(t *testing.T) {
	f := newResolveFixture(t, nil)
	ctx := context.Background()

	_, err := f.svc.ApplyResolution(ctx, req("multi", "2222222222222222222222", "reorder_primary"))
	require.NoError(t, err)
	require.Equal(t, "2222222222222222222222", f.store.c.Artists[3].SpotifyID)
	require.Equal(t, []string{"1111111111111111111111"}, f.store.c.Artists[3].SpotifyIDs)

	q := req("multi", "1111111111111111111111", "quarantine")
	q.Note = "mixed homonym"
	_, err = f.svc.ApplyResolution(ctx, q)
	require.NoError(t, err)
	require.Equal(t, []string{"2222222222222222222222"}, f.store.c.Artists[3].AllSpotifyIDs())
	require.Equal(t, catalog.ProfileNeedsReview, f.store.c.Artists[3].StatusOf("1111111111111111111111"))

	i := req("newcomer", newcomerID, "inconclusive")
	i.Note = "MusicBrainz was down"
	res, err := f.svc.ApplyResolution(ctx, i)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileNeedsReview, res.Preview.Result.To)
	require.Empty(t, res.Preview.Impact.SyncAdded)
	require.Empty(t, catalog.ValidateEditorialCatalog(f.store.c))
}

func TestResolutionRejectsOversizedAndInvalidInput(t *testing.T) {
	f := newResolveFixture(t, nil)
	r := req("newcomer", newcomerID, "reject")
	r.Note = strings.Repeat("x", 1001)
	_, err := f.svc.ApplyResolution(context.Background(), r)
	require.ErrorIs(t, err, artists.ErrInvalidMapping)

	r = req("newcomer", newcomerID, "accept_primary")
	r.Evidence = []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: "http://127.0.0.1/admin", Rationale: "x"}}
	_, err = f.svc.ApplyResolution(context.Background(), r)
	require.ErrorIs(t, err, artists.ErrInvalidMapping)

	_, err = f.svc.ApplyResolution(context.Background(), req("nobody", newcomerID, "accept_primary"))
	require.ErrorIs(t, err, admin.ErrNotFound)
	_, err = f.svc.ApplyResolution(context.Background(), req("newcomer", newcomerID, "teleport"))
	require.ErrorIs(t, err, artists.ErrInvalidMapping)
	require.Zero(t, f.store.saves)
}

func TestResolutionRequiresEditableStoreAndWorkbench(t *testing.T) {
	plain := admin.NewService(fakeStore{c: seededCatalog()}, nil, nil, "x").WithResolution(admin.ResolutionDeps{})
	_, err := plain.ApplyResolution(context.Background(), req("newcomer", newcomerID, "accept_primary"))
	require.ErrorIs(t, err, admin.ErrReadOnly)

	bare := admin.NewService(&memStore{c: seededCatalog()}, nil, nil, "x")
	require.False(t, bare.Resolvable())
	_, err = bare.ResolveQueue(context.Background(), admin.ResolveQueueQuery{})
	require.ErrorIs(t, err, admin.ErrReadOnly)
	_ = fmt.Sprint()
}
