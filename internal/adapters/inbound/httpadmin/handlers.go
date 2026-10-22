package httpadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const maxSearchLength = 100

var navItems = []struct{ Path, Label string }{
	{"/", "Dashboard"},
	{"/artists", "Artists"},
	{"/groups", "Groups"},
	{"/review", "Review queue"},
	{"/operations", "Operations"},
}

// nav lists the pages, adding the profile review workbench and the jobs page
// when they are enabled.
func (s *Server) nav() []struct{ Path, Label string } {
	if s.resolver == nil && s.maint == nil {
		return navItems
	}
	out := make([]struct{ Path, Label string }, 0, len(navItems)+2)
	for _, item := range navItems {
		out = append(out, item)
		if item.Path == "/review" && s.resolver != nil {
			out = append(out, struct{ Path, Label string }{"/resolve", "Spotify profiles"})
		}
		if item.Path == "/operations" && s.maint != nil {
			out = append(out, struct{ Path, Label string }{"/jobs", "Jobs"})
		}
	}
	return out
}

type view struct {
	Flash  flash
	Title  string
	Path   string
	Nav    []struct{ Path, Label string }
	Authed bool
	CSRF   string
	Data   any
	// Refresh adds a no-JS auto refresh while a page shows live progress.
	Refresh bool
	// Reload adds an auto refresh for every client (JS or not) while a page
	// waits for a background check it cannot poll for.
	Reload bool
	// Banner is the pending-changes notice; nil when there is none.
	Banner *banner
}

type pager struct {
	Path     string
	Search   string
	Page     int
	Pages    int
	Total    int
	PrevURL  string
	NextURL  string
	HasPrev  bool
	HasNext  bool
	PageSize int
}

// newPager builds pagination links that keep every active filter and sort.
func newPager(path string, query url.Values, page, pages, total, size int, search string) pager {
	p := pager{Path: path, Search: search, Page: page, Pages: pages, Total: total, PageSize: size, HasPrev: page > 1, HasNext: page < pages}
	link := func(n int) string {
		v := url.Values{}
		for key, values := range query {
			if key != "page" {
				v[key] = append([]string(nil), values...)
			}
		}
		v.Set("page", strconv.Itoa(n))
		return path + "?" + v.Encode()
	}
	p.PrevURL, p.NextURL = link(page-1), link(page+1)
	return p
}

