package httpadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

// pendingOp is an Operator that also reports pending catalog changes.
type pendingOp struct {
	*fakeOp
	view  maintenance.PendingView
	err   error
	calls atomic.Int32
}

func (p *pendingOp) Pending(context.Context) (maintenance.PendingView, error) {
	p.calls.Add(1)
	return p.view, p.err
}

var _ httpadmin.PendingProvider = (*pendingOp)(nil)

func pendingActions() []maintenance.ActionInfo {
	acts := defaultActions()
	for _, n := range []string{"update-website", "reconcile-all-dry-run", "reconcile-all-apply", "db-seed", "release-pr"} {
		acts = append(acts, maintenance.ActionInfo{Name: n, Title: n, Available: true})
	}
	return acts
}

func stages() []maintenance.StageView {
	return []maintenance.StageView{
		{Name: "validate", Title: "Validate the catalog", Need: maintenance.NeedRequired, Status: maintenance.StageDone},
		{Name: "reconcile", Title: "Reconcile all artists", Need: maintenance.NeedRequired, Status: maintenance.StageTodo, Action: "reconcile-all-apply"},
		{Name: "seed", Title: "Seed the database from the catalog", Need: maintenance.NeedRequired, Status: maintenance.StageTodo, Action: "db-seed"},
		{Name: "sync", Title: "Sync from Spotify", Need: maintenance.NeedOptional, Status: maintenance.StageTodo, Action: "sync"},
		{Name: "build", Title: "Build the site locally", Need: maintenance.NeedManual, Status: maintenance.StageUnknown, Note: "build the site locally with make site-build"},
		{Name: "release-publish", Title: "Publish the release", Need: maintenance.NeedRequired, Status: maintenance.StageBlocked,
			BlockedReason: "Prepare the release first", Action: "release-publish"},
		{Name: "pr", Title: "Open the pull request", Need: maintenance.NeedRequired, Status: maintenance.StageTodo, Action: "release-pr"},
		{Name: "deploy", Title: "GitHub Pages deploy", Need: maintenance.NeedManual, Status: maintenance.StageUnknown},
	}
}

func pendingView() maintenance.PendingView {
	return maintenance.PendingView{
		State: maintenance.PendingChanges,
		Changes: []artists.ArtistChange{
			{Slug: "rza", Kinds: []artists.ChangeKind{artists.ChangeIDsAdded, artists.ChangeVerified}, NeedsReconcile: true, NeedsSync: true},
			{Slug: "<b>evil</b>", Kinds: []artists.ChangeKind{artists.ChangeMetadata}},
		},
		Stages: stages(),
	}
}

func newPendingEnv(t *testing.T, v maintenance.PendingView, mutate ...func(*pendingOp)) (*pendingOp, http.Handler, *http.Cookie) {
	t.Helper()
	f := &fakeOp{actions: pendingActions(), previews: map[string]maintenance.Preview{}, eligible: map[string]string{}}
	f.dashboard = admin.Dashboard{DatabaseAvailable: true, SpotifyCredentialsPresent: true}
	p := &pendingOp{fakeOp: f, view: v}
	for _, m := range mutate {
		m(p)
	}
	srv, err := httpadmin.New(p, httpadmin.Options{Addr: host, Token: token, Now: func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }})
	require.NoError(t, err)
	h := srv.Handler()
	return p, h, login(t, h)
}

func TestPendingPanelRendersChangesAndStages(t *testing.T) {
	_, h, c := newPendingEnv(t, pendingView())
	body := do(t, h, http.MethodGet, "/operations", withCookie(c)).Body.String()
	require.Contains(t, body, `id="pending"`)
	require.Less(t, strings.Index(body, `id="pending"`), strings.Index(body, `id="freshness"`), "panel above freshness")
	require.Contains(t, body, "2 artists modified since last database reconciliation")
	require.Contains(t, body, `href="/artists/rza"`)
	require.Contains(t, body, "Spotify IDs added")
	require.Contains(t, body, "needs reconcile")
	require.Contains(t, body, "needs Spotify sync")
	require.Contains(t, body, "database seed only")
	require.Contains(t, body, "Not every edit needs a Spotify")
	require.NotContains(t, body, "<b>evil</b>", "slugs are escaped")
	require.Contains(t, body, "&lt;b&gt;evil&lt;/b&gt;")
	require.NotRegexp(t, `(/Users/|/home/|/tmp/|/var/)`, body)
	// ordered checklist with CTA to the preview page
	require.Contains(t, body, `href="/operations/db-seed"`)
	require.Contains(t, body, `href="/operations/update-website"`)
	require.Contains(t, body, "Review plan and update website")
	require.Contains(t, body, "make site-build")
	require.Less(t, strings.Index(body, "Validate the catalog"), strings.Index(body, "Reconcile all artists"))
}

