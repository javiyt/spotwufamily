package httpadmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// Resolver is the set of profile review use cases the adapter consumes. A
// Service that implements it and reports Resolvable gets the /resolve pages.
type Resolver interface {
	Resolvable() bool
	ResolveQueue(ctx context.Context, q admin.ResolveQueueQuery) (admin.ResolveQueuePage, error)
	ResolveWorkbench(ctx context.Context, slug, query string) (admin.Workbench, error)
	PreviewResolution(ctx context.Context, req admin.ResolveRequest) (admin.ResolvePreview, error)
	ApplyResolution(ctx context.Context, req admin.ResolveRequest) (admin.ResolveResult, error)
}

const (
	maxFormText = 2000
	maxQueryLen = 300
)

func (s *Server) registerResolver() {
	s.routes["/resolve"] = route{methods: []string{http.MethodGet}, handler: s.handleResolveQueue}
	s.routes["/api/v1/resolve"] = route{methods: []string{http.MethodGet}, handler: s.apiResolveQueue}
	if s.dynamic == nil {
		s.dynamic = http.NewServeMux()
	}
	s.dynamic.HandleFunc("GET /resolve/{slug}", s.handleWorkbench)
	s.dynamic.HandleFunc("GET /api/v1/resolve/{slug}", s.apiWorkbench)
	s.dynamic.HandleFunc("POST /resolve/{slug}/preview", s.handleResolvePreview)
	s.dynamic.HandleFunc("POST /resolve/{slug}/apply", s.handleResolveApply)
}

// --- queue ---

type filterLink struct {
	Value  string
	Label  string
	Count  int
	URL    string
	Active bool
}

var queueLabels = map[admin.QueueFilter]string{
	admin.QueueAll: "All", admin.QueueUnresolved: "Unresolved", admin.QueueSuspect: "Suspect",
	admin.QueueNeedsReview: "Needs review", admin.QueueUnverified: "Unverified", admin.QueueRejected: "Previously rejected",
	admin.QueueMultiple: "Multiple IDs",
}

func queueQuery(r *http.Request) admin.ResolveQueueQuery {
	q := admin.ResolveQueueQuery{ListQuery: listQuery(r)}
	if f := admin.QueueFilter(r.URL.Query().Get("filter")); admin.IsKnownQueueFilter(f) {
		q.Filter = f
	}
	return q
}

func (s *Server) handleResolveQueue(w http.ResponseWriter, r *http.Request) {
	q := queueQuery(r)
	page, err := s.resolver.ResolveQueue(r.Context(), q)
	if err != nil {
		s.resolveFailed(w, r, err)
		return
	}
	counts := map[admin.QueueFilter]int{
		admin.QueueAll: page.Counts.All, admin.QueueUnresolved: page.Counts.Unresolved, admin.QueueSuspect: page.Counts.Suspect,
		admin.QueueNeedsReview: page.Counts.NeedsReview, admin.QueueUnverified: page.Counts.Unverified,
		admin.QueueRejected: page.Counts.Rejected, admin.QueueMultiple: page.Counts.Multiple,
	}
	var links []filterLink
	for _, f := range admin.QueueFilters() {
		v := url.Values{}
		if q.Search != "" {
			v.Set("q", q.Search)
		}
		if f != admin.QueueAll {
			v.Set("filter", string(f))
		}
		u := "/resolve"
		if enc := v.Encode(); enc != "" {
			u += "?" + enc
		}
		links = append(links, filterLink{Value: string(f), Label: queueLabels[f], Count: counts[f], URL: u, Active: f == page.Filter})
	}
	s.render(w, r, "resolve_queue", "Spotify profile review", map[string]any{
		"Page": page.Page, "Filters": links, "Search": q.Search, "Filter": string(page.Filter),
		"SuspectKnown": page.SuspectKnown, "Live": page.Live,
		"Pager": newPager("/resolve", r.URL.Query(), page.Page.Page, page.Page.Pages, page.Page.Total, page.Page.PageSize, page.Page.Search),
	})
}

