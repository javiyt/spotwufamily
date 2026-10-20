package httpadmin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

const syncCaveat = "Spotify request counts are an estimate"

// fakeOp is a Service that implements both Maintenance and Operator.
type fakeOp struct {
	fakeMaint
	opMu       sync.Mutex
	actions    []maintenance.ActionInfo
	previews   map[string]maintenance.Preview
	previewErr error
	runErr     error
	runCalls   int
	lastParams maintenance.Params
	lastTyped  string
	// held is the job holding the resources after a successful run.
	held string

	fresh        maintenance.Freshness
	freshErr     error
	freshCalls   atomic.Int32
	freshEntered chan struct{}
	freshRelease chan struct{}

	recent    []maintenance.OperationEntry
	recentErr error
	eligible  map[string]string // job ID -> action
}

func (f *fakeOp) Actions() []maintenance.ActionInfo {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	return append([]maintenance.ActionInfo(nil), f.actions...)
}

func (f *fakeOp) info(name string) (maintenance.ActionInfo, bool) {
	for _, a := range f.Actions() {
		if a.Name == name {
			return a, true
		}
	}
	return maintenance.ActionInfo{}, false
}

func (f *fakeOp) PreviewAction(_ context.Context, name string, p maintenance.Params) (maintenance.Preview, maintenance.Token, error) {
	f.opMu.Lock()
	err := f.previewErr
	f.opMu.Unlock()
	if err != nil {
		return maintenance.Preview{}, "", err
	}
	a, ok := f.info(name)
	if !ok {
		return maintenance.Preview{}, "", &maintenance.SafeError{Code: "not_found", Message: "The requested action was not found."}
	}
	if !a.Available {
		return maintenance.Preview{}, "", maintenance.ErrUnavailable
	}
	if _, bad := p["bogus"]; bad {
		return maintenance.Preview{}, "", &maintenance.SafeError{Code: "validation", Message: `Unknown parameter: "bogus".`}
	}
	pv := f.previews[name]
	pv.Action = name
	if pv.Title == "" {
		pv.Title = a.Title
	}
	return pv, maintenance.Token("tok-secret-" + name), nil
}

func (f *fakeOp) RunAction(ctx context.Context, name string, p maintenance.Params, tok maintenance.Token, typed string) (jobs.Job, error) {
	pv, _, err := f.PreviewAction(ctx, name, p)
	f.opMu.Lock()
	defer f.opMu.Unlock()
	f.runCalls++
	f.lastParams, f.lastTyped = p, typed
	if f.runErr != nil {
		return jobs.Job{}, f.runErr
	}
	if err != nil {
		return jobs.Job{}, err
	}
	if tok != maintenance.Token("tok-secret-"+name) {
		return jobs.Job{}, maintenance.ErrStalePreview
	}
	if pv.RequiresTyped != "" && typed != pv.RequiresTyped {
		return jobs.Job{}, maintenance.ErrConfirmationRequired
	}
	if f.held != "" {
		return jobs.Job{}, &jobs.ConflictError{HolderID: f.held}
	}
	f.held = jobID2
	return jobs.Job{ID: jobID2, Kind: name, Status: jobs.StatusQueued}, nil
}

func (f *fakeOp) Freshness(context.Context) (maintenance.Freshness, error) {
	f.freshCalls.Add(1)
	if f.freshEntered != nil {
		f.freshEntered <- struct{}{}
	}
	if f.freshRelease != nil {
		<-f.freshRelease
	}
	return f.fresh, f.freshErr
}

func (f *fakeOp) RecentOperations(context.Context, int) ([]maintenance.OperationEntry, error) {
	return f.recent, f.recentErr
}

func (f *fakeOp) ResumeEligible(id, action string) bool {
	return f.eligible[id] == action && action != ""
}

var (
	_ httpadmin.Operator    = (*fakeOp)(nil)
	_ httpadmin.Maintenance = (*fakeOp)(nil)
)

