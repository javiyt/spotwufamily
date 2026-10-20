package httpadmin_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func spotifyID(n int) string { return fmt.Sprintf("%022d", n) }

// editorEnv is the real stack: YAML file, store, service, HTTP server.
type editorEnv struct {
	t      *testing.T
	path   string
	h      http.Handler
	cookie *http.Cookie
	csrf   string
}

func newEditorEnv(t *testing.T) *editorEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artists.yaml")
	store := yamlcatalog.NewStore()
	seed := catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", SpotifyID: spotifyID(1), Category: catalog.CategoryCore,
				Roles: []catalog.Category{catalog.CategoryCore, catalog.CategoryAffiliateGroup}, Aliases: []string{"Wu Tang"},
				Enabled: true, EditorialOrder: 1, Genres: []string{"hardcore hip hop"},
				SpotifyProfiles: []catalog.SpotifyProfile{{ID: spotifyID(1), Status: catalog.ProfileVerified, Reviewer: "javi"}}},
			{Slug: "method-man", Name: "Method Man", SpotifyID: spotifyID(2), Category: catalog.CategoryCore, Aliases: []string{"Johnny Blaze"}, Enabled: true, EditorialOrder: 2},
			{Slug: "rza", Name: "RZA", Category: catalog.CategoryProducer, Aliases: []string{}},
			{Slug: "killarmy", Name: "Killarmy", Category: catalog.CategoryAffiliateGroup, Aliases: []string{}},
		},
		Relationships: []catalog.Relationship{{
			Type: catalog.RelMemberOf, From: "method-man", To: "wu-tang-clan", Status: catalog.RelationshipVerified,
			Confidence: catalog.ConfidenceHigh, Reviewer: "javi", ReviewedAt: "2026-01-02T00:00:00Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Method_Man", Rationale: "founding member"}},
		}},
	}
	require.NoError(t, store.Save(context.Background(), path, seed))

	svc := admin.NewService(store, nil, nil, path)
	srv, err := httpadmin.New(svc, httpadmin.Options{Addr: host, Token: token})
	require.NoError(t, err)
	h := srv.Handler()
	cookie := login(t, h)
	return &editorEnv{t: t, path: path, h: h, cookie: cookie, csrf: csrfFor(t, h, cookie)}
}

func (e *editorEnv) get(target string) *httptest.ResponseRecorder {
	e.t.Helper()
	return do(e.t, e.h, http.MethodGet, target, withCookie(e.cookie))
}

func (e *editorEnv) post(target string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	if form.Get("csrf_token") == "" {
		form.Set("csrf_token", e.csrf)
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+host)
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *editorEnv) file() string {
	e.t.Helper()
	raw, err := os.ReadFile(e.path)
	require.NoError(e.t, err)
	return string(raw)
}

func (e *editorEnv) revision(slug string) string {
	e.t.Helper()
	rec := e.get("/artists/" + slug)
	if rec.Code == http.StatusSeeOther {
		rec = e.get(rec.Header().Get("Location"))
	}
	m := revisionPattern.FindStringSubmatch(rec.Body.String())
	require.Len(e.t, m, 2, "edit form must embed the record revision")
	return m[1]
}

var revisionPattern = regexpMust(`name="revision" value="([^"]+)"`)

