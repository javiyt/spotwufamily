package httpadmin_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	rzOriginals  = "0FCYBhXjpfQIQWNKiVEDg5"
	rzReal       = "7CrVv4zLga71gdlMOMfjXL"
	rzMixed      = "2gKwx0Lm0JoOrN2mY9Z0PG"
	rzNewcomer   = "5rN8PqXk2mTz9LwYc3VbHa"
	rzSecretVal  = "spotify-client-secret-do-not-leak-0123"
	rzMBEvidence = "https://musicbrainz.org/artist/e70e310e-8e92-4da7-9aba-071bc6032a34"
)

// fakeProfiles is the offline Spotify fixture: no network, no credentials.
type fakeProfiles struct {
	profiles map[string]catalog.ArtistCandidate
	albums   map[string][]catalog.Release
	err      error
	calls    atomic.Int64
}

func (f *fakeProfiles) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	f.calls.Add(1)
	if f.err != nil {
		return catalog.ArtistCandidate{}, f.err
	}
	p, ok := f.profiles[id]
	if !ok {
		return catalog.ArtistCandidate{}, artists.ErrSourceNotFound
	}
	return p, nil
}

func (f *fakeProfiles) GetArtistAlbums(_ context.Context, id string, groups []string) ([]catalog.Release, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []catalog.Release
	for _, r := range f.albums[id] {
		for _, g := range groups {
			if g == r.AlbumGroup {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

type fakeFinder struct {
	found []catalog.ArtistCandidate
	err   error
	calls atomic.Int64
}

func (f *fakeFinder) SearchArtistCandidates(context.Context, catalog.Artist) ([]catalog.ArtistCandidate, error) {
	f.calls.Add(1)
	return f.found, f.err
}

type stubMB struct {
	groups []artists.MusicBrainzReleaseGroup
	err    error
}

func (m stubMB) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return m.groups, m.err
}

type slowDown struct{ wait time.Duration }

func (e slowDown) Error() string             { return "HTTP 429" }
func (e slowDown) Unwrap() error             { return artists.ErrSourceRateLimited }
func (e slowDown) RetryAfter() time.Duration { return e.wait }

type resolveEnv struct {
	*editorEnv
	spotify *fakeProfiles
	finder  *fakeFinder
	logPath string
}

func newResolveEnv(t *testing.T, tweak func(*resolveEnv, *admin.ResolutionDeps)) *resolveEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "artists.yaml")
	store := yamlcatalog.NewStore()
	reject := func(id, why string) catalog.SpotifyProfile {
		return catalog.SpotifyProfile{ID: id, Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-10-20T08:20:12Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceSpotifyProfile, URL: "https://open.spotify.com/artist/" + id, Rationale: why}}}
	}
	mb := []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: rzMBEvidence, Rationale: "credited with Method Man"}}
	seed := catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "streetlife", Name: "Streetlife", SpotifyID: rzReal, Category: catalog.CategoryAffiliateArtist, Enabled: true, EditorialOrder: 74,
			SpotifyProfiles: []catalog.SpotifyProfile{
				{ID: rzReal, Status: catalog.ProfileVerified, Reviewer: "javi", ReviewedAt: "2026-10-20T08:20:12Z", Evidence: mb},
				reject(rzOriginals, "Streetlife Originals is a nu jazz act"),
			}},
		{Slug: "k-g-b", Name: "K.G.B.", Category: catalog.CategoryAffiliateGroup, EditorialOrder: 19,
			SpotifyProfiles: []catalog.SpotifyProfile{reject(rzMixed, "mixed homonym profile")}},
		{Slug: "newcomer", Name: "Newcomer", Category: catalog.CategoryAffiliateArtist, EditorialOrder: 40},
	}}
	require.NoError(t, store.Save(context.Background(), path, seed))

	env := &resolveEnv{
		spotify: &fakeProfiles{
			profiles: map[string]catalog.ArtistCandidate{
				rzNewcomer: {SpotifyID: rzNewcomer, Name: "Newcomer", Genres: []string{"hip hop"}, Followers: 99, Images: []catalog.Image{
					{URL: "https://i.scdn.co/image/abc123", Width: 160}, {URL: "https://evil.example/track.png", Width: 160}}},
				rzReal: {SpotifyID: rzReal, Name: "Street Life"},
			},
			albums: map[string][]catalog.Release{
				rzNewcomer: {{SpotifyID: "A1111111111111111111aa", Name: "First Light", ReleaseDate: "2020-05-01", AlbumGroup: "album", Artists: []catalog.ArtistCandidate{{SpotifyID: rzNewcomer}}}},
			},
		},
		finder:  &fakeFinder{},
		logPath: path + ".reviews.jsonl",
	}
	deps := admin.ResolutionDeps{
		Searcher: env.finder, Source: env.spotify, Cached: env.spotify, Live: true,
		Log: filesystem.NewReviewLog(env.logPath), Now: func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) },
		MusicBrainz: stubMB{groups: []artists.MusicBrainzReleaseGroup{{ID: "mb1", Title: "First Light", FirstReleaseDate: "2020-05-01", URL: "https://musicbrainz.org/release-group/mb1"}}},
	}
	if tweak != nil {
		tweak(env, &deps)
	}

	svc := admin.NewService(store, nil, func() bool { return true }, path).WithResolution(deps)
	srv, err := httpadmin.New(svc, httpadmin.Options{Addr: host, Token: token})
	require.NoError(t, err)
	h := srv.Handler()
	cookie := login(t, h)
	env.editorEnv = &editorEnv{t: t, path: path, h: h, cookie: cookie, csrf: csrfFor(t, h, cookie)}
	return env
}