func defaultActions() []maintenance.ActionInfo {
	return []maintenance.ActionInfo{
		{Name: "discover-preview", Title: "Discover Wu Family artists (preview)", Available: true, UsesNetwork: true},
		{Name: "audit-profiles", Title: "Audit Spotify profiles", Available: true, UsesNetwork: true},
		{Name: "sync-dry-run", Title: "Sync catalog from Spotify (dry run)", Available: true},
		{Name: "sync", Title: "Sync catalog from Spotify", Available: true, UsesNetwork: true},
		{Name: "refresh-export", Title: "Verify, snapshot, export and audit", Available: true},
		{Name: "reconcile-apply", Title: "Reconcile artist associations", Destructive: true,
			UnavailableReason: "No database found; run a sync or rebuild it from the snapshot first"},
		{Name: "release-publish", Title: "Publish catalog release to GitHub", Available: true, UsesNetwork: true, Destructive: true},
	}
}

func syncAllPreview() maintenance.Preview {
	return maintenance.Preview{
		Title: "Sync catalog from Spotify",
		Scope: []maintenance.ScopeLine{
			{Label: "Artists", Value: "42 (all enabled artists with a verified Spotify profile)"},
			{Label: "Spotify requests", Value: "about 126 (estimate)"},
		},
		Warnings:      []string{syncCaveat + ": artists synced recently are skipped."},
		UsesNetwork:   true,
		Destructive:   true,
		RequiresTyped: "sync 42 artists",
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type opsEnv struct {
	svc    *fakeOp
	h      http.Handler
	cookie *http.Cookie
	csrf   string
	log    *bytes.Buffer
	clock  *fakeClock
}

func newOpsEnv(t *testing.T, mutate ...func(*fakeOp)) *opsEnv {
	t.Helper()
	svc := &fakeOp{
		actions:  defaultActions(),
		previews: map[string]maintenance.Preview{"sync": syncAllPreview()},
		eligible: map[string]string{},
	}
	svc.dashboard = admin.Dashboard{DatabaseAvailable: true, SpotifyCredentialsPresent: true}
	for _, m := range mutate {
		m(svc)
	}
	clk := &fakeClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	var logs bytes.Buffer
	srv, err := httpadmin.New(svc, httpadmin.Options{Addr: host, Token: token, Log: &logs, Now: clk.now})
	require.NoError(t, err)
	h := srv.Handler()
	cookie := login(t, h)
	return &opsEnv{svc: svc, h: h, cookie: cookie, csrf: csrfFor(t, h, cookie), log: &logs, clock: clk}
}

func (e *opsEnv) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, e.h, http.MethodGet, target, withCookie(e.cookie))
}

func (e *opsEnv) post(target string, form url.Values, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *opsEnv) send(target string, form url.Values) *httptest.ResponseRecorder {
	return e.post(target, form, "http://"+host)
}

