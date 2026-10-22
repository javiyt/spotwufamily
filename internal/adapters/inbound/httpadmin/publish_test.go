package httpadmin_test

import (
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

const (
	pubLocal  = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	pubPinned = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
)

func (f *fakeOp) ReleaseStatus() (maintenance.ReleaseStatus, bool) {
	f.statusCalls.Add(1)
	f.opMu.Lock()
	defer f.opMu.Unlock()
	return f.release, !f.noRelease
}

func (f *fakeOp) RecheckRelease() { f.rechecks.Add(1) }

var _ httpadmin.Publisher = (*fakeOp)(nil)

func (f *fakeOp) ranNames() []string {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	return append([]string(nil), f.runNames...)
}

// readyRelease is a checked status where both steps can run.
func readyRelease() maintenance.ReleaseStatus {
	return maintenance.ReleaseStatus{
		Checked: time.Date(2026, 10, 20, 11, 59, 30, 0, time.UTC),
		Prereq: maintenance.Prereq{
			GhAuthenticated: true, GoAvailable: true, PackageDirClean: false,
			LocalContentSHA256: pubLocal, PinnedContentSHA256: pubPinned, LastReleaseTag: "catalog-2026-09-01",
			PreparedTag: "catalog-2026-10-20", PreparedContentSHA256: pubLocal,
		},
		LastPrepared: pubLocal, PreparedMatchesLocal: true,
		PrepareBlockers: []string{"The release package directory build/catalog-release is not empty; publish or remove it first"},
		Repo:            "javiyt/spotwufamily", PackageName: "build/catalog-release", ManifestName: "data/catalog.release.json",
	}
}

func withRelease(st maintenance.ReleaseStatus) func(*fakeOp) {
	return func(f *fakeOp) {
		f.release = st
		f.actions = append(f.actions, maintenance.ActionInfo{Name: "release-prepare", Title: "Prepare catalog release package", Available: true})
		f.previews = map[string]maintenance.Preview{
			"release-publish": {Title: "Publish catalog release to GitHub", RequiresTyped: "publish release", Destructive: true, UsesNetwork: true},
			"release-prepare": {Title: "Prepare catalog release package"},
		}
	}
}

func TestPublishPageListsUnmetPrerequisites(t *testing.T) {
	st := readyRelease()
	st.Prereq = maintenance.Prereq{GhAuthenticated: false, GoAvailable: false, PackageDirClean: false,
		LocalContentSHA256: pubLocal, PinnedContentSHA256: pubPinned, LastReleaseTag: "catalog-2026-09-01"}
	st.LastPrepared, st.PreparedMatchesLocal = "", false
	st.PrepareBlockers = []string{"Go is not installed or not on PATH"}
	st.PublishBlockers = []string{"GitHub CLI is not authenticated. Run `gh auth login` in a terminal.",
		"Nothing is prepared for the current catalog content; run release-prepare first."}
	e := newOpsEnv(t, withRelease(st))
	rec := e.get(t, "/publish")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{
		"Publication checklist", "Prerequisites",
		"GitHub CLI authenticated", "Run <code>gh auth login</code> in a terminal",
		"Go available", "Install Go",
		"Package directory empty", "Empty or remove <code>build/catalog-release</code>",
		"python3 and the release script",
	} {
		require.Contains(t, body, want)
	}
	require.Equal(t, 3, strings.Count(body, `class="badge bad">FAIL`), "gh, Go and the package directory fail")
	require.Contains(t, body, `class="badge ok">PASS</span> python3`, "python3 ran")
	require.Contains(t, body, "Go is not installed or not on PATH", "step 1 shows why it cannot run")
	require.NotContains(t, body, `action="/operations/release-prepare/preview"`)
	require.NotContains(t, body, `action="/operations/release-publish/preview"`)

	// A failed check: python3 or the script is missing, nothing else is known.
	failed := maintenance.ReleaseStatus{Checked: st.Checked, CheckFailed: true, PackageName: "build/catalog-release",
		PrepareBlockers: []string{"python3 or the release script is unavailable"},
		PublishBlockers: []string{"python3 or the release script is unavailable"}}
	e = newOpsEnv(t, withRelease(failed))
	body = e.get(t, "/publish").Body.String()
	require.Contains(t, body, `class="badge bad">FAIL</span> python3 and the release script`)
	require.Contains(t, body, "Install python3")
	require.Contains(t, body, "unknown")
	require.NotContains(t, body, `action="/operations/release-publish/preview"`)
}

func TestPublishButtonDisabledWhenStale(t *testing.T) {
	// Ready: step 2 is a form that leads to the generic preview.
	e := newOpsEnv(t, withRelease(readyRelease()))
	body := e.get(t, "/publish").Body.String()
	require.Contains(t, body, `action="/operations/release-publish/preview"`)
	require.Contains(t, body, "Review publish…")
	require.Contains(t, body, `name="from" value="publish"`)
	require.Contains(t, body, "catalog-2026-10-20", "prepared tag")
	require.Contains(t, body, "aaaaaaaaaaaa", "short local sha")
	require.NotContains(t, body, pubLocal, "shas are shortened to 12 characters")

	// The prepared package holds other content: step 2 is not a form.
	st := readyRelease()
	st.Prereq.PreparedContentSHA256 = pubPinned
	st.PreparedMatchesLocal = false
	st.PublishBlockers = []string{"Nothing is prepared for the current catalog content; run release-prepare first."}
	e = newOpsEnv(t, withRelease(st))
	body = e.get(t, "/publish").Body.String()
	require.NotContains(t, body, `action="/operations/release-publish/preview"`)
	require.NotContains(t, body, "Review publish…")
	require.Contains(t, body, "Nothing is prepared for the current catalog content")
	require.Contains(t, body, "does not match the local content")

	// Still checking: nothing can be published yet and the page reloads.
	e = newOpsEnv(t, withRelease(maintenance.ReleaseStatus{Checking: true,
		PrepareBlockers: []string{"Checking release prerequisites; reload the page in a moment"},
		PublishBlockers: []string{"Checking release prerequisites; reload the page in a moment"}}))
	body = e.get(t, "/publish").Body.String()
	require.Contains(t, body, "Checking…")
	require.Contains(t, body, `<meta http-equiv="refresh" content="3">`)
	require.NotContains(t, body, `action="/operations/release-publish/preview"`)
	require.NotContains(t, body, `action="/operations/release-prepare/preview"`)
}

func TestPublishPageNeverComputesFreshness(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()))
	body := e.get(t, "/publish").Body.String()
	require.Contains(t, body, "not checked yet")
	require.Contains(t, body, `action="/operations/freshness"`)
	require.Zero(t, e.svc.freshCalls.Load(), "GET /publish never computes freshness")

	e.svc.fresh = maintenance.Freshness{ExportNeeded: true, DiffersFromPublished: true,
		Reasons: []string{"Generated export is out of date with the database", "Local data differs from the published catalog"}}
	rec := e.send("/operations/freshness", e.form("from", "publish"))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/publish#readiness", rec.Header().Get("Location"))
	// Any other "from" goes back to the operations page.
	rec = e.send("/operations/freshness", e.form("from", "https://evil.example"))
	require.Equal(t, "/operations#freshness", rec.Header().Get("Location"))

	body = e.get(t, "/publish").Body.String()
	require.Contains(t, body, "Generated export is out of date with the database")
	require.Contains(t, body, "Export needed")
	require.EqualValues(t, 1, e.svc.freshCalls.Load())
}