func (s *Server) apiResolveQueue(w http.ResponseWriter, r *http.Request) {
	page, err := s.resolver.ResolveQueue(r.Context(), queueQuery(r))
	if err != nil {
		s.resolveFailed(w, r, err)
		return
	}
	type row struct {
		Slug    string   `json:"slug"`
		Name    string   `json:"name"`
		Order   int      `json:"editorial_order"`
		Enabled bool     `json:"enabled"`
		Primary string   `json:"primary_spotify_id,omitempty"`
		IDs     int      `json:"spotify_id_count"`
		Flags   []string `json:"flags"`
		Reasons []string `json:"reasons"`
	}
	writeJSON(w, http.StatusOK, struct {
		apiPage[row]
		Counts       map[string]int `json:"counts"`
		Filter       string         `json:"filter"`
		SuspectKnown bool           `json:"suspect_known"`
	}{toAPIPage(page.Page, func(q admin.QueueRow) row {
		flags := make([]string, 0, len(q.Flags))
		for _, f := range q.Flags {
			flags = append(flags, string(f))
		}
		return row{q.Slug, q.Name, q.Order, q.Enabled, q.Primary, q.IDs, flags, append([]string{}, q.Reasons...)}
	}), map[string]int{
		"all": page.Counts.All, "unresolved": page.Counts.Unresolved, "suspect": page.Counts.Suspect,
		"needs_review": page.Counts.NeedsReview, "unverified": page.Counts.Unverified, "rejected": page.Counts.Rejected,
		"multiple": page.Counts.Multiple,
	}, string(page.Filter), page.SuspectKnown})
}

// --- workbench ---

type workbenchPage struct {
	WB            admin.Workbench
	Query         string
	EvidenceTypes []option
	Error         string
	// Reviewer is the name last typed, echoed back after a failed submission.
	Reviewer string
	CSRF     string
}

// cardView is what the card template renders: one profile plus its page.
type cardView struct {
	Card admin.Card
	Page workbenchPage
	Zone string
}

func (s *Server) workbenchPage(r *http.Request, wb admin.Workbench, q string) workbenchPage {
	opts := []option{{"", "— choose a source type —"}}
	for _, t := range admin.EvidenceTypes() {
		opts = append(opts, option{string(t), strings.ReplaceAll(string(t), "_", " ")})
	}
	return workbenchPage{WB: wb, Query: q, EvidenceTypes: opts, CSRF: s.sessions.csrf(s.sessionID(r))}
}

func cleanQuery(r *http.Request) string {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
	return q
}

func (s *Server) handleWorkbench(w http.ResponseWriter, r *http.Request) {
	q := cleanQuery(r)
	wb, err := s.resolver.ResolveWorkbench(r.Context(), r.PathValue("slug"), q)
	if err != nil {
		s.resolveFailed(w, r, err)
		return
	}
	s.render(w, r, "resolve", "Review "+wb.Artist.Name, s.workbenchPage(r, wb, q))
}

