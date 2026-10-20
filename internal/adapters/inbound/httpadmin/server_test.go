package httpadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/application/admin"
)

const (
	token = "0123456789abcdef0123456789abcdef"
	host  = "127.0.0.1:8787"
)

type fakeService struct {
	dashboard admin.Dashboard
	artists   admin.Page[admin.ArtistRow]
	err       error
	lastQuery admin.ListQuery
}

func (f *fakeService) Dashboard(context.Context) (admin.Dashboard, error) { return f.dashboard, f.err }
func (f *fakeService) ListArtists(_ context.Context, q admin.ListQuery) (admin.Page[admin.ArtistRow], error) {
	f.lastQuery = q
	return f.artists, f.err
}
func (f *fakeService) ListGroups(context.Context, admin.ListQuery) (admin.Page[admin.GroupRow], error) {
	return admin.Page[admin.GroupRow]{Page: 1, Pages: 1, PageSize: 25}, f.err
}
func (f *fakeService) ReviewQueue(context.Context, admin.ListQuery) (admin.Page[admin.ReviewItem], error) {
	return admin.Page[admin.ReviewItem]{Page: 1, Pages: 1, PageSize: 25}, f.err
}

func newServer(t *testing.T, svc *fakeService) *httpadmin.Server {
	t.Helper()
	srv, err := httpadmin.New(svc, httpadmin.Options{Addr: host, Token: token})
	require.NoError(t, err)
	return srv
}