func (e *opsEnv) form(kv ...string) url.Values {
	v := url.Values{"csrf_token": {e.csrf}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func (e *opsEnv) runs() int {
	e.svc.opMu.Lock()
	defer e.svc.opMu.Unlock()
	return e.svc.runCalls
}

func TestRunWithoutPreviewTokenRefused(t *testing.T) {
	e := newOpsEnv(t)
	rec := e.send("/operations/sync-dry-run/run", e.form("scope", "all"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "The preview is out of date; review it again")
	require.Zero(t, e.runs(), "no token never reaches the service's run")
	require.Empty(t, e.svc.held)

	rec = e.send("/operations/sync-dry-run/run", e.form("scope", "all", "token", "forged"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "The preview is out of date; review it again")
	require.Empty(t, e.svc.held, "a forged token starts nothing")

	// GET never runs anything.
	require.Equal(t, http.StatusNotFound, e.get(t, "/operations/sync-dry-run/run").Code)
}

func TestDoubleSubmitSecondGets409(t *testing.T) {
	e := newOpsEnv(t)
	form := e.form("scope", "all", "token", "tok-secret-sync-dry-run")
	rec := e.send("/operations/sync-dry-run/run", form)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID2, rec.Header().Get("Location"))

	rec = e.send("/operations/sync-dry-run/run", form)
	require.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Another job is already using")
	require.Contains(t, body, `href="/jobs/`+jobID2+`"`)
	require.Contains(t, body, "b2c3d4e5")
	require.Equal(t, 2, e.runs())
}

func TestSyncAllShowsScopeAndQuotaCaveatBeforeRun(t *testing.T) {
	e := newOpsEnv(t)
	rec := e.send("/operations/sync/preview", e.form("scope", "all", "artists", "", "resume", "true"))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Sync catalog from Spotify")
	require.Contains(t, body, "42 (all enabled artists with a verified Spotify profile)")
	require.Contains(t, body, "about 126 (estimate)")
	require.Contains(t, body, syncCaveat)
	require.Contains(t, body, "API quota", "network caveat block")
	require.Contains(t, body, "NETWORK")
	require.Contains(t, body, "DESTRUCTIVE")
	require.Contains(t, body, `<code>sync 42 artists</code>`)
	require.Contains(t, body, `name="typed"`)
	require.Contains(t, body, `action="/operations/sync/run"`)
	require.Contains(t, body, `name="token" value="tok-secret-sync"`)
	require.Contains(t, body, `name="scope" value="all"`)
	require.Contains(t, body, `name="artists" value=""`)
	require.Contains(t, body, `name="resume" value="true"`)
	require.Contains(t, body, "data-once")
	require.Zero(t, e.runs(), "previewing never runs")

	// Running with the wrong phrase re-renders the preview with a message.
	rec = e.send("/operations/sync/run", e.form("scope", "all", "artists", "", "resume", "true", "token", "tok-secret-sync", "typed", "sync 41 artists"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body = rec.Body.String()
	require.Contains(t, body, "Type the exact phrase")
	require.Contains(t, body, `<code>sync 42 artists</code>`)
	require.Contains(t, body, `name="typed"`)
	require.Empty(t, e.svc.held)

	rec = e.send("/operations/sync/run", e.form("scope", "all", "artists", "", "resume", "true", "token", "tok-secret-sync", "typed", "sync 42 artists"))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, maintenance.Params{"scope": "all", "artists": "", "resume": "true"}, e.svc.lastParams, "params pass through untouched")
}

func TestTypedPhraseFieldOnlyWhenRequired(t *testing.T) {
	e := newOpsEnv(t)
	body := e.send("/operations/sync-dry-run/preview", e.form("scope", "all")).Body.String()
	require.NotContains(t, body, `name="typed"`)
	require.NotContains(t, body, "API quota", "no network caveat for a local dry run")
	require.Contains(t, body, `action="/operations/sync-dry-run/run"`)
}

func TestUnavailableActionShowsReasonAndNoRunButton(t *testing.T) {
	e := newOpsEnv(t)
	reason := "No database found; run a sync or rebuild it from the snapshot first"

	dash := e.get(t, "/operations").Body.String()
	require.Contains(t, dash, reason)
	require.NotContains(t, dash, `href="/operations/reconcile-apply"`)
	require.Contains(t, dash, `href="/operations/sync"`)

	rec := e.get(t, "/operations/reconcile-apply")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, reason)
	require.NotContains(t, body, "/operations/reconcile-apply/preview")
	require.NotContains(t, body, "/operations/reconcile-apply/run")

	rec = e.send("/operations/reconcile-apply/preview", e.form("artist", "rza"))
	require.Equal(t, http.StatusConflict, rec.Code)
	body = rec.Body.String()
	require.Contains(t, body, reason)
	require.NotContains(t, body, "/operations/reconcile-apply/run")
	require.NotContains(t, body, "temporarily unavailable", "the action's own reason, not the generic message")

	rec = e.send("/operations/reconcile-apply/run", e.form("artist", "rza", "token", "x"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), reason)
	require.Empty(t, e.svc.held)
}

func TestDashboardOfflineBannerWithoutDB(t *testing.T) {
	e := newOpsEnv(t, func(f *fakeOp) {
		f.dashboard = admin.Dashboard{DatabaseAvailable: false, SpotifyCredentialsPresent: false}
	})
	rec := e.get(t, "/operations")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "No database found (offline mode)")
	require.Contains(t, body, "unavailable (offline)")
	require.Contains(t, body, "Spotify credentials are not configured")

	e = newOpsEnv(t)
	body = e.get(t, "/operations").Body.String()
	require.NotContains(t, body, "offline mode")
	require.NotContains(t, body, "Spotify credentials are not configured")
	require.Contains(t, body, "available")
}

func TestOperationsDashboardCardsGroupsAndRecent(t *testing.T) {
	e := newOpsEnv(t, func(f *fakeOp) {
		f.recent = []maintenance.OperationEntry{
			{At: time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC), Kind: maintenance.EntryFinished, Action: "sync", JobID: jobID1, Outcome: "succeeded", Summary: "Synced 3 artists."},
			{At: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), Kind: maintenance.EntryConfirmed, Action: "sync", JobID: "../bad", Outcome: "submitted", Summary: "Sync"},
		}
	})
	body := e.get(t, "/operations").Body.String()
	for _, g := range []string{"Discover", "Audit", "Sync", "Refresh/Export", "Reconcile", "Release"} {
		require.Contains(t, body, "<h3>"+g+"</h3>", g)
	}
	iD, iS, iR := strings.Index(body, "<h3>Discover</h3>"), strings.Index(body, "<h3>Sync</h3>"), strings.Index(body, "<h3>Release</h3>")
	require.True(t, iD < iS && iS < iR, "groups in order")
	require.Contains(t, body, "Review…")
	require.Contains(t, body, `href="/publish"`, "release cards link to the publish page")
	require.NotContains(t, body, `href="/operations/release-publish"`)
	require.Contains(t, body, "Synced 3 artists.")
	require.Contains(t, body, `href="/jobs/`+jobID1+`"`)
	require.NotContains(t, body, `href="/jobs/../bad"`)
	require.Less(t, strings.Index(body, "Synced 3 artists."), strings.Index(body, "2026-10-20T10:00:00Z"), "newest first")
	require.NotContains(t, body, "make sync-all", "the static command list is replaced")
}

