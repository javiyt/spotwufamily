package httpadmin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const refreshHint = "Catalog changed — the database, snapshot and exports may need refreshing."

func TestFlashShowsRefreshNeededAfterYamlEdit(t *testing.T) {
	e := newEditorEnv(t)
	rec := e.post("/artists/rza", artistForm("rza", e.revision("rza"), "name", "RZA", "category", "producer", "intent", "save"))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	body := e.get(rec.Header().Get("Location")).Body.String()
	require.Contains(t, body, "Saved RZA.")
	require.Contains(t, body, refreshHint)
	require.Contains(t, body, `<a href="/operations">Review in Operations</a>`)
	require.NotContains(t, e.get("/artists/rza").Body.String(), refreshHint, "the hint shows once")

	// A preview writes nothing and gives no hint.
	form := artistForm("rza", e.revision("rza"), "name", "RZA", "category", "producer", "intent", "preview")
	require.NotContains(t, e.post("/artists/rza", form).Body.String(), refreshHint)

	// Fetch clients get the hint in the JSON and on the page they go to.
	form = artistForm("rza", e.revision("rza"), "name", "RZA", "category", "producer", "intent", "save")
	form.Set("csrf_token", e.csrf)
	req := httptest.NewRequest(http.MethodPost, "/artists/rza", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+host)
	req.Header.Set("X-Admin-Fetch", "1")
	req.AddCookie(e.cookie)
	fr := httptest.NewRecorder()
	e.h.ServeHTTP(fr, req)
	require.Equal(t, http.StatusOK, fr.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(fr.Body.Bytes(), &got))
	require.Equal(t, "Saved RZA.", got["message"])
	require.Contains(t, got["hint"], "Catalog changed")
	require.Equal(t, "/operations", got["hint_link"])
	require.Contains(t, e.get("/artists/rza").Body.String(), refreshHint)

	// A failed save gives no hint.
	bad := artistForm("rza", "stale-revision", "name", "RZA", "category", "producer", "intent", "save")
	require.NotContains(t, e.post("/artists/rza", bad).Body.String(), refreshHint)
	require.NotContains(t, e.get("/artists/rza").Body.String(), refreshHint)
}

func TestFlashShowsRefreshNeededAfterProfileDecision(t *testing.T) {
	env := newResolveEnv(t, nil)
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	rec := env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1"))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	body := env.get(rec.Header().Get("Location")).Body.String()
	require.Contains(t, body, "Saved:")
	require.Contains(t, body, refreshHint)
}

func TestCatalogWriteMarksFreshnessStale(t *testing.T) {
	env, op := newEditOpsEnv(t)
	require.Equal(t, http.StatusSeeOther, env.post("/operations/freshness", url.Values{}).Code)
	require.EqualValues(t, 1, op.freshCalls.Load())
	require.NotContains(t, env.get("/operations").Body.String(), "Catalog changed since last check")

	rec := env.post("/artists/newcomer/archive", url.Values{"revision": {env.revision("newcomer")}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.EqualValues(t, 1, op.freshCalls.Load(), "a write never computes freshness")
	require.Contains(t, env.get("/operations").Body.String(), "Catalog changed since last check")
	require.Contains(t, env.get("/publish").Body.String(), "Catalog changed since last check")
	var api map[string]any
	require.NoError(t, json.Unmarshal(env.get("/api/v1/freshness").Body.Bytes(), &api))
	require.Equal(t, true, api["stale"])

	// The next check recomputes even within the 60 s cache and clears it.
	require.Equal(t, http.StatusSeeOther, env.post("/operations/freshness", url.Values{}).Code)
	require.EqualValues(t, 2, op.freshCalls.Load())
	require.NotContains(t, env.get("/operations").Body.String(), "Catalog changed since last check")
}

func TestCatalogWriteDuringFreshnessCheckStaysStale(t *testing.T) {
	env, op := newEditOpsEnv(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	op.freshEntered, op.freshRelease = entered, release
	done := make(chan int, 1)
	go func() { done <- env.post("/operations/freshness", url.Values{}).Code }()
	<-entered
	require.Equal(t, http.StatusSeeOther, env.post("/artists/newcomer/archive", url.Values{"revision": {env.revision("newcomer")}}).Code)
	close(release)
	require.Equal(t, http.StatusSeeOther, <-done)
	require.Contains(t, env.get("/operations").Body.String(), "Catalog changed since last check",
		"a check that started before the write does not clear the marker")
}