func TestPublishPageReturnsWhileReleasePreflightHangs(t *testing.T) {
	e, rel := newRealOpsEnv(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.get(t, "/publish") }()
	select {
	case rec := <-done:
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		require.Contains(t, body, "Checking…")
		require.Contains(t, body, `<meta http-equiv="refresh" content="3">`)
		require.NotContains(t, body, `action="/operations/release-publish/preview"`)
	case <-time.After(3 * time.Second):
		t.Fatal("GET /publish blocked on the release preflight")
	}
	// Re-check while the check still hangs answers promptly too.
	go func() { done <- e.send("/publish/recheck", e.form()) }()
	select {
	case rec := <-done:
		require.Equal(t, http.StatusSeeOther, rec.Code)
	case <-time.After(3 * time.Second):
		t.Fatal("POST /publish/recheck blocked on the release preflight")
	}
	require.LessOrEqual(t, rel.calls.Load(), int32(1), "single-flight background check")
}

func TestGenericReleaseRoutesRedirectToPublish(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()))
	for _, name := range []string{"release-publish", "release-prepare"} {
		rec := e.get(t, "/operations/"+name)
		require.Equal(t, http.StatusSeeOther, rec.Code, name)
		require.Equal(t, "/publish", rec.Header().Get("Location"))

		for _, step := range []string{"preview", "run"} {
			rec = e.send("/operations/"+name+"/"+step, e.form("token", "tok-secret-"+name, "typed", "publish release"))
			require.Equal(t, http.StatusSeeOther, rec.Code, name+" "+step)
			require.Equal(t, "/publish", rec.Header().Get("Location"))
			rec = e.send("/operations/"+name+"/"+step, e.form("from", "operations", "token", "tok-secret-"+name, "typed", "publish release"))
			require.Equal(t, "/publish", rec.Header().Get("Location"), "only from=publish passes")
		}
	}
	require.Zero(t, e.runs(), "the generic flow never runs a guided action")
	// Even the guided redirect checks the request first.
	require.Equal(t, http.StatusForbidden, e.post("/operations/release-publish/run", e.form("from", "publish"), "https://evil.example").Code)
	require.Equal(t, http.StatusForbidden, e.post("/operations/release-publish/run", url.Values{"from": {"publish"}}, "http://"+host).Code)
	require.Equal(t, http.StatusUnauthorized, do(t, e.h, http.MethodGet, "/operations/release-publish", nil).Code)
}