func TestOperationsPageDoesNotRenderSecretsOrPaths(t *testing.T) {
	leak := errors.New("open /Users/javi/private/ops.jsonl: denied\ngoroutine 7 [running]")
	e := newOpsEnv(t, func(f *fakeOp) {
		f.recentErr = leak
		f.freshErr = leak
	})
	for _, rec := range []*httptest.ResponseRecorder{
		e.get(t, "/operations"),
		e.get(t, "/api/v1/operations"),
		e.get(t, "/api/v1/freshness"),
		e.send("/operations/freshness", e.form()),
		e.get(t, "/operations"),
		e.get(t, "/operations/sync"),
	} {
		body := rec.Body.String()
		require.NotContains(t, body, "/Users")
		require.NotContains(t, body, "ops.jsonl")
		require.NotContains(t, body, "goroutine")
		require.NotContains(t, body, "tok-secret")
		require.NotContains(t, body, token)
	}
	require.Contains(t, e.get(t, "/operations").Body.String(), "could not be read")

	// Errors from preview and run are generic; tokens never reach the log.
	e.svc.previewErr = leak
	rec := e.send("/operations/sync-dry-run/preview", e.form("scope", "all"))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "/Users")
	e.svc.previewErr, e.svc.runErr = nil, leak
	rec = e.send("/operations/sync-dry-run/run", e.form("scope", "all", "token", "tok-secret-sync-dry-run"))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "/Users")
	require.NotContains(t, rec.Body.String(), "tok-secret")
	require.NotContains(t, e.log.String(), "tok-secret", "tokens are never logged")
	require.Contains(t, e.log.String(), "denied", "details are logged server-side")
}