func TestPendingBlockedStageIsDisabledWithReason(t *testing.T) {
	_, h, c := newPendingEnv(t, pendingView())
	body := do(t, h, http.MethodGet, "/operations", withCookie(c)).Body.String()
	require.Contains(t, body, "Prepare the release first")
	require.Contains(t, body, `class="button disabled" aria-disabled="true"`)
	// the blocked stage's action has no live link inside the pending panel
	panel := body[strings.Index(body, `id="pending"`):strings.Index(body, `id="freshness"`)]
	require.NotContains(t, panel, `href="/publish"`)
	require.NotContains(t, panel, `href="/operations/release-publish"`)
}

func TestPendingUpToDateOnlyWhenClean(t *testing.T) {
	for _, st := range []maintenance.PendingState{maintenance.PendingChanges, maintenance.PendingUnknown, maintenance.PendingOffline} {
		v := pendingView()
		v.State = st
		_, h, c := newPendingEnv(t, v)
		body := do(t, h, http.MethodGet, "/operations", withCookie(c)).Body.String()
		require.NotContains(t, strings.ToLower(body), "up to date", string(st))
	}
	_, h, c := newPendingEnv(t, maintenance.PendingView{State: maintenance.PendingClean, Stages: stages()})
	body := do(t, h, http.MethodGet, "/operations", withCookie(c)).Body.String()
	require.Contains(t, body, "Up to date")
	require.Contains(t, body, "Local database up to date")
	require.Contains(t, body, "Published website updated: not confirmed")
	require.NotContains(t, body, `class="banner`)
}

func TestPendingUnknownOffersBaselineCheck(t *testing.T) {
	_, h, c := newPendingEnv(t, maintenance.PendingView{State: maintenance.PendingUnknown, Stages: stages()})
	rec := do(t, h, http.MethodGet, "/operations", withCookie(c))
	body := rec.Body.String()
	require.Contains(t, body, "catalog state is unknown")
	require.Contains(t, body, `href="/operations/reconcile-all-dry-run"`)
	require.Contains(t, body, "Database catalog state unknown")
	require.Contains(t, body, `href="/operations#pending"`)
}

func TestPendingBannerAfterExternalEditInFreshSession(t *testing.T) {
	_, h, _ := newPendingEnv(t, pendingView())
	// A brand new session (no flash, no history) still sees the banner on any page.
	c2 := login(t, h)
	for _, p := range []string{"/", "/artists", "/review", "/operations"} {
		body := do(t, h, http.MethodGet, p, withCookie(c2)).Body.String()
		require.Contains(t, body, "2 artists modified since last database reconciliation", p)
		require.Contains(t, body, `href="/operations#pending"`, p)
	}
}

func TestPendingBannerAbsentWhenCleanAndOnNonHTML(t *testing.T) {
	p, h, c := newPendingEnv(t, maintenance.PendingView{State: maintenance.PendingClean, Stages: stages()})
	require.NotContains(t, do(t, h, http.MethodGet, "/artists", withCookie(c)).Body.String(), "modified since last database")
	p.view = pendingView()
	p.calls.Store(0)
	for _, path := range []string{"/api/v1/status", "/static/admin.css", "/api/v1/freshness"} {
		do(t, h, http.MethodGet, path, withCookie(c))
	}
	require.Zero(t, p.calls.Load(), "non-HTML endpoints never compute pending")
	// login page is not authenticated and renders no banner
	body := do(t, h, http.MethodGet, "/login", nil).Body.String()
	require.NotContains(t, body, "modified since")
	require.Zero(t, p.calls.Load())
}