func TestGuidedPublishFlowKeepsItsOrigin(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()))
	rec := e.send("/operations/release-publish/preview", e.form("from", "publish"))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `action="/operations/release-publish/run"`)
	require.Contains(t, body, `<code>publish release</code>`)
	require.Contains(t, body, `name="from" value="publish"`, "the run form carries the origin")
	require.Contains(t, body, `href="/publish" data-cancel`)
	require.NotContains(t, body, `href="/operations/release-publish"`)

	// Wrong phrase: re-rendered with the origin, nothing runs.
	rec = e.send("/operations/release-publish/run", e.form("from", "publish", "token", "tok-secret-release-publish", "typed", "publish"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), `name="from" value="publish"`)
	require.Empty(t, e.svc.held)

	rec = e.send("/operations/release-publish/run", e.form("from", "publish", "token", "tok-secret-release-publish", "typed", "publish release"))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID2, rec.Header().Get("Location"))
	require.Equal(t, maintenance.Params{}, e.svc.lastParams, "from is never an action parameter")
	require.Equal(t, []string{"release-publish", "release-publish"}, e.svc.ranNames(), "the wrong phrase was refused by the service, the right one ran")

	// Stale and unavailable pages keep the origin too.
	rec = e.send("/operations/release-prepare/run", e.form("from", "publish", "token", "forged"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), `name="from" value="publish"`)
}

func TestPublishPageEscapesReleaseData(t *testing.T) {
	st := readyRelease()
	st.Prereq.LastReleaseTag = `<script>alert(1)</script>`
	st.Prereq.PreparedTag = `"><img src=x onerror=alert(1)>`
	st.Prereq.LocalContentSHA256 = `<b>bold</b>"onmouseover=x`
	st.PackageName = `<i>pkg</i>`
	e := newOpsEnv(t, withRelease(st), func(f *fakeOp) {
		f.recent = []maintenance.OperationEntry{{At: time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC), Action: "release-publish",
			Kind: maintenance.EntryFinished, Outcome: "failed", Summary: `<script>x()</script>`, JobID: "../../etc"}}
	})
	body := e.get(t, "/publish").Body.String()
	for _, raw := range []string{"<script>alert(1)</script>", `"><img src=x`, "<b>bold</b>", "<i>pkg</i>", "<script>x()</script>", `href="/jobs/../../etc"`} {
		require.NotContains(t, body, raw)
	}
	require.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestPublishPageListsReleaseOperationsOnly(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()), func(f *fakeOp) {
		f.recent = []maintenance.OperationEntry{
			{Action: "sync", Kind: maintenance.EntryConfirmed, Outcome: "submitted", Summary: "Sync everything", JobID: jobID1},
			{Action: "release-prepare", Kind: maintenance.EntryFinished, Outcome: "succeeded", Summary: "Prepared catalog release", JobID: jobID2},
		}
	})
	body := e.get(t, "/publish").Body.String()
	require.Contains(t, body, "Prepared catalog release")
	require.Contains(t, body, `href="/jobs/`+jobID2+`"`)
	require.NotContains(t, body, "Sync everything")
	require.Contains(t, body, `href="/operations#recent"`)
}