type apiNotice struct {
	Level             string `json:"level"`
	Source            string `json:"source"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

func apiNotices(in []admin.Notice) []apiNotice {
	out := make([]apiNotice, 0, len(in))
	for _, n := range in {
		out = append(out, apiNotice{n.Level, n.Source, n.Message, seconds(n.RetryAfter)})
	}
	return out
}

func seconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

func (s *Server) apiWorkbench(w http.ResponseWriter, r *http.Request) {
	wb, err := s.resolver.ResolveWorkbench(r.Context(), r.PathValue("slug"), cleanQuery(r))
	if err != nil {
		s.resolveFailed(w, r, err)
		return
	}
	type card struct {
		SpotifyID      string   `json:"spotify_id"`
		URI            string   `json:"uri"`
		URL            string   `json:"url"`
		Name           string   `json:"name,omitempty"`
		ImageURL       string   `json:"image_url,omitempty"`
		Genres         []string `json:"genres"`
		Followers      int      `json:"followers"`
		Linked         bool     `json:"linked"`
		Role           string   `json:"role,omitempty"`
		Status         string   `json:"status"`
		Reviewer       string   `json:"reviewer,omitempty"`
		ReviewedAt     string   `json:"reviewed_at,omitempty"`
		Owner          string   `json:"owner,omitempty"`
		Score          int      `json:"score,omitempty"`
		Confidence     string   `json:"confidence,omitempty"`
		GateStatus     string   `json:"gate_status,omitempty"`
		GateReasons    []string `json:"gate_reasons,omitempty"`
		Classification string   `json:"classification"`
		Explanation    string   `json:"explanation"`
		Contradictions []string `json:"contradictions"`
		Missing        []string `json:"missing_evidence"`
		Warnings       []string `json:"warnings"`
		Actions        []string `json:"actions"`
	}
	conv := func(in []admin.Card) []card {
		out := make([]card, 0, len(in))
		for _, c := range in {
			item := card{
				SpotifyID: c.SpotifyID, URI: c.URI, URL: c.URL, Name: c.Name, ImageURL: c.ImageURL, Genres: nonNilStrings(c.Genres),
				Followers: c.Followers, Linked: c.Linked, Role: string(c.Role), Status: string(c.Status), Reviewer: c.Reviewer,
				ReviewedAt: c.ReviewedAt, Owner: c.Owner, Score: c.Score, Confidence: c.Confidence,
				Classification: string(c.Audit.Classification), Explanation: c.Audit.Explanation,
				Contradictions: nonNilStrings(c.Audit.Contradictions), Missing: nonNilStrings(c.Audit.MissingEvidence),
				Warnings: nonNilStrings(c.Warnings), Actions: []string{},
			}
			if c.Gate != nil {
				item.GateStatus, item.GateReasons = string(c.Gate.Status), nonNilStrings(c.Gate.Reasons)
			}
			for _, a := range c.Actions {
				item.Actions = append(item.Actions, a.Name)
			}
			out = append(out, item)
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artist":   map[string]any{"slug": wb.Artist.Slug, "name": wb.Artist.Name, "enabled": wb.Artist.Enabled},
		"revision": wb.Revision, "editable": wb.Editable, "search_available": wb.SearchAvailable, "query": wb.Query,
		"attached": conv(wb.Attached), "history": conv(wb.History), "candidates": conv(wb.Candidates),
		"notices": apiNotices(wb.Notices),
	})
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// --- decisions ---

type confirmPage struct {
	Preview       admin.ResolvePreview
	Fields        []hiddenField
	Query         string
	Back          string
	Error         string
	NeedsOverride bool
	Override      string
	Evidence      []evidenceRow
	// Confirmable is false when the preview failed and there is nothing to apply.
	Confirmable bool
	ActionLabel string
}

var actionLabels = map[string]string{
	admin.ActionVerify: "Verify", string(artists.MapAcceptPrimary): "Accept as primary", string(artists.MapAddSecondary): "Add as reviewed secondary",
	string(artists.MapReject): "Reject", string(artists.MapInconclusive): "Mark inconclusive",
	string(artists.MapQuarantine): "Quarantine", string(artists.MapRemove): "Remove", string(artists.MapReorderPrimary): "Make primary",
}

// resolveForm reads a decision. Every value is length-capped; domain rules are
// left to the use case.
func resolveForm(slug string, v url.Values) (admin.ResolveRequest, string) {
	field := func(name string) string {
		value := strings.TrimSpace(v.Get(name))
		if len(value) > maxFormText {
			value = value[:maxFormText]
		}
		return value
	}
	req := admin.ResolveRequest{
		Slug: slug, SpotifyID: field("spotify_id"), Action: field("action"), Revision: field("revision"),
		Reviewer: field("reviewer"), Note: field("note"), OverrideReason: field("override_reason"),
		Verify: v.Get("verify") != "", Enable: v.Get("enable") != "",
	}
	if t, u, rationale := field("evidence_type"), field("evidence_url"), field("evidence_rationale"); t != "" || u != "" || rationale != "" {
		req.Evidence = []catalog.Evidence{{Type: catalog.EvidenceType(t), URL: u, Rationale: rationale}}
	}
	q := field("q")
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
	return req, q
}

func echoDecision(v url.Values) []hiddenField {
	var out []hiddenField
	for _, name := range []string{"spotify_id", "action", "revision", "reviewer", "note", "evidence_type", "evidence_url", "evidence_rationale", "verify", "enable", "q"} {
		if value := v.Get(name); value != "" {
			if len(value) > maxFormText {
				value = value[:maxFormText]
			}
			out = append(out, hiddenField{name, value})
		}
	}
	return out
}

func backURL(slug, q string) string {
	u := "/resolve/" + url.PathEscape(slug)
	if q != "" {
		u += "?q=" + url.QueryEscape(q)
	}
	return u
}

// postChecked parses a mutating request and checks its CSRF token (no
// editability check; see postForm for catalog writes).
func (s *Server) postChecked(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		s.formError(w, r, err)
		return false
	}
	if !s.sessions.checkCSRF(s.sessionID(r), r.PostForm.Get("csrf_token")) {
		s.fail(w, r, http.StatusForbidden, "invalid_csrf", "invalid or missing CSRF token")
		return false
	}
	return true
}

func (s *Server) handleResolvePreview(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	slug := r.PathValue("slug")
	req, q := resolveForm(slug, r.PostForm)
	preview, err := s.resolver.PreviewResolution(r.Context(), req)

	page := confirmPage{
		Preview: preview, Fields: echoDecision(r.PostForm), Query: q, Back: backURL(slug, q),
		ActionLabel: actionLabels[req.Action], Override: req.OverrideReason,
	}
	for _, e := range req.Evidence {
		page.Evidence = append(page.Evidence, evidenceRow{string(e.Type), e.URL, e.Rationale})
	}
	if err == nil {
		page.Confirmable = true
		s.render(w, r, "resolve_confirm", "Confirm profile change", page)
		return
	}
	status, message, overridable := s.classifyResolveError(err)
	if status == 0 {
		s.resolveFailed(w, r, err)
		return
	}
	page.Error, page.NeedsOverride = message, overridable
	s.renderStatus(w, r, status, "resolve_confirm", "Profile change not allowed", page)
}

func (s *Server) handleResolveApply(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	slug := r.PathValue("slug")
	req, q := resolveForm(slug, r.PostForm)
	req.OverrideReason = strings.TrimSpace(r.PostForm.Get("override_reason"))
	req.Confirm = r.PostForm.Get("confirm") == "1"
	if !req.Confirm {
		s.fail(w, r, http.StatusUnprocessableEntity, "confirmation_required", "the change must be confirmed")
		return
	}
	result, err := s.resolver.ApplyResolution(r.Context(), req)
	if err != nil {
		status, message, overridable := s.classifyResolveError(err)
		if status == 0 {
			s.resolveFailed(w, r, err)
			return
		}
		s.renderStatus(w, r, status, "resolve_confirm", "Profile change not saved", confirmPage{
			Fields: echoDecision(r.PostForm), Query: q, Back: backURL(slug, q), Error: message, NeedsOverride: overridable, ActionLabel: actionLabels[req.Action],
		})
		return
	}
	message := "Saved: " + actionLabels[req.Action] + " " + req.SpotifyID + "."
	if len(result.Preview.Impact.Notes) > 0 {
		message += " " + result.Preview.Impact.Notes[len(result.Preview.Impact.Notes)-1]
	}
	if result.LogError != "" {
		message += " Warning: " + result.LogError + "."
	}
	s.catalogChanged(w, r, message, "/resolve/"+url.PathEscape(slug))
}

// classifyResolveError maps a use-case error to a status and a message safe to
// show. A zero status means the error is unexpected and must be logged.
func (s *Server) classifyResolveError(err error) (status int, message string, overridable bool) {
	var (
		invalid  *admin.ValidationError
		conflict *admin.ConflictError
		confirm  *admin.ConfirmationError
	)
	switch {
	case errors.Is(err, admin.ErrOverrideRequired):
		return http.StatusUnprocessableEntity, "Automated evidence contradicts this profile. To verify it anyway, state why in the override reason; it is recorded with your name.", true
	case errors.As(err, &conflict):
		return http.StatusConflict, "This artist changed since you opened it (another tab, the CLI or a manual edit). Reload the page and decide again.", false
	case errors.As(err, &invalid):
		parts := make([]string, 0, len(invalid.Issues))
		for _, issue := range invalid.Issues {
			parts = append(parts, issue.Error())
		}
		return http.StatusUnprocessableEntity, "The change would make the catalog invalid: " + strings.Join(parts, "; "), false
	case errors.As(err, &confirm):
		return http.StatusUnprocessableEntity, "The change must be confirmed.", false
	case errors.Is(err, artists.ErrInvalidMapping), errors.Is(err, catalog.ErrInvalidTransition):
		return http.StatusUnprocessableEntity, cleanMappingMessage(err), false
	}
	return 0, "", false
}

// cleanMappingMessage keeps the human sentence of a mapping error.
func cleanMappingMessage(err error) string {
	message := err.Error()
	message = strings.ReplaceAll(message, artists.ErrInvalidMapping.Error()+": ", "")
	return message
}

// resolveFailed answers the errors that are not part of a decision form.
func (s *Server) resolveFailed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, admin.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "artist not found")
	case errors.Is(err, admin.ErrReadOnly):
		s.fail(w, r, http.StatusForbidden, "read_only", "the catalog is read-only")
	default:
		s.internal(w, r, err)
	}
}