func listQuery(r *http.Request) admin.ListQuery {
	v := r.URL.Query()
	search := strings.TrimSpace(v.Get("q"))
	if len(search) > maxSearchLength {
		search = search[:maxSearchLength]
	}
	page, _ := strconv.Atoi(v.Get("page"))
	size, _ := strconv.Atoi(v.Get("page_size"))
	q := admin.ListQuery{Search: search, Page: page, PageSize: size}

	// Unknown filter values are ignored rather than failing the page.
	if c := catalog.Category(v.Get("category")); catalog.IsKnownCategory(c) {
		q.Category = c
	}
	switch v.Get("enabled") {
	case "yes", "true", "1":
		t := true
		q.Enabled = &t
	case "no", "false", "0":
		f := false
		q.Enabled = &f
	}
	switch st := admin.IDState(v.Get("ids")); st {
	case admin.IDVerified, admin.IDPending, admin.IDUnresolved:
		q.IDState = st
	}
	if t := catalog.RelationshipType(v.Get("rel")); catalog.IsKnownRelationshipType(t) {
		q.Relation = t
	}
	q.IncludeArchived = v.Get("archived") == "1" || v.Get("archived") == "true"
	switch k := admin.SortKey(v.Get("sort")); k {
	case admin.SortName, admin.SortSlug, admin.SortCategory, admin.SortOrder, admin.SortIDState:
		q.Sort = k
	}
	q.Desc = v.Get("dir") == "desc"
	return q
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.svc.Dashboard(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.render(w, r, "dashboard", "Dashboard", dashboardView(d))
}

func (s *Server) handleArtists(w http.ResponseWriter, r *http.Request) {
	page, err := s.svc.ListArtists(r.Context(), listQuery(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.render(w, r, "artists", "Artists", s.artistsView(r, page))
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	page, err := s.svc.ListGroups(r.Context(), listQuery(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.render(w, r, "groups", "Groups", map[string]any{
		"Page": page, "Pager": newPager("/groups", r.URL.Query(), page.Page, page.Pages, page.Total, page.PageSize, page.Search),
		"Editable": s.canEdit(),
	})
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	page, err := s.svc.ReviewQueue(r.Context(), listQuery(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.render(w, r, "review", "Review queue", map[string]any{
		"Resolver": s.resolver != nil, "Page": page, "Pager": newPager("/review", r.URL.Query(), page.Page, page.Pages, page.Total, page.PageSize, page.Search),
	})
}

type dashboardViewModel struct {
	admin.Dashboard
	LastSyncStatus string
	LastSyncAt     string
}

func dashboardView(d admin.Dashboard) dashboardViewModel {
	vm := dashboardViewModel{Dashboard: d}
	if d.LastSync != nil {
		vm.LastSyncStatus = d.LastSync.Status
		t := d.LastSync.FinishedAt
		if t.IsZero() {
			t = d.LastSync.StartedAt
		}
		if !t.IsZero() {
			vm.LastSyncAt = t.UTC().Format(time.RFC3339)
		}
	}
	return vm
}

// --- authentication ---

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	s.exchangeToken(w, r, r.URL.Query().Get("token"))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, r, "login", "Sign in", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.formError(w, r, err)
		return
	}
	s.exchangeToken(w, r, r.PostForm.Get("token"))
}

func (s *Server) exchangeToken(w http.ResponseWriter, r *http.Request, got string) {
	if s.limiter.blocked() {
		w.Header().Set("Retry-After", "60")
		s.fail(w, r, http.StatusTooManyRequests, "rate_limited", "too many failed attempts; try again later")
		return
	}
	if !tokenEqual(got, s.opts.Token) {
		s.limiter.record()
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "invalid token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.sessions.create(), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.formError(w, r, err)
		return
	}
	id := s.sessionID(r)
	if !s.sessions.checkCSRF(id, r.PostForm.Get("csrf_token")) {
		s.fail(w, r, http.StatusForbidden, "invalid_csrf", "invalid or missing CSRF token")
		return
	}
	s.sessions.destroy(id)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) formError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		s.fail(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		return
	}
	s.fail(w, r, http.StatusBadRequest, "bad_request", "malformed request")
}

// --- JSON API ---

type apiPage[T any] struct {
	Items    []T    `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Pages    int    `json:"pages"`
	Search   string `json:"search,omitempty"`
}

func toAPIPage[S, T any](p admin.Page[S], conv func(S) T) apiPage[T] {
	items := make([]T, 0, len(p.Items))
	for _, it := range p.Items {
		items = append(items, conv(it))
	}
	return apiPage[T]{Items: items, Total: p.Total, Page: p.Page, PageSize: p.PageSize, Pages: p.Pages, Search: p.Search}
}

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.svc.Dashboard(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	vm := dashboardView(d)
	writeJSON(w, http.StatusOK, map[string]any{
		"artists": d.Artists, "enabled": d.Enabled, "groups": d.Groups, "people": d.People,
		"spotify_ids":   map[string]int{"verified": d.VerifiedIDs, "pending": d.PendingIDs, "unresolved": d.UnresolvedIDs},
		"relationships": d.Relationships, "review_queue": d.ReviewQueue,
		"pending_reconciliation": d.PendingReconciliation, "database_available": d.DatabaseAvailable,
		"last_sync":                   map[string]string{"status": vm.LastSyncStatus, "at": vm.LastSyncAt},
		"spotify_credentials_present": d.SpotifyCredentialsPresent,
	})
}

func (s *Server) apiArtists(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.ListArtists(r.Context(), listQuery(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type row struct {
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		Category string `json:"category"`
		Enabled  bool   `json:"enabled"`
		IDState  string `json:"id_state"`
		IDs      int    `json:"spotify_id_count"`
		Group    bool   `json:"group"`
		Archived bool   `json:"archived"`
	}
	writeJSON(w, http.StatusOK, struct {
		apiPage[row]
		Facets map[string]int `json:"facets"`
	}{toAPIPage(p, func(a admin.ArtistRow) row {
		return row{a.Slug, a.Name, string(a.Category), a.Enabled, string(a.IDState), a.IDs, a.Group, a.Archived}
	}), map[string]int{
		"all": p.Facets.All, "enabled": p.Facets.Enabled, "disabled": p.Facets.Disabled, "archived": p.Facets.Archived,
		"verified": p.Facets.Verified, "pending": p.Facets.Pending, "unresolved": p.Facets.Unresolved,
	}})
}

func (s *Server) apiGroups(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.ListGroups(r.Context(), listQuery(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type row struct {
		Slug            string `json:"slug"`
		Name            string `json:"name"`
		IDState         string `json:"id_state"`
		VerifiedMembers int    `json:"verified_members"`
		ProposedMembers int    `json:"proposed_members"`
	}
	writeJSON(w, http.StatusOK, toAPIPage(p, func(g admin.GroupRow) row {
		return row{g.Slug, g.Name, string(g.IDState), g.VerifiedMembers, g.ProposedMembers}
	}))
}

func (s *Server) apiReview(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.ReviewQueue(r.Context(), listQuery(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type row struct {
		Kind   string `json:"kind"`
		Artist string `json:"artist"`
		Name   string `json:"name"`
		Detail string `json:"detail"`
		Status string `json:"status"`
	}
	writeJSON(w, http.StatusOK, toAPIPage(p, func(i admin.ReviewItem) row {
		return row{i.Kind, i.Artist, i.Name, i.Detail, i.Status}
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- rendering and errors ---

var templateFuncs = template.FuncMap{
	"identity": func(e catalog.IdentityEvidence) string {
		parts := []string{strings.ReplaceAll(string(e.Kind), "_", " ")}
		if e.Strong {
			parts = append(parts, "strong")
		}
		if e.ReleaseName != "" {
			release := e.ReleaseName
			if e.ReleaseYear != "" {
				release += " (" + e.ReleaseYear + ")"
			}
			parts = append(parts, "release: "+release)
		}
		if e.Detail != "" {
			parts = append(parts, e.Detail)
		}
		return strings.Join(parts, " · ")
	},
	"seconds": seconds,
	"cardCtx": func(c admin.Card, p workbenchPage, zone string) cardView {
		view := cardView{Card: c, Page: p, Zone: zone, Suggestions: suggestionsOf(c)}
		if p.Draft != nil && p.Draft.SpotifyID == c.SpotifyID {
			view.Draft = p.Draft
			for i := range view.Suggestions {
				view.Suggestions[i].Added = p.Draft.Has(view.Suggestions[i].Key)
			}
		}
		return view
	},
	"blankRow": func() evidenceDraft { return evidenceDraft{} },
	"rowCtx": func(row evidenceDraft, i int, p workbenchPage) evidenceRowView {
		return evidenceRowView{Row: row, Index: i, Types: p.EvidenceTypes, Edit: editURL(p.WB.Artist.Slug), Independent: evidenceIndependent(row), Known: row.Type != ""}
	},
	"join":  strings.Join,
	"label": func(v string) string { return strings.ReplaceAll(v, "_", " ") },
	"assess": func(c artists.ProfileClassification) string {
		switch c {
		case artists.ClassVerified:
			return "supported by evidence"
		case artists.ClassSuspicious:
			return "contradicted by evidence"
		case artists.ClassAmbiguous:
			return "ambiguous"
		case artists.ClassUnavailable:
			return "could not be checked"
		}
		return "not enough evidence"
	},
	// relView hands a relationship row its page-level context.
	"relView": func(r relRow, csrf string, editable bool) relRowView {
		return relRowView{relRow: r, CSRF: csrf, Editable: editable}
	},
}

type relRowView struct {
	relRow
	CSRF     string
	Editable bool
}

func parseTemplates() (map[string]*template.Template, error) {
	out := map[string]*template.Template{}
	for _, name := range []string{"dashboard", "artists", "groups", "review", "operations", "login", "error", "artist", "preview", "conflict", "resolve_queue", "resolve", "resolve_confirm", "jobs", "job", "operation", "operation_preview", "publish"} {
		t, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/layout.gohtml", "templates/"+name+".gohtml")
		if err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title string, data any) {
	s.renderStatus(w, r, http.StatusOK, name, title, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	v := view{Title: title, Path: r.URL.Path, Nav: s.nav(), Data: data}
	if rf, ok := data.(interface{ autoRefresh() bool }); ok {
		v.Refresh = rf.autoRefresh()
	}
	if rl, ok := data.(interface{ reload() bool }); ok {
		v.Reload = rl.reload()
	}
	if id := s.sessionID(r); s.sessions.valid(id) {
		v.Authed, v.CSRF = true, s.sessions.csrf(id)
		v.Flash = s.sessions.takeFlash(id)
		v.Banner = s.pendingBanner(r.Context())
	}
	var buf bytes.Buffer
	if err := s.templates[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		s.logger.Printf("render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func wantsJSON(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") || strings.Contains(r.Header.Get("Accept"), "application/json")
}

// fail writes a structured, non-secret error in the format the client expects.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if wantsJSON(r) {
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
		return
	}
	var buf bytes.Buffer
	v := view{Title: http.StatusText(status), Path: r.URL.Path, Nav: s.nav(),
		Data: map[string]any{"Status": status, "Message": message, "Code": code}}
	if err := s.templates["error"].ExecuteTemplate(&buf, "layout", v); err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// internal logs the real error server-side (the cause behind a safe error,
// credentials redacted) and tells the client nothing more.
func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Printf("internal error on %s %s: %s", r.Method, r.URL.Path, maintenance.LogText(err))
	s.fail(w, r, http.StatusInternalServerError, "internal", "internal error")
}