func do(t *testing.T, h http.Handler, method, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/auth?token="+token, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/", rec.Header().Get("Location"))
	for _, c := range rec.Result().Cookies() {
		if c.Name == "swf_admin_session" {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

func TestValidateListen(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1", "127.0.0.2"} {
		require.NoError(t, httpadmin.ValidateListen(h, false, ""), h)
	}
	for _, h := range []string{"0.0.0.0", "::", "", "192.168.1.5", "example.com"} {
		require.Error(t, httpadmin.ValidateListen(h, false, token), h)
	}
	require.Error(t, httpadmin.ValidateListen("192.168.1.5", true, ""), "remote needs auth token")
	require.Error(t, httpadmin.ValidateListen("192.168.1.5", true, "short"), "remote needs a strong token")
	require.NoError(t, httpadmin.ValidateListen("192.168.1.5", true, token))
}

func TestNewRejectsWeakTokenAndNonLoopbackWithoutOptIn(t *testing.T) {
	_, err := httpadmin.New(&fakeService{}, httpadmin.Options{Addr: host, Token: "short"})
	require.Error(t, err)
	_, err = httpadmin.New(&fakeService{}, httpadmin.Options{Addr: "0.0.0.0:8787", Token: token})
	require.Error(t, err)
}

func TestUnauthenticatedRequestsAreRejectedWithoutData(t *testing.T) {
	svc := &fakeService{dashboard: admin.Dashboard{Artists: 4242}}
	h := newServer(t, svc).Handler()

	rec := do(t, h, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NotContains(t, rec.Body.String(), "4242")

	rec = do(t, h, http.MethodGet, "/api/v1/artists", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var body map[string]map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "unauthorized", body["error"]["code"])
}

func TestWrongTokenIsRejectedAndRateLimited(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	for i := 0; i < 10; i++ {
		rec := do(t, h, http.MethodGet, "/auth?token=wrong", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Empty(t, rec.Result().Cookies())
	}
	rec := do(t, h, http.MethodGet, "/auth?token="+token, nil)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestForeignHostHeaderIsRejectedEvenWhenAuthenticated(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	cookie := login(t, h)
	rec := do(t, h, http.MethodGet, "/", func(r *http.Request) { r.AddCookie(cookie); r.Host = "evil.example:8787" })
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = do(t, h, http.MethodGet, "/", func(r *http.Request) { r.AddCookie(cookie); r.Host = "localhost:8787" })
	require.Equal(t, http.StatusOK, rec.Code, "localhost alias on same port is allowed")
}

func TestSessionCookieAttributes(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	c := login(t, h)
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, c.SameSite)
	require.Equal(t, "/", c.Path)
	require.NotContains(t, c.Value, token)
}

func TestDashboardHeadersAndCredentialStatusOnly(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_SECRET", "super-secret-value-xyz")
	svc := &fakeService{dashboard: admin.Dashboard{Artists: 77, SpotifyCredentialsPresent: true}}
	h := newServer(t, svc).Handler()
	rec := do(t, h, http.MethodGet, "/", withCookie(login(t, h)))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "77")
	require.Contains(t, strings.ToLower(body), "present")
	require.NotContains(t, body, "super-secret-value-xyz")
	require.NotContains(t, body, token)

	hd := rec.Header()
	require.Equal(t, "no-store", hd.Get("Cache-Control"))
	require.Equal(t, "nosniff", hd.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", hd.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", hd.Get("Referrer-Policy"))
	csp := hd.Get("Content-Security-Policy")
	require.Contains(t, csp, "default-src 'none'")
	require.Contains(t, csp, "frame-ancestors 'none'")
	require.NotContains(t, csp, "unsafe-inline")
	require.NotContains(t, csp, "unsafe-eval")
}

func TestArtistNamesAreEscaped(t *testing.T) {
	svc := &fakeService{artists: admin.Page[admin.ArtistRow]{
		Items: []admin.ArtistRow{{Slug: "x", Name: `<script>alert(1)</script>`, IDState: admin.IDUnresolved}},
		Total: 1, Page: 1, Pages: 1, PageSize: 25, Search: `"><img src=x onerror=alert(2)>`,
	}}
	h := newServer(t, svc).Handler()
	rec := do(t, h, http.MethodGet, "/artists?q=%22%3E%3Cimg", withCookie(login(t, h)))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<script>alert(1)")
	require.NotContains(t, body, "<img src=x")
	require.Contains(t, body, "&lt;script&gt;alert(1)")
}

func TestAPIArtistsPaginationAndSearchParams(t *testing.T) {
	svc := &fakeService{artists: admin.Page[admin.ArtistRow]{
		Items: []admin.ArtistRow{{Slug: "a", Name: "A", IDState: admin.IDVerified}}, Total: 51, Page: 2, Pages: 3, PageSize: 25,
	}}
	h := newServer(t, svc).Handler()
	rec := do(t, h, http.MethodGet, "/api/v1/artists?q=wu&page=2&page_size=25", withCookie(login(t, h)))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, admin.ListQuery{Search: "wu", Page: 2, PageSize: 25}, svc.lastQuery)

	var out struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
		Page  int              `json:"page"`
		Pages int              `json:"pages"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, 51, out.Total)
	require.Equal(t, 3, out.Pages)
	require.Len(t, out.Items, 1)
}

func TestInternalErrorsAreStructuredAndDoNotLeakPaths(t *testing.T) {
	svc := &fakeService{err: errors.New("open /Users/secret/data/artists.yaml: permission denied")}
	h := newServer(t, svc).Handler()
	cookie := login(t, h)

	for _, target := range []string{"/", "/api/v1/artists"} {
		rec := do(t, h, http.MethodGet, target, withCookie(cookie))
		require.Equal(t, http.StatusInternalServerError, rec.Code, target)
		require.NotContains(t, rec.Body.String(), "/Users/secret")
		require.NotContains(t, rec.Body.String(), "permission denied")
	}
	rec := do(t, h, http.MethodGet, "/api/v1/artists", withCookie(cookie))
	require.JSONEq(t, `{"error":{"code":"internal","message":"internal error"}}`, rec.Body.String())
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	cookie := login(t, h)
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/nope", withCookie(cookie)).Code)
	require.Equal(t, http.StatusMethodNotAllowed, do(t, h, http.MethodPut, "/", withCookie(cookie)).Code)
}

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func csrfFor(t *testing.T, h http.Handler, c *http.Cookie) string {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/", withCookie(c))
	m := csrfPattern.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2, "dashboard must embed a CSRF token in its logout form")
	return m[1]
}

func post(h http.Handler, c *http.Cookie, form url.Values, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMutationsRequireCSRFAndSameOrigin(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	cookie := login(t, h)
	csrf := csrfFor(t, h, cookie)

	// Unauthenticated cross-origin write fails.
	rec := post(h, nil, url.Values{"csrf_token": {csrf}}, "https://evil.example")
	require.GreaterOrEqual(t, rec.Code, 400)

	// Authenticated but cross-origin fails even with a valid token.
	rec = post(h, cookie, url.Values{"csrf_token": {csrf}}, "https://evil.example")
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Same-origin without a CSRF token fails.
	rec = post(h, cookie, url.Values{}, "http://"+host)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Wrong CSRF token fails.
	rec = post(h, cookie, url.Values{"csrf_token": {"nope"}}, "http://"+host)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Session still valid after all the failed attempts.
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/", withCookie(cookie)).Code)

	// Valid request logs out and invalidates the session.
	rec = post(h, cookie, url.Values{"csrf_token": {csrf}}, "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/", withCookie(cookie)).Code)
}

func TestOversizedBodyIsRejected(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	cookie := login(t, h)
	csrf := csrfFor(t, h, cookie)
	rec := post(h, cookie, url.Values{"csrf_token": {csrf}, "pad": {strings.Repeat("a", 200_000)}}, "http://"+host)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestStaticAssetsServedWithSecurityHeaders(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	rec := do(t, h, http.MethodGet, "/static/admin.css", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/static/../go.mod", nil).Code)
	rec = do(t, h, http.MethodGet, "/static/admin.css", func(r *http.Request) { r.Host = "evil.example" })
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServeShutsDownGracefullyWhenContextIsCancelled(t *testing.T) {
	srv, err := httpadmin.New(&fakeService{}, httpadmin.Options{Addr: "127.0.0.1:0", Token: token})
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + ln.Addr().String() + "/static/admin.css")
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return true
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	_, err = http.Get("http://" + ln.Addr().String() + "/")
	require.Error(t, err)
}

func TestHTTPServerHasTimeoutsAndLimits(t *testing.T) {
	srv := newServer(t, &fakeService{})
	hs := srv.HTTPServer()
	require.NotZero(t, hs.ReadHeaderTimeout)
	require.NotZero(t, hs.ReadTimeout)
	require.NotZero(t, hs.WriteTimeout)
	require.NotZero(t, hs.IdleTimeout)
	require.NotZero(t, hs.MaxHeaderBytes)
}

func TestLaunchURLNeverUsedAsLogPath(t *testing.T) {
	var logs strings.Builder
	srv, err := httpadmin.New(&fakeService{}, httpadmin.Options{Addr: host, Token: token, Log: &logs})
	require.NoError(t, err)
	h := srv.Handler()
	_ = do(t, h, http.MethodGet, "/auth?token="+token, nil)
	_ = do(t, h, http.MethodGet, "/", nil)
	require.NotContains(t, logs.String(), token)
	require.Contains(t, logs.String(), "/auth")
}