func TestPublishRecheckAndPostsAreProtected(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()))
	rec := e.send("/publish/recheck", e.form())
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/publish#prerequisites", rec.Header().Get("Location"))
	require.EqualValues(t, 1, e.svc.rechecks.Load())

	require.Equal(t, http.StatusForbidden, e.post("/publish/recheck", e.form(), "https://evil.example").Code)
	require.Equal(t, http.StatusForbidden, e.post("/publish/recheck", url.Values{}, "http://"+host).Code)
	require.Equal(t, http.StatusMethodNotAllowed, e.get(t, "/publish/recheck").Code)
	require.Equal(t, http.StatusMethodNotAllowed, e.send("/publish", e.form()).Code)
	require.EqualValues(t, 1, e.svc.rechecks.Load())
	for _, target := range []string{"/publish", "/publish/recheck"} {
		require.Equal(t, http.StatusUnauthorized, do(t, e.h, http.MethodGet, target, nil).Code, target)
	}
	require.Zero(t, e.runs())
}

func TestPublishPageManualChecklistAndNoJS(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()))
	body := e.get(t, "/publish").Body.String()
	for _, want := range []string{"data/catalog.release.json", "pull request", "Pages", "Verify the site"} {
		require.Contains(t, body, want)
	}
	require.NotContains(t, body, "<script>", "no inline script; every step is a plain form")
	require.Contains(t, body, `action="/publish/recheck"`)
	// The package directory holds the prepared package, so step 1 explains
	// instead of offering a form.
	require.NotContains(t, body, `action="/operations/release-prepare/preview"`)
	require.Contains(t, body, "is not empty; publish or remove it first")

	st := readyRelease()
	st.Prereq.PackageDirClean, st.Prereq.PreparedTag, st.Prereq.PreparedContentSHA256 = true, "", ""
	st.PrepareBlockers = nil
	e = newOpsEnv(t, withRelease(st))
	body = e.get(t, "/publish").Body.String()
	require.Contains(t, body, `action="/operations/release-prepare/preview"`)
	require.Contains(t, body, "Prepare package…")
}

func TestPublishPageWithoutReleaseActions(t *testing.T) {
	e := newOpsEnv(t, func(f *fakeOp) { f.noRelease = true })
	rec := e.get(t, "/publish")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "not configured")
	require.NotContains(t, body, `action="/operations/release-publish/preview"`)
}

// editOps serves the real editor and profile workbench next to a fake
// operator that records every RunAction.
type editOps struct {
	admin.Service
	*fakeOp
}

func newEditOpsEnv(t *testing.T) (*resolveEnv, *fakeOp) {
	t.Helper()
	op := &fakeOp{actions: defaultActions(), eligible: map[string]string{}}
	withRelease(readyRelease())(op)
	op.previews["sync"] = syncAllPreview()
	op.fresh = maintenance.Freshness{Reasons: []string{"No snapshot found"}}
	env := newResolveEnv(t, func(e *resolveEnv, _ *admin.ResolutionDeps) {
		e.wrap = func(s admin.Service) httpadmin.Service { return editOps{Service: s, fakeOp: op} }
	})
	return env, op
}