func artistForm(slug, revision string, kv ...string) url.Values {
	v := url.Values{"slug": {slug}, "revision": {revision}, "category": {"core"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func TestListFiltersSortsAndShowsBadges(t *testing.T) {
	e := newEditorEnv(t)

	body := e.get("/artists").Body.String()
	for _, want := range []string{"Wu-Tang Clan", "Method Man", "RZA", "Killarmy", "4 artists", "2 unresolved"} {
		require.Contains(t, body, want)
	}

	body = e.get("/artists?category=producer").Body.String()
	require.Contains(t, body, "RZA")
	require.NotContains(t, body, "Method Man")

	body = e.get("/artists?q=johnny").Body.String()
	require.Contains(t, body, "Method Man", "aliases are searched")
	require.NotContains(t, body, "Killarmy")

	body = e.get("/artists?ids=unresolved&enabled=no").Body.String()
	require.Contains(t, body, "RZA")
	require.NotContains(t, body, "Method Man")

	body = e.get("/artists?rel=member_of").Body.String()
	require.Contains(t, body, "Method Man")
	require.NotContains(t, body, "RZA")

	body = e.get("/artists?sort=slug&dir=desc").Body.String()
	require.Less(t, strings.Index(body, "wu-tang-clan"), strings.Index(body, "killarmy"))
	require.Contains(t, body, `aria-sort="descending"`)

	body = e.get("/artists?page_size=2&page=2&category=core").Body.String()
	require.Contains(t, body, "page 1 of 1", "out-of-range pages fall back to the first page")
}

func TestListLinksPreserveFilters(t *testing.T) {
	e := newEditorEnv(t)
	body := e.get("/artists?category=core&page_size=1").Body.String()
	require.Contains(t, body, "category=core")
	require.Regexp(t, `rel="next" href="[^"]*category=core[^"]*page=2`, body)
}

func TestCreateArtistPreviewThenSave(t *testing.T) {
	e := newEditorEnv(t)
	before := e.file()
	form := artistForm("ghostface-killah", "", "name", "Ghostface Killah", "category", "core", "aliases", "Tony Starks\n\nIronman", "notes", "Staten Island")

	form.Set("intent", "preview")
	rec := e.post("/artists", form)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Ghostface Killah")
	require.Contains(t, rec.Body.String(), "Tony Starks | Ironman")
	require.Equal(t, before, e.file(), "a preview never writes")

	form.Set("intent", "save")
	rec = e.post("/artists", form)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/artists/ghostface-killah", rec.Header().Get("Location"))

	page := e.get(rec.Header().Get("Location"))
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "Created Ghostface Killah", "toast confirms what the server did")
	require.NotContains(t, e.get("/artists/ghostface-killah").Body.String(), "Created Ghostface Killah", "a toast shows once")
	require.Contains(t, e.file(), "slug: ghostface-killah")
	require.Contains(t, e.file(), "- Ironman")
	require.Contains(t, e.get("/artists").Body.String(), "Ghostface Killah")
}

func TestCreateValidationErrorsAreShownPerFieldAndNothingIsWritten(t *testing.T) {
	e := newEditorEnv(t)
	before := e.file()
	form := artistForm("Bad Slug", "", "name", "Method Man", "external_url", "http://example.com", "intent", "save")

	rec := e.post("/artists", form)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "must use lowercase letters")
	require.Contains(t, body, "must be an https URL")
	require.Contains(t, body, "duplicate name")
	require.Contains(t, body, `value="Bad Slug"`, "the submitted values are kept")
	require.Contains(t, body, `role="alert"`)
	require.Equal(t, before, e.file())
}

func TestUpdatePreservesUnrelatedFieldsAndOrder(t *testing.T) {
	e := newEditorEnv(t)
	rev := e.revision("wu-tang-clan")
	form := artistForm("wu-tang-clan", rev, "name", "Wu-Tang Clan", "roles", "affiliate_group", "aliases", "Wu Tang\nWTC",
		"enabled", "on", "editorial_order", "1", "notes", "Staten Island", "intent", "save")
	form.Add("roles", "core")

	rec := e.post("/artists/wu-tang-clan", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	file := e.file()
	require.Contains(t, file, "- WTC")
	require.Contains(t, file, "hardcore hip hop", "genres untouched")
	require.Contains(t, file, "status: verified", "reviewed Spotify profile untouched")
	require.Contains(t, file, "# Editorial artist catalog.", "header comments kept")
	require.Less(t, strings.Index(file, "slug: wu-tang-clan"), strings.Index(file, "slug: method-man"), "record order kept")
	require.Less(t, strings.Index(file, "- slug: "), strings.Index(file, "name: Wu-Tang Clan"), "field order is deterministic")
}

func TestUpdatePreviewShowsDiffOnly(t *testing.T) {
	e := newEditorEnv(t)
	before := e.file()
	form := artistForm("method-man", e.revision("method-man"), "name", "Method Man", "notes", "new note", "enabled", "on", "editorial_order", "2", "aliases", "Johnny Blaze", "intent", "preview")

	rec := e.post("/artists/method-man", form)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "new note")
	require.Contains(t, body, "notes")
	require.NotContains(t, body, "<td>aliases</td>", "unchanged fields are not listed")
	require.Equal(t, before, e.file())
}

func TestStaleRevisionReturnsConflictWithChangedFields(t *testing.T) {
	e := newEditorEnv(t)
	stale := e.revision("method-man")

	first := artistForm("method-man", stale, "name", "Method Man", "enabled", "on", "editorial_order", "2", "aliases", "Johnny Blaze", "notes", "tab one", "intent", "save")
	require.Equal(t, http.StatusSeeOther, e.post("/artists/method-man", first).Code)
	afterFirst := e.file()

	second := artistForm("method-man", stale, "name", "Method Man", "enabled", "on", "editorial_order", "2", "aliases", "Johnny Blaze", "notes", "tab two", "intent", "save")
	rec := e.post("/artists/method-man", second)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "tab one")
	require.Contains(t, rec.Body.String(), "notes")
	require.Contains(t, rec.Body.String(), "tab two", "the rejected submission is offered back")
	require.Equal(t, afterFirst, e.file())
}