func TestStaleTokenPageKeepsParams(t *testing.T) {
	e := newOpsEnv(t)
	rec := e.send("/operations/sync-dry-run/run", e.form("scope", "selected", "artists", "rza,gza", "token", "expired"))
	require.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "The preview is out of date; review it again")
	require.Contains(t, body, `action="/operations/sync-dry-run/preview"`)
	require.Contains(t, body, `name="scope" value="selected"`)
	require.Contains(t, body, `name="artists" value="rza,gza"`)
	require.NotContains(t, body, `name="token"`, "the stale token is not carried along")
}

func TestConflictPageNamesHolderWithLink(t *testing.T) {
	e := newOpsEnv(t, func(f *fakeOp) { f.held = jobID1 })
	rec := e.send("/operations/sync-dry-run/run", e.form("scope", "all", "token", "tok-secret-sync-dry-run"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), `href="/jobs/`+jobID1+`"`)

	// A holder ID outside the job-ID charset is never linked.
	e = newOpsEnv(t, func(f *fakeOp) { f.held = `"><script>x</script>` })
	rec = e.send("/operations/sync-dry-run/run", e.form("scope", "all", "token", "tok-secret-sync-dry-run"))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.NotContains(t, rec.Body.String(), "<script>x")
	require.NotContains(t, rec.Body.String(), `href="/jobs/&#34;`)
}

func TestResumeOfOnlyForEligibleJobs(t *testing.T) {
	e := newOpsEnv(t, func(f *fakeOp) { f.eligible[jobID1] = "sync" })
	body := e.get(t, "/operations/sync?resume_of="+jobID1).Body.String()
	require.Contains(t, body, "Resuming job")
	require.Contains(t, body, `href="/jobs/`+jobID1+`"`)
	require.Regexp(t, `name="resume" value="true"[^>]* checked`, body)

	for _, target := range []string{
		"/operations/sync?resume_of=" + jobID2,                        // not eligible
		"/operations/sync-dry-run?resume_of=" + jobID1,                // other action
		"/operations/sync?resume_of=" + url.QueryEscape(`"><script>`), // bad charset
	} {
		rec := e.get(t, target)
		require.Equal(t, http.StatusOK, rec.Code, target)
		body = rec.Body.String()
		require.NotContains(t, body, "Resuming job", target)
		require.Contains(t, body, "cannot be resumed", target)
		require.NotRegexp(t, `name="resume" value="true"[^>]* checked`, body, target)
		require.NotContains(t, body, "<script>", target)
	}
	body = e.get(t, "/operations/sync").Body.String()
	require.NotContains(t, body, "cannot be resumed")
	require.NotContains(t, body, "Resuming job")
}

func TestParameterFormFieldsAndUnknownAction(t *testing.T) {
	e := newOpsEnv(t)
	body := e.get(t, "/operations/sync").Body.String()
	require.Contains(t, body, `action="/operations/sync/preview"`)
	require.Contains(t, body, `<select id="f-scope" name="scope"`)
	require.Contains(t, body, `name="artists"`)
	require.Contains(t, body, `type="checkbox"`)
	require.Contains(t, body, "Preview")

	require.Contains(t, e.get(t, "/operations/discover-preview").Body.String(), "no parameters")

	for _, target := range []string{"/operations/nope", "/operations/UPPER", "/operations/%3Cscript%3E"} {
		require.Equal(t, http.StatusNotFound, e.get(t, target).Code, target)
	}
	require.Equal(t, http.StatusNotFound, e.send("/operations/nope/preview", e.form()).Code)
	require.Equal(t, http.StatusNotFound, e.send("/operations/nope/run", e.form("token", "x")).Code)
	require.Zero(t, e.runs())
}