func TestPendingComputedOncePerRequest(t *testing.T) {
	p, h, c := newPendingEnv(t, pendingView())
	do(t, h, http.MethodGet, "/operations", withCookie(c))
	require.EqualValues(t, 1, p.calls.Load())
	do(t, h, http.MethodGet, "/artists", withCookie(c))
	require.EqualValues(t, 1, p.calls.Load(), "short TTL cache is reused")
}

func TestPendingErrorHidesBannerAndShowsNotice(t *testing.T) {
	_, h, c := newPendingEnv(t, maintenance.PendingView{}, func(p *pendingOp) { p.err = errors.New("open /Users/x/db: boom") })
	rec := do(t, h, http.MethodGet, "/operations", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "/Users/x")
	require.NotContains(t, rec.Body.String(), "modified since")
}

func TestUpdateWebsiteCTADisabledWhenUnavailable(t *testing.T) {
	_, h, c := newPendingEnv(t, pendingView(), func(p *pendingOp) {
		for i := range p.actions {
			if p.actions[i].Name == "update-website" {
				p.actions[i].Available = false
				p.actions[i].UnavailableReason = "The editorial catalog does not validate"
			}
		}
	})
	body := do(t, h, http.MethodGet, "/operations", withCookie(c)).Body.String()
	panel := body[strings.Index(body, `id="pending"`):strings.Index(body, `id="freshness"`)]
	require.Contains(t, panel, "The editorial catalog does not validate")
	require.NotContains(t, panel, `href="/operations/update-website"`)
	require.Contains(t, panel, `aria-disabled="true"`)
}

var prLine = regexp.MustCompile(`(?s)<li class="status-pr">(.*?)</li>`)

func TestPendingHonestLabels(t *testing.T) {
	v := pendingView()
	v.Stages = append([]maintenance.StageView(nil), v.Stages...)
	for i := range v.Stages {
		if v.Stages[i].Name == "pr" {
			v.Stages[i].Status = maintenance.StageDone
		}
	}
	_, h, c := newPendingEnv(t, v)
	body := do(t, h, http.MethodGet, "/operations", withCookie(c)).Body.String()
	m := prLine.FindStringSubmatch(body)
	require.NotNil(t, m)
	require.Contains(t, m[1], "Pull request open")
	require.Contains(t, m[1], "waiting for merge and GitHub Pages deploy")
	require.NotContains(t, strings.ToLower(m[1]), "published")
	require.Contains(t, body, "Published website updated: not confirmed")
	require.Contains(t, body, "happen on GitHub")
}

func TestAPIPending(t *testing.T) {
	_, h, c := newPendingEnv(t, pendingView())
	rec := do(t, h, http.MethodGet, "/api/v1/pending", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	var got struct {
		State          string `json:"state"`
		PendingChanges int    `json:"pending_changes"`
		Changes        []struct {
			Slug           string   `json:"slug"`
			Kinds          []string `json:"kinds"`
			NeedsReconcile bool     `json:"needs_reconcile"`
			NeedsSync      bool     `json:"needs_sync"`
		} `json:"changes"`
		Stages []struct {
			Name, Title, Need, Status string
			BlockedReason             string `json:"blocked_reason"`
			Action                    string `json:"action"`
		} `json:"stages"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "pending", got.State)
	require.Equal(t, 2, got.PendingChanges)
	require.Equal(t, "rza", got.Changes[0].Slug)
	require.Equal(t, []string{"ids_added", "verified"}, got.Changes[0].Kinds)
	require.True(t, got.Changes[0].NeedsSync)
	require.Len(t, got.Stages, len(stages()))
	require.Equal(t, "blocked", got.Stages[5].Status)
	require.Equal(t, "Prepare the release first", got.Stages[5].BlockedReason)
	require.Equal(t, "db-seed", got.Stages[2].Action)

	// unauthenticated is refused like the other API routes
	require.NotEqual(t, http.StatusOK, do(t, h, http.MethodGet, "/api/v1/pending", nil).Code)
}

func TestAPIPendingAbsentWithoutProvider(t *testing.T) {
	e := newOpsEnv(t)
	require.Equal(t, http.StatusNotFound, e.get(t, "/api/v1/pending").Code)
}