func TestSavingArtistNeverTriggersPublish(t *testing.T) {
	env, op := newEditOpsEnv(t)
	ok := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		env.get(rec.Header().Get("Location"))
	}
	// Every catalog write and decision the admin offers.
	ok(env.post("/artists", artistForm("ghostface-killah", "", "name", "Ghostface Killah", "category", "core", "intent", "save")))
	ok(env.post("/artists/newcomer", artistForm("newcomer", env.revision("newcomer"), "name", "Newcomer", "category", "affiliate_artist", "intent", "save")))
	ok(env.post("/artists/newcomer/archive", url.Values{"revision": {env.revision("newcomer")}}))
	ok(env.post("/artists/newcomer/restore", url.Values{"revision": {env.revision("newcomer")}}))
	ok(env.post("/groups/k-g-b/relationships", url.Values{"rel_type": {"member_of"}, "direction": {"inbound"}, "other": {"ghostface-killah"},
		"status": {"proposed"}, "intent": {"save"}}))
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	ok(env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")))
	ok(env.post("/artists/streetlife/delete", url.Values{"revision": {env.revision("streetlife")}, "confirm_slug": {"streetlife"}}))
	// Every operations page and success path short of the guided publish run.
	env.get("/operations")
	env.get("/publish")
	ok(env.post("/operations/freshness", url.Values{}))
	ok(env.post("/publish/recheck", url.Values{}))
	ok(env.post("/operations/sync/run", url.Values{"scope": {"all"}, "token": {"tok-secret-sync"}, "typed": {"sync 42 artists"}}))
	op.opMu.Lock()
	op.held = ""
	op.opMu.Unlock()
	ok(env.post("/operations/release-prepare/run", url.Values{"from": {"publish"}, "token": {"tok-secret-release-prepare"}}))
	op.opMu.Lock()
	op.held = ""
	op.opMu.Unlock()
	require.Equal(t, []string{"sync", "release-prepare"}, op.ranNames(), "nothing but an explicit run reaches RunAction")
	require.NotContains(t, op.ranNames(), "release-publish")
	// The page edits link to never posts to /publish or a release action.
	for _, page := range []string{"/artists/ghostface-killah", "/groups/k-g-b", "/resolve/newcomer", "/artists"} {
		body := env.get(page).Body.String()
		require.NotContains(t, body, `action="/publish`, page)
		require.NotContains(t, body, `action="/operations/release-`, page)
	}

	// Only the guided run publishes.
	ok(env.post("/operations/release-publish/run", url.Values{"from": {"publish"}, "token": {"tok-secret-release-publish"}, "typed": {"publish release"}}))
	require.Equal(t, []string{"sync", "release-prepare", "release-publish"}, op.ranNames())
}

func TestPublishChecklistCommitsCatalogWithThePin(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()))
	body := e.get(t, "/publish").Body.String()
	require.Contains(t, body, "<code>data/catalog.release.json</code> together with the catalog changes")
	require.Contains(t, body, "<code>data/artists.yaml</code>")
	require.Contains(t, body, "<code>data/artists.yaml.reviews.jsonl</code>")
	require.Contains(t, body, "one pull request")
	require.Contains(t, body, "press Re-check", "step 1 says how to pick up a fresh package")
}

func TestPublishWarnsWhenDataMayNeedRefreshing(t *testing.T) {
	const warning = "Data may need refreshing — check freshness before publishing."
	e := newOpsEnv(t, withRelease(readyRelease()))
	body := e.get(t, "/publish").Body.String()
	require.Contains(t, body, warning, "unchecked freshness warns")
	require.Contains(t, body, `action="/operations/release-publish/preview"`, "the warning never blocks")

	// Only the expected "differs from published" reason: no warning.
	e.svc.fresh = maintenance.Freshness{DiffersFromPublished: true, Reasons: []string{"Local data differs from the published catalog"}}
	require.Equal(t, http.StatusSeeOther, e.send("/operations/freshness", e.form("from", "publish")).Code)
	require.NotContains(t, e.get(t, "/publish").Body.String(), warning)

	for _, f := range []maintenance.Freshness{{SeedNeeded: true}, {SeedUnknown: true}, {SnapshotNeeded: true}, {ExportNeeded: true}, {RebuildNeeded: true, Offline: true}} {
		e := newOpsEnv(t, withRelease(readyRelease()), func(op *fakeOp) { op.fresh = f })
		require.Equal(t, http.StatusSeeOther, e.send("/operations/freshness", e.form("from", "publish")).Code)
		require.Contains(t, e.get(t, "/publish").Body.String(), warning, "%+v", f)
	}
}

func TestPublishCheckingListsTheReasonForBothSteps(t *testing.T) {
	e, _ := newRealOpsEnv(t)
	body := e.get(t, "/publish").Body.String()
	require.Equal(t, 2, strings.Count(body, "<li>Checking release prerequisites; reload the page in a moment</li>"), body)
}

func TestPublishShowsUnknownSeedState(t *testing.T) {
	e := newOpsEnv(t, withRelease(readyRelease()), func(op *fakeOp) {
		op.fresh = maintenance.Freshness{SeedUnknown: true, Pending: []artists.ArtistChange{{Slug: "a"}}}
	})
	require.Equal(t, http.StatusSeeOther, e.send("/operations/freshness", e.form("from", "publish")).Code)
	body := e.get(t, "/publish").Body.String()
	require.Contains(t, body, "<td>unknown</td>")
	require.Contains(t, body, "Refresh the data first")

	api := e.get(t, "/api/v1/freshness").Body.String()
	require.Contains(t, api, `"seed_unknown":true`)
	require.Contains(t, api, `"pending_changes":1`)
}
