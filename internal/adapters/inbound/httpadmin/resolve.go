package httpadmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
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
	s.dynamic.HandleFunc("POST /resolve/{slug}/edit", s.handleResolveEdit)
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
	// Draft is the decision being edited, set when the page is rendered from a
	// form (adding or removing evidence, or going back from a confirmation).
	Draft *decisionDraft
}

// cardView is what the card template renders: one profile plus its page.
type cardView struct {
	Card admin.Card
	Page workbenchPage
	Zone string
	// Draft is the page's draft when it belongs to this card, else nil.
	Draft *decisionDraft
	// Suggestions are the typed sources offered for this card.
	Suggestions []suggestionView
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
	type sugg struct {
		Key         string `json:"key"`
		Type        string `json:"type"`
		URL         string `json:"url"`
		Rationale   string `json:"rationale"`
		Independent bool   `json:"independent"`
		Title       string `json:"title,omitempty"`
		Release     string `json:"release,omitempty"`
		ReleaseID   string `json:"release_id,omitempty"`
		TrackID     string `json:"track_id,omitempty"`
		ReleaseURL  string `json:"release_url,omitempty"`
		Limitation  string `json:"limitation,omitempty"`
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
		Suggestions    []sugg   `json:"suggestions"`
		Credits        []sugg   `json:"credit_suggestions"`
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
				Warnings: nonNilStrings(c.Warnings), Actions: []string{}, Suggestions: []sugg{}, Credits: []sugg{},
			}
			for _, sg := range suggestionsOf(c) {
				e := sg.Evidence
				row := sugg{sg.Key, string(e.Type), e.URL, e.Rationale, sg.Independent, sg.Title, sg.Detail, e.ReleaseID, e.TrackID, sg.ReleaseURL, sg.Limitation}
				if e.Type == catalog.EvidenceCredit {
					item.Credits = append(item.Credits, row)
				} else {
					item.Suggestions = append(item.Suggestions, row)
				}
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
	// Independent says, per Evidence row, whether it can verify on its own.
	Independent []bool
	// Edit is where "Edit decision" posts: the same slug's edit step.
	Edit string
}

var actionLabels = map[string]string{
	admin.ActionVerify: "Verify", string(artists.MapAcceptPrimary): "Accept as primary", string(artists.MapAddSecondary): "Add as reviewed secondary",
	string(artists.MapReject): "Reject", string(artists.MapInconclusive): "Mark inconclusive",
	string(artists.MapQuarantine): "Quarantine", string(artists.MapRemove): "Remove", string(artists.MapReorderPrimary): "Make primary",
}

// resolveForm reads a decision. Every value is length-capped; domain rules are
// left to the use case. Evidence arrives as repeated evidence_* keys (one group
// per row; a single group is the legacy form). The error is set when the rows
// themselves are unusable: too many, or the same item twice.
func resolveForm(slug string, v url.Values) (admin.ResolveRequest, string, error) {
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
	rows, err := filledEvidence(readEvidenceRows(v))
	for _, row := range rows {
		req.Evidence = append(req.Evidence, row.evidence())
	}
	q := field("q")
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
	return req, q, err
}

// echoDecision carries a decision through the confirmation page and back.
func echoDecision(v url.Values) []hiddenField {
	var out []hiddenField
	for _, name := range []string{"spotify_id", "action", "revision", "reviewer", "note", "verify", "enable", "q"} {
		if value := v.Get(name); value != "" {
			if len(value) > maxFormText {
				value = value[:maxFormText]
			}
			out = append(out, hiddenField{name, value})
		}
	}
	return append(out, echoEvidence(readEvidenceRows(v))...)
}

func editURL(slug string) string { return "/resolve/" + url.PathEscape(slug) + "/edit" }

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
	req, q, rowsErr := resolveForm(slug, r.PostForm)
	var preview admin.ResolvePreview
	err := rowsErr
	if err == nil {
		preview, err = s.resolver.PreviewResolution(r.Context(), req)
	}

	page := confirmPage{
		Preview: preview, Fields: echoDecision(r.PostForm), Query: q, Back: backURL(slug, q),
		ActionLabel: actionLabels[req.Action], Override: req.OverrideReason, Edit: editURL(slug),
	}
	for _, e := range req.Evidence {
		page.Evidence = append(page.Evidence, evidenceRow{string(e.Type), e.URL, e.Rationale})
		page.Independent = append(page.Independent, e.Independent())
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
	req, q, rowsErr := resolveForm(slug, r.PostForm)
	req.OverrideReason = strings.TrimSpace(r.PostForm.Get("override_reason"))
	req.Confirm = r.PostForm.Get("confirm") == "1"
	if !req.Confirm {
		s.fail(w, r, http.StatusUnprocessableEntity, "confirmation_required", "the change must be confirmed")
		return
	}
	var result admin.ResolveResult
	err := rowsErr
	if err == nil {
		result, err = s.resolver.ApplyResolution(r.Context(), req)
	}
	if s.catalogBusy(w, r, err) {
		return
	}
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

// handleResolveEdit re-renders the review page around a decision in progress:
// the form is server-rendered, so adding a suggestion, adding or removing a row
// and going back from a confirmation all work without JavaScript. Nothing is
// written; the decision still goes through preview and confirmation.
func (s *Server) handleResolveEdit(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	slug := r.PathValue("slug")
	_, q, _ := resolveForm(slug, r.PostForm)
	wb, err := s.resolver.ResolveWorkbench(r.Context(), slug, q)
	if err != nil {
		s.resolveFailed(w, r, err)
		return
	}
	draft := newDraft(r.PostForm)
	var problem string
	switch {
	case r.PostForm.Get("add_suggestion") != "":
		key := strings.TrimSpace(r.PostForm.Get("add_suggestion"))
		found := false
		for _, c := range allCards(wb) {
			if c.SpotifyID != draft.SpotifyID {
				continue
			}
			for _, sg := range suggestionsOf(c) {
				if sg.Key == key {
					found, problem = true, draft.add(sg)
				}
			}
		}
		if !found {
			problem = "That suggestion is no longer offered for this profile, so nothing was added."
		}
	case r.PostForm.Get("add_row") != "":
		problem = draft.addBlank()
	case r.PostForm.Get("remove_row") != "":
		if i, err := strconv.Atoi(r.PostForm.Get("remove_row")); err == nil {
			draft.remove(i)
		}
	}
	page := s.workbenchPage(r, wb, q)
	page.Draft = draft
	page.Reviewer = strings.TrimSpace(r.PostForm.Get("reviewer"))
	if len(page.Reviewer) > 100 {
		page.Reviewer = page.Reviewer[:100]
	}
	title := "Review " + wb.Artist.Name
	if problem == "" {
		s.render(w, r, "resolve", title, page)
		return
	}
	page.Error = problem
	s.renderStatus(w, r, http.StatusUnprocessableEntity, "resolve", title, page)
}

func allCards(wb admin.Workbench) []admin.Card {
	out := make([]admin.Card, 0, len(wb.Attached)+len(wb.Candidates)+len(wb.History))
	out = append(out, wb.Attached...)
	out = append(out, wb.Candidates...)
	return append(out, wb.History...)
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