func TestExternalFileEditAlsoCausesConflict(t *testing.T) {
	e := newEditorEnv(t)
	rev := e.revision("rza")
	raw := e.file()
	require.NoError(t, os.WriteFile(e.path, []byte(strings.Replace(raw, "name: RZA", "name: The RZA", 1)), 0o644))

	rec := e.post("/artists/rza", artistForm("rza", rev, "name", "RZA", "category", "producer", "notes", "x", "intent", "save"))

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, e.file(), "name: The RZA")
}

func TestConcurrentSavesNeverCorruptTheFile(t *testing.T) {
	e := newEditorEnv(t)
	rev := e.revision("rza")

	const writers = 6
	codes := make(chan int, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			form := artistForm("rza", rev, "name", "RZA", "category", "producer", "notes", fmt.Sprintf("writer %d", i), "intent", "save")
			codes <- e.post("/artists/rza", form).Code
		}(i)
	}
	wg.Wait()
	close(codes)

	won, conflicted := 0, 0
	for code := range codes {
		switch code {
		case http.StatusSeeOther:
			won++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, writers-1, conflicted)
	_, err := yamlcatalog.NewStore().Load(context.Background(), e.path)
	require.NoError(t, err, "the catalog is still valid YAML")
}

func TestSlugChangeRequiresExplicitConfirmation(t *testing.T) {
	e := newEditorEnv(t)
	rev := e.revision("method-man")
	form := artistForm("meth", rev, "name", "Method Man", "enabled", "on", "editorial_order", "2", "aliases", "Johnny Blaze", "intent", "save")
	form.Set("original_slug", "method-man")

	rec := e.post("/artists/method-man", form)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "member_of")
	require.Contains(t, rec.Body.String(), "confirm_slug_change")
	require.Contains(t, e.file(), "slug: method-man")

	form.Set("confirm_slug_change", "on")
	rec = e.post("/artists/method-man", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/artists/meth", rec.Header().Get("Location"))
	require.Contains(t, e.file(), "from: meth")
}