func TestValidationErrorReRendersForm(t *testing.T) {
	e := newOpsEnv(t)
	rec := e.send("/operations/sync/preview", e.form("scope", "artist", "artists", "rza", "bogus", "1"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Unknown parameter: &#34;bogus&#34;.")
	require.Contains(t, body, `action="/operations/sync/preview"`)
	require.Contains(t, body, `value="rza"`, "submitted values are kept")
}

func TestOperationParamsAreEscaped(t *testing.T) {
	xss := `"><script>alert(1)</script>`
	e := newOpsEnv(t, func(f *fakeOp) {
		f.actions = append(f.actions, maintenance.ActionInfo{Name: "evil", Title: "<script>alert(2)</script>", UnavailableReason: "<script>alert(3)</script>"})
		f.recent = []maintenance.OperationEntry{{Action: "<script>alert(4)</script>", Summary: "<script>alert(5)</script>", JobID: jobID1}}
		f.previews["sync-dry-run"] = maintenance.Preview{Scope: []maintenance.ScopeLine{{Label: "<b>l</b>", Value: "<script>alert(6)</script>"}}, Warnings: []string{"<script>alert(7)</script>"}, RequiresTyped: xss}
	})
	for _, rec := range []*httptest.ResponseRecorder{
		e.send("/operations/sync-dry-run/preview", e.form("scope", xss, "artists", xss)),
		e.send("/operations/sync-dry-run/run", e.form("scope", xss, "token", "stale")),
		e.get(t, "/operations"),
		e.get(t, "/operations/evil"),
		e.get(t, "/operations/sync?resume_of="+url.QueryEscape(xss)),
	} {
		body := rec.Body.String()
		require.NotContains(t, body, "<script>alert", rec.Code)
		require.NotContains(t, body, "<b>l</b>")
		require.NotContains(t, body, `value=""><script>`)
	}
	body := e.send("/operations/sync-dry-run/preview", e.form("scope", xss)).Body.String()
	require.Contains(t, body, `name="scope" value="&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`)
	for _, target := range []string{"/api/v1/operations"} {
		require.NotContains(t, e.get(t, target).Body.String(), "<script>", target)
	}
}

func TestOperationPostsRequireSameOriginAndCSRF(t *testing.T) {
	e := newOpsEnv(t)
	for _, target := range []string{"/operations/sync-dry-run/preview", "/operations/sync-dry-run/run", "/operations/freshness"} {
		form := e.form("scope", "all", "token", "tok-secret-sync-dry-run")
		require.Equal(t, http.StatusForbidden, e.post(target, form, "https://evil.example").Code, target)
		noCSRF := url.Values{"scope": {"all"}, "token": {"tok-secret-sync-dry-run"}}
		require.Equal(t, http.StatusForbidden, e.post(target, noCSRF, "http://"+host).Code, target)
		bad := url.Values{"csrf_token": {"nope"}, "scope": {"all"}, "token": {"tok-secret-sync-dry-run"}}
		require.Equal(t, http.StatusForbidden, e.post(target, bad, "http://"+host).Code, target)
	}
	require.Zero(t, e.runs())
	require.Zero(t, e.svc.freshCalls.Load())

	// Unauthenticated requests never reach the handlers.
	for _, target := range []string{"/operations", "/operations/sync", "/api/v1/operations", "/api/v1/freshness"} {
		require.Equal(t, http.StatusUnauthorized, do(t, e.h, http.MethodGet, target, nil).Code, target)
	}
}

func TestFreshnessNeverComputedOnRead(t *testing.T) {
	e := newOpsEnv(t)
	body := e.get(t, "/operations").Body.String()
	require.Contains(t, body, "not checked yet")
	require.Contains(t, body, `action="/operations/freshness"`)
	require.Contains(t, body, "Check data freshness")
	rec := e.get(t, "/api/v1/freshness")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"checked":false}`, rec.Body.String())
	require.Equal(t, http.StatusMethodNotAllowed, e.send("/api/v1/freshness", e.form()).Code)
	require.Zero(t, e.svc.freshCalls.Load(), "reads never compute freshness")
}

func TestFreshnessCheckCachesForAMinute(t *testing.T) {
	e := newOpsEnv(t, func(f *fakeOp) {
		f.fresh = maintenance.Freshness{ExportNeeded: true, Reasons: []string{"Generated export is out of date with the database"}}
	})
	rec := e.send("/operations/freshness", e.form())
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/operations#freshness", rec.Header().Get("Location"))
	require.EqualValues(t, 1, e.svc.freshCalls.Load())

	body := e.get(t, "/operations").Body.String()
	require.Contains(t, body, "Generated export is out of date with the database")
	require.Contains(t, body, "checked 0s ago")
	require.NotContains(t, body, "not checked yet")

	e.clock.advance(30 * time.Second)
	require.Equal(t, http.StatusSeeOther, e.send("/operations/freshness", e.form()).Code)
	require.EqualValues(t, 1, e.svc.freshCalls.Load(), "cached for 60 s")
	require.Contains(t, e.get(t, "/operations").Body.String(), "checked 30s ago")

	var api map[string]any
	require.NoError(t, json.Unmarshal(e.get(t, "/api/v1/freshness").Body.Bytes(), &api))
	require.Equal(t, true, api["checked"])
	require.Equal(t, "2026-10-20T12:00:00Z", api["checked_at"])
	require.EqualValues(t, 30, api["age_seconds"])
	require.Equal(t, true, api["export_needed"])
	require.Equal(t, []any{"Generated export is out of date with the database"}, api["reasons"])

	e.clock.advance(31 * time.Second)
	require.Equal(t, http.StatusSeeOther, e.send("/operations/freshness", e.form()).Code)
	require.EqualValues(t, 2, e.svc.freshCalls.Load(), "recomputed after 60 s")
}

func TestConcurrentFreshnessChecksCoalesce(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	e := newOpsEnv(t, func(f *fakeOp) { f.freshEntered, f.freshRelease = entered, release })
	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(1)
	go func() { defer wg.Done(); codes[0] = e.send("/operations/freshness", e.form()).Code }()
	<-entered
	wg.Add(1)
	go func() { defer wg.Done(); codes[1] = e.send("/operations/freshness", e.form()).Code }()
	time.Sleep(50 * time.Millisecond) // let the second click wait on the first
	close(release)
	wg.Wait()
	require.Equal(t, []int{http.StatusSeeOther, http.StatusSeeOther}, codes)
	require.EqualValues(t, 1, e.svc.freshCalls.Load(), "concurrent clicks share one computation")
}

func TestOperationsAPI(t *testing.T) {
	e := newOpsEnv(t)
	rec := e.get(t, "/api/v1/operations")
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Items, len(defaultActions()))
	var reconcile map[string]any
	for _, it := range got.Items {
		if it["name"] == "reconcile-apply" {
			reconcile = it
		}
	}
	require.Equal(t, map[string]any{
		"name": "reconcile-apply", "title": "Reconcile artist associations", "group": "Reconcile",
		"description": reconcile["description"], "available": false,
		"unavailable_reason": "No database found; run a sync or rebuild it from the snapshot first",
		"uses_network":       false, "destructive": true,
		"links": map[string]any{"page": "/operations/reconcile-apply"},
	}, reconcile)
	require.NotEmpty(t, reconcile["description"])
}

func TestOperationRoutesAbsentWithoutOperator(t *testing.T) {
	e := newJobsEnv(t) // Maintenance only
	rec := e.get(t, "/operations")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Spotify credentials")
	require.NotContains(t, body, "Check data freshness")
	for _, target := range []string{"/operations/sync", "/api/v1/operations", "/api/v1/freshness"} {
		require.Equal(t, http.StatusNotFound, e.get(t, target).Code, target)
	}
	for _, target := range []string{"/operations/freshness", "/operations/sync/preview", "/operations/sync/run"} {
		require.Equal(t, http.StatusNotFound, e.post(target, e.form(), "http://"+host).Code, target)
	}
}