func (e *resolveEnv) workbenchRevision(slug, query string) string {
	e.t.Helper()
	rec := e.get("/resolve/" + slug + query)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	m := revisionPattern.FindStringSubmatch(rec.Body.String())
	require.Len(e.t, m, 2, "decision forms must carry the record revision")
	return m[1]
}

func decision(slug, id, action, revision string, kv ...string) url.Values {
	v := url.Values{"spotify_id": {id}, "action": {action}, "revision": {revision}, "reviewer": {"javi"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func TestResolveRequiresSessionAndIsNotPublic(t *testing.T) {
	env := newResolveEnv(t, nil)
	for _, target := range []string{"/resolve", "/resolve/newcomer", "/api/v1/resolve", "/api/v1/resolve/newcomer"} {
		rec := do(t, env.h, http.MethodGet, target, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target)
	}
	rec := do(t, env.h, http.MethodPost, "/resolve/newcomer/apply", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestResolveQueuePageAndAPI(t *testing.T) {
	env := newResolveEnv(t, nil)
	rec := env.get("/resolve")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{"Previously rejected", "Unresolved", "/resolve/k-g-b", "/resolve/streetlife", "/resolve/newcomer"} {
		require.Contains(t, body, want)
	}
	// Nav shows the workbench when it is enabled.
	require.Contains(t, body, `href="/resolve"`)

	rec = env.get("/api/v1/resolve?filter=rejected")
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Items []struct {
			Slug  string   `json:"slug"`
			Flags []string `json:"flags"`
		} `json:"items"`
		Counts map[string]int `json:"counts"`
		Filter string         `json:"filter"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "rejected", out.Filter)
	require.Len(t, out.Items, 2)
	require.Equal(t, "k-g-b", out.Items[0].Slug, "editorial order, then slug")
	require.Equal(t, "streetlife", out.Items[1].Slug)
	require.Equal(t, 2, out.Counts["rejected"])
	require.Equal(t, 2, out.Counts["unresolved"])

	// Unknown filters are ignored rather than failing.
	require.Equal(t, http.StatusOK, env.get("/resolve?filter=bogus").Code)
}

func TestSeededStreetlifeAndKGBAreFlaggedNotSilentlyReplaced(t *testing.T) {
	env := newResolveEnv(t, nil)
	before := env.file()

	rec := env.get("/resolve/streetlife")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, rzReal)
	require.Contains(t, body, rzOriginals, "the rejected profile stays on the page")
	require.Contains(t, body, "Rejected and quarantined")
	require.Contains(t, body, "Streetlife Originals is a nu jazz act")

	rec = env.get("/resolve/k-g-b")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), rzMixed)
	require.Contains(t, rec.Body.String(), "No Spotify ID is linked yet")

	require.Equal(t, before, env.file(), "browsing never writes")
}

func TestEndToEndReviewFromUnknownIDToVerifiedWithoutCLI(t *testing.T) {
	env := newResolveEnv(t, nil)

	// Search by a pasted link: parsed locally, looked up by ID.
	link := "https://open.spotify.com/artist/" + rzNewcomer + "?si=abc"
	rev := env.workbenchRevision("newcomer", "?q="+url.QueryEscape(link))
	page := env.get("/resolve/newcomer?q=" + url.QueryEscape(link)).Body.String()
	require.Contains(t, page, "First Light")
	require.Contains(t, page, "https://i.scdn.co/image/abc123")
	require.NotContains(t, page, "evil.example")
	require.Zero(t, env.finder.calls.Load(), "an ID lookup is not a name search")

	// Preview, then confirm: accept as a candidate.
	rec := env.post("/resolve/newcomer/preview", decision("newcomer", rzNewcomer, "accept_primary", rev))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Downstream impact")
	require.Contains(t, rec.Body.String(), "Nothing has been saved yet")
	require.NotContains(t, env.file(), rzNewcomer, "a preview never writes")

	confirm := decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")
	rec = env.post("/resolve/newcomer/apply", confirm)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	file := env.file()
	require.Contains(t, file, rzNewcomer)
	require.Contains(t, file, "candidate")

	// The page now shows it as linked, unverified: nothing is auto-enabled or auto-verified.
	page = env.get("/resolve/newcomer").Body.String()
	require.Contains(t, page, "Linked profiles (1)")
	require.Contains(t, page, "candidate")

	// Verify with independent evidence supplied by the reviewer, and enable explicitly.
	rev = env.workbenchRevision("newcomer", "")
	verify := decision("newcomer", rzNewcomer, "verify", rev,
		"evidence_type", "musicbrainz", "evidence_url", rzMBEvidence, "evidence_rationale", "credited on a Wu release", "enable", "1")
	rec = env.post("/resolve/newcomer/preview", verify)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "The artist will be enabled")
	verify.Set("confirm", "1")
	rec = env.post("/resolve/newcomer/apply", verify)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	got, err := yamlcatalog.NewStore().Load(context.Background(), env.path)
	require.NoError(t, err)
	a := got.Artists[got.FindArtist("newcomer")]
	require.True(t, a.Enabled)
	require.Equal(t, []string{rzNewcomer}, a.SyncableSpotifyIDs(got.ProfileSyncPolicy))
	p, _ := a.ProfileFor(rzNewcomer)
	require.Equal(t, catalog.ProfileVerified, p.Status)
	require.Equal(t, "javi", p.Reviewer)
	require.Equal(t, "2026-10-20T12:00:00Z", p.ReviewedAt)
	require.Empty(t, catalog.ValidateEditorialCatalog(got))

	// Both decisions were appended to the audit trail.
	f, err := os.Open(env.logPath)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var actions []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line))
		actions = append(actions, line["action"].(string))
		require.Equal(t, "javi", line["actor"])
	}
	require.Equal(t, []string{"accept_primary", "verify"}, actions)
}

func TestVerifyWithoutIndependentEvidenceIsRefusedOverHTTP(t *testing.T) {
	env := newResolveEnv(t, nil)
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	// First link it as a candidate.
	require.Equal(t, http.StatusSeeOther, env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")).Code)
	before := env.file()

	rev = env.workbenchRevision("newcomer", "")
	bad := decision("newcomer", rzNewcomer, "verify", rev, "override_reason", "I am sure", "confirm", "1")
	rec := env.post("/resolve/newcomer/apply", bad)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "independent evidence")
	require.Equal(t, before, env.file())
}

func TestContradictingEvidenceAsksForAnAuditedOverride(t *testing.T) {
	env := newResolveEnv(t, func(_ *resolveEnv, d *admin.ResolutionDeps) {
		d.MusicBrainz = stubMB{groups: []artists.MusicBrainzReleaseGroup{{ID: "mb9", Title: "Totally Different", FirstReleaseDate: "1999-01-01", URL: "https://musicbrainz.org/release-group/mb9"}}}
	})
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	v := decision("newcomer", rzNewcomer, "accept_primary", rev,
		"verify", "1", "evidence_type", "musicbrainz", "evidence_url", rzMBEvidence, "evidence_rationale", "credited on a Wu release")
	rec := env.post("/resolve/newcomer/preview", v)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "Override reason")
	require.Contains(t, rec.Body.String(), "Preview again")

	v.Set("override_reason", "MusicBrainz titles the same release differently")
	rec = env.post("/resolve/newcomer/preview", v)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v.Set("confirm", "1")
	rec = env.post("/resolve/newcomer/apply", v)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Contains(t, env.file(), "Audited override by javi")
}

func TestNameSearchAndGateOverHTTP(t *testing.T) {
	env := newResolveEnv(t, func(e *resolveEnv, _ *admin.ResolutionDeps) {
		e.finder.found = []catalog.ArtistCandidate{{SpotifyID: rzNewcomer, Name: "Newcomer", Genres: []string{"hip hop"}}}
	})
	rec := env.get("/resolve/newcomer?q=Newcomer")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.EqualValues(t, 1, env.finder.calls.Load())
	require.Contains(t, body, "Evidence gate")
	require.NotContains(t, body, "approved:", "a name match alone is never approved")
	require.Contains(t, body, "no independent strong evidence")

	rec = env.get("/api/v1/resolve/newcomer?q=Newcomer")
	var out struct {
		Candidates []struct {
			SpotifyID  string   `json:"spotify_id"`
			GateStatus string   `json:"gate_status"`
			Actions    []string `json:"actions"`
		} `json:"candidates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Candidates, 1)
	require.Equal(t, "needs_review", out.Candidates[0].GateStatus)
	require.Contains(t, out.Candidates[0].Actions, "accept_primary")
}

func TestUnsafeReferencesAreNeverFetched(t *testing.T) {
	env := newResolveEnv(t, nil)
	for _, q := range []string{"https://evil.example/artist/" + rzNewcomer, "http://169.254.169.254/latest/meta-data/", "file:///etc/passwd", "spotify:album:" + rzNewcomer} {
		rec := env.get("/resolve/newcomer?q=" + url.QueryEscape(q))
		require.Equal(t, http.StatusOK, rec.Code, q)
		require.Contains(t, rec.Body.String(), "not a Spotify artist link", q)
	}
	require.Zero(t, env.spotify.calls.Load()+env.finder.calls.Load(), "nothing was looked up")
}

func TestRateLimitAndOutageDegradeToNotices(t *testing.T) {
	t.Run("spotify 429 with Retry-After", func(t *testing.T) {
		env := newResolveEnv(t, func(e *resolveEnv, _ *admin.ResolutionDeps) {
			e.spotify.err = slowDown{wait: 42 * time.Second}
			e.finder.err = slowDown{wait: 42 * time.Second}
		})
		rec := env.get("/resolve/streetlife?q=Streetlife")
		require.Equal(t, http.StatusOK, rec.Code, "an upstream 429 must not become a server error")
		require.Contains(t, rec.Body.String(), "42 s")
		require.NotContains(t, rec.Body.String(), "supported by evidence")

		rec = env.get("/api/v1/resolve/newcomer?q=Newcomer")
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct {
			Notices []struct {
				Source            string `json:"source"`
				RetryAfterSeconds int    `json:"retry_after_seconds"`
			} `json:"notices"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		var seen bool
		for _, n := range out.Notices {
			seen = seen || (n.Source == "spotify" && n.RetryAfterSeconds == 42)
		}
		require.True(t, seen, "%s", rec.Body.String())
	})

	t.Run("musicbrainz 503", func(t *testing.T) {
		env := newResolveEnv(t, func(_ *resolveEnv, d *admin.ResolutionDeps) { d.MusicBrainz = stubMB{err: errors.New("HTTP 503")} })
		rec := env.get("/resolve/newcomer?q=" + rzNewcomer)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "unavailable")
		require.Contains(t, rec.Body.String(), "not a contradiction")
	})

	t.Run("offline without credentials", func(t *testing.T) {
		env := newResolveEnv(t, func(_ *resolveEnv, d *admin.ResolutionDeps) { d.Searcher, d.Live, d.MusicBrainz = nil, false, nil })
		rec := env.get("/resolve/newcomer")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "Spotify is not configured")
		rec = env.get("/resolve/newcomer?q=Newcomer")
		require.Contains(t, rec.Body.String(), "Name search needs Spotify credentials")
	})
}

func TestDecisionsNeedCSRFAndSameOrigin(t *testing.T) {
	env := newResolveEnv(t, nil)
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	before := env.file()

	// Missing or wrong CSRF token.
	for _, token := range []string{"", "forged"} {
		form := decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")
		form.Set("csrf_token", token)
		req, _ := http.NewRequest(http.MethodPost, "/resolve/newcomer/apply", strings.NewReader(form.Encode()))
		rec := serve(env.h, req, env.cookie, "http://"+host)
		require.Equal(t, http.StatusForbidden, rec.Code, "token %q", token)
	}
	// Cross-origin submission, even with a valid token.
	form := decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")
	form.Set("csrf_token", env.csrf)
	req, _ := http.NewRequest(http.MethodPost, "/resolve/newcomer/apply", strings.NewReader(form.Encode()))
	require.Equal(t, http.StatusForbidden, serve(env.h, req, env.cookie, "https://evil.example").Code)
	req, _ = http.NewRequest(http.MethodPost, "/resolve/newcomer/preview", strings.NewReader(form.Encode()))
	require.Equal(t, http.StatusForbidden, serve(env.h, req, env.cookie, "https://evil.example").Code)

	require.Equal(t, before, env.file())

	// A decision without explicit confirmation is not applied.
	rec := env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, before, env.file())
}

func TestConcurrentEditIsReportedAsConflict(t *testing.T) {
	env := newResolveEnv(t, nil)
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	// Someone else edits the record.
	editRev := env.revision("newcomer")
	require.Equal(t, http.StatusSeeOther, env.post("/artists/newcomer", artistForm("newcomer", editRev, "name", "Newcomer", "notes", "changed elsewhere")).Code)

	rec := env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.NotContains(t, env.file(), rzNewcomer)
}

func TestProfileDataIsEscapedAndNoCredentialsLeak(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_SECRET", rzSecretVal)
	t.Setenv("SPOTIFY_CLIENT_ID", "client-id-do-not-leak")
	xss := `<script>alert(1)</script><img src=x onerror=alert(2)>`
	env := newResolveEnv(t, func(e *resolveEnv, _ *admin.ResolutionDeps) {
		e.spotify.profiles[rzNewcomer] = catalog.ArtistCandidate{SpotifyID: rzNewcomer, Name: xss, Genres: []string{xss}}
		e.spotify.albums[rzNewcomer][0].Name = xss
		e.finder.found = []catalog.ArtistCandidate{{SpotifyID: rzNewcomer, Name: xss}}
	})

	targets := []string{
		"/resolve", "/resolve?q=" + url.QueryEscape(xss), "/resolve/newcomer", "/resolve/newcomer?q=" + url.QueryEscape(xss),
		"/resolve/newcomer?q=" + rzNewcomer, "/resolve/streetlife", "/resolve/k-g-b", "/api/v1/resolve", "/api/v1/resolve/newcomer?q=" + rzNewcomer,
		"/review", "/operations", "/",
	}
	for _, target := range targets {
		rec := env.get(target)
		body := rec.Body.String()
		require.NotContains(t, body, "<script>alert(1)", target)
		require.NotContains(t, body, "<img src=x", target)
		require.NotContains(t, body, rzSecretVal, target)
		require.NotContains(t, body, "client-id-do-not-leak", target)
		require.NotContains(t, body, token, target)
		require.NotContains(t, rec.Header().Get("Set-Cookie"), rzSecretVal)
	}

	// The hostile name is rendered, but inert.
	body := env.get("/resolve/newcomer?q=" + rzNewcomer).Body.String()
	require.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")

	// A hostile note is echoed on the confirmation page escaped.
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	rec := env.post("/resolve/newcomer/preview", decision("newcomer", rzNewcomer, "inconclusive", rev, "note", xss))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "<script>alert(1)")
	require.NotContains(t, rec.Body.String(), rzSecretVal)
}

func TestImagesComeOnlyFromTheSpotifyCDNAndCSPAllowsOnlyThat(t *testing.T) {
	env := newResolveEnv(t, nil)
	rec := env.get("/resolve/newcomer?q=" + rzNewcomer)
	csp := rec.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "img-src 'self' data: https://*.scdn.co https://*.spotifycdn.com")
	require.NotContains(t, csp, "img-src *")
	require.NotContains(t, csp, "unsafe")
	require.Contains(t, rec.Body.String(), `referrerpolicy="no-referrer"`)
	require.NotContains(t, rec.Body.String(), "evil.example")
}

func TestDestructiveDecisionsOverHTTP(t *testing.T) {
	env := newResolveEnv(t, nil)
	rev := env.workbenchRevision("streetlife", "")

	// Rejecting the only verified ID previews the downstream effect first.
	rec := env.post("/resolve/streetlife/preview", decision("streetlife", rzReal, "reject", rev, "note", "wrong act"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, "The artist has no Spotify ID left")
	require.Contains(t, body, "will stop ingesting")
	require.Contains(t, body, "artists reconcile --artist streetlife")
	require.Contains(t, env.file(), rzReal)

	// A rejection without a note is refused.
	rec = env.post("/resolve/streetlife/preview", decision("streetlife", rzReal, "reject", rev))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// A rejected ID cannot be brought back by a name match.
	rec = env.post("/resolve/streetlife/apply", decision("streetlife", rzOriginals, "add_secondary", rev, "confirm", "1"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "independent evidence")
}

// serve sends a form POST with the given session cookie and Origin.
func serve(h http.Handler, req *http.Request, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNullOriginIsAcceptedOnlyWhenTheBrowserSaysSameOrigin(t *testing.T) {
	env := newResolveEnv(t, nil)
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	form := decision("newcomer", rzNewcomer, "accept_primary", rev)
	form.Set("csrf_token", env.csrf)

	post := func(fetchSite string) int {
		req, _ := http.NewRequest(http.MethodPost, "/resolve/newcomer/preview", strings.NewReader(form.Encode()))
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		return serve(env.h, req, env.cookie, "null").Code
	}
	require.Equal(t, http.StatusOK, post("same-origin"))
	require.Equal(t, http.StatusForbidden, post("cross-site"))
	require.Equal(t, http.StatusForbidden, post(""), "a bare null origin is not proof")
}