func TestArchiveHidesFromListAndRestoreBringsBack(t *testing.T) {
	e := newEditorEnv(t)
	rec := e.post("/artists/rza/archive", url.Values{"revision": {e.revision("rza")}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, e.file(), "archived: true")
	require.NotContains(t, e.get("/artists").Body.String(), "<code>rza</code>")
	require.Contains(t, e.get("/artists?archived=1").Body.String(), "<code>rza</code>")

	rec = e.post("/artists/rza/restore", url.Values{"revision": {e.revision("rza")}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, e.get("/artists").Body.String(), "<code>rza</code>")
}

func TestDeleteNeedsTypedSlugAndIsBlockedByReferences(t *testing.T) {
	e := newEditorEnv(t)

	rec := e.post("/artists/method-man/delete", url.Values{"revision": {e.revision("method-man")}, "confirm_slug": {"method-man"}})
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "member_of")
	require.Contains(t, rec.Body.String(), "archive")
	require.Contains(t, e.file(), "slug: method-man")

	rev := e.revision("rza")
	rec = e.post("/artists/rza/delete", url.Values{"revision": {rev}, "confirm_slug": {"wrong"}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, e.file(), "slug: rza")

	rec = e.post("/artists/rza/delete", url.Values{"revision": {rev}, "confirm_slug": {"rza"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/artists", rec.Header().Get("Location"))
	require.NotContains(t, e.file(), "slug: rza")
}

func TestEditorShowsSpotifyProfilesSeparatelyAndReadOnly(t *testing.T) {
	e := newEditorEnv(t)
	body := e.get("/groups/wu-tang-clan").Body.String()
	require.Contains(t, body, "Spotify profiles")
	require.Contains(t, body, spotifyID(1))
	require.Contains(t, body, "profile review")
	require.NotContains(t, body, `name="spotify_id"`, "there is no raw Spotify ID field")

	form := artistForm("wu-tang-clan", e.revision("wu-tang-clan"), "name", "Wu-Tang Clan", "enabled", "on", "editorial_order", "1", "spotify_id", spotifyID(9), "spotify_ids", spotifyID(8), "intent", "save")
	form.Add("roles", "core")
	form.Add("roles", "affiliate_group")
	form.Set("aliases", "Wu Tang")
	require.Equal(t, http.StatusSeeOther, e.post("/artists/wu-tang-clan", form).Code)
	require.NotContains(t, e.file(), spotifyID(9), "posted Spotify IDs are ignored")
	require.NotContains(t, e.file(), spotifyID(8))
}

func TestGroupPageListsMembersAndAcceptsRelationshipEdits(t *testing.T) {
	e := newEditorEnv(t)

	require.Equal(t, http.StatusSeeOther, e.get("/artists/wu-tang-clan").Code, "groups have their own page")
	require.Equal(t, "/groups/wu-tang-clan", e.get("/artists/wu-tang-clan").Header().Get("Location"))
	require.Equal(t, "/artists/rza", e.get("/groups/rza").Header().Get("Location"))

	page := e.get("/groups/wu-tang-clan").Body.String()
	require.Contains(t, page, "Method Man")
	require.Contains(t, page, "verified")

	// Verified membership without independent evidence is refused.
	bad := url.Values{"rel_type": {"member_of"}, "direction": {"inbound"}, "other": {"rza"}, "status": {"verified"},
		"reviewer": {"javi"}, "confidence": {"high"}, "evidence_type": {"name_match"}, "evidence_rationale": {"same name"}, "intent": {"save"}}
	rec := e.post("/groups/wu-tang-clan/relationships", bad)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "independent evidence")
	require.NotContains(t, e.file(), "from: rza")

	// Proposed membership is allowed and shown as unverified.
	ok := url.Values{"rel_type": {"member_of"}, "direction": {"inbound"}, "other": {"rza"}, "status": {"proposed"}, "start": {"1992"}, "intent": {"save"}}
	rec = e.post("/groups/wu-tang-clan/relationships", ok)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Contains(t, e.file(), "from: rza")
	require.Contains(t, e.get("/groups/wu-tang-clan").Body.String(), "proposed")
}

func TestRelationshipRemoveNeedsCurrentRevision(t *testing.T) {
	e := newEditorEnv(t)
	body := e.get("/groups/wu-tang-clan").Body.String()
	m := regexpMust(`name="rel_revision" value="([^"]+)"`).FindStringSubmatch(body)
	require.Len(t, m, 2)
	form := url.Values{"rel_type": {"member_of"}, "rel_from": {"method-man"}, "rel_to": {"wu-tang-clan"}, "rel_revision": {"stale"}}

	rec := e.post("/groups/wu-tang-clan/relationships/remove", form)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, e.file(), "from: method-man")

	form.Set("rel_revision", m[1])
	rec = e.post("/groups/wu-tang-clan/relationships/remove", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NotContains(t, e.file(), "from: method-man")
}

func TestMutationsNeedCSRFToken(t *testing.T) {
	e := newEditorEnv(t)
	before := e.file()
	form := artistForm("ghostface-killah", "", "name", "Ghostface Killah", "intent", "save")
	form.Set("csrf_token", "forged")
	require.Equal(t, http.StatusForbidden, e.post("/artists", form).Code)
	require.Equal(t, before, e.file())
}

func TestFilesystemFailureReportsErrorAndKeepsFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	e := newEditorEnv(t)
	before := e.file()
	dir := filepath.Dir(e.path)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	rec := e.post("/artists", artistForm("ghostface-killah", "", "name", "Ghostface Killah", "intent", "save"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "nothing was changed")
	require.NotContains(t, rec.Body.String(), dir, "server paths are not leaked")
	require.NoError(t, os.Chmod(dir, 0o755))
	require.Equal(t, before, e.file())
}

func TestFetchSubmissionsGetServerConfirmationAsJSON(t *testing.T) {
	e := newEditorEnv(t)
	form := artistForm("ghostface-killah", "", "name", "Ghostface Killah", "intent", "save")
	form.Set("csrf_token", e.csrf)
	req := httptest.NewRequest(http.MethodPost, "/artists", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+host)
	req.Header.Set("X-Admin-Fetch", "1")
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Contains(t, rec.Body.String(), `"redirect":"/artists/ghostface-killah"`)
	require.Contains(t, rec.Body.String(), "Created Ghostface Killah")
	require.Contains(t, e.file(), "slug: ghostface-killah")
}

func TestEditorRoutesRequireAuthentication(t *testing.T) {
	e := newEditorEnv(t)
	for _, target := range []string{"/artists/new", "/artists/rza", "/groups/wu-tang-clan"} {
		rec := do(t, e.h, http.MethodGet, target, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target)
	}
	rec := do(t, e.h, http.MethodGet, "/artists/nobody", withCookie(e.cookie))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func regexpMust(expr string) *regexp.Regexp { return regexp.MustCompile(expr) }
