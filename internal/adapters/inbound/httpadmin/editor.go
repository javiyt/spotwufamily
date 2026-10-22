package httpadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// Editor is the set of write use cases the adapter consumes. A Service that
// also implements it gets the editing routes; otherwise the admin stays
// read-only.
type Editor interface {
	Editable() bool
	Artist(ctx context.Context, slug string) (admin.ArtistDetail, error)
	Choices(ctx context.Context) ([]admin.Choice, error)
	ApplyChange(ctx context.Context, req admin.ChangeRequest) (admin.ChangeResult, error)
	ApplyRelationship(ctx context.Context, req admin.RelationshipRequest) (admin.RelationshipResult, error)
}

func (s *Server) canEdit() bool { return s.editor != nil && s.editor.Editable() }

// registerEditor adds the record pages. Both /artists and /groups address the
// same backend record; the path only decides which page layout is shown.
func (s *Server) registerEditor() {
	mux := http.NewServeMux()
	for _, kind := range []string{"artists", "groups"} {
		k := kind
		mux.HandleFunc("GET /"+k+"/new", func(w http.ResponseWriter, r *http.Request) { s.handleNew(w, r, k) })
		mux.HandleFunc("GET /"+k+"/{slug}", func(w http.ResponseWriter, r *http.Request) { s.handleRecord(w, r, k) })
		mux.HandleFunc("POST /"+k+"/{slug}", func(w http.ResponseWriter, r *http.Request) { s.handleSave(w, r, k) })
		mux.HandleFunc("POST /"+k+"/{slug}/archive", func(w http.ResponseWriter, r *http.Request) {
			s.handleLifecycle(w, r, k, admin.ChangeArchive)
		})
		mux.HandleFunc("POST /"+k+"/{slug}/restore", func(w http.ResponseWriter, r *http.Request) {
			s.handleLifecycle(w, r, k, admin.ChangeRestore)
		})
		mux.HandleFunc("POST /"+k+"/{slug}/delete", func(w http.ResponseWriter, r *http.Request) { s.handleDelete(w, r, k) })
		mux.HandleFunc("POST /"+k+"/{slug}/relationships", func(w http.ResponseWriter, r *http.Request) {
			s.handleRelationship(w, r, k, admin.RelAdd)
		})
		mux.HandleFunc("POST /"+k+"/{slug}/relationships/update", func(w http.ResponseWriter, r *http.Request) {
			s.handleRelationship(w, r, k, admin.RelUpdate)
		})
		mux.HandleFunc("POST /"+k+"/{slug}/relationships/remove", func(w http.ResponseWriter, r *http.Request) {
			s.handleRelationship(w, r, k, admin.RelRemove)
		})
		route := s.routes["/"+k]
		route.methods = []string{http.MethodGet, http.MethodPost}
		inner := route.handler
		route.handler = func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				s.handleSave(w, r, k)
				return
			}
			inner(w, r)
		}
		s.routes["/"+k] = route
	}
	s.dynamic = mux
}

// --- form model ---

type option struct {
	Value string
	Label string
}

var categoryOptions = []option{
	{string(catalog.CategoryCore), "Core"},
	{string(catalog.CategoryAffiliateGroup), "Affiliate group"},
	{string(catalog.CategoryAffiliateArtist), "Affiliate artist"},
	{string(catalog.CategoryProducer), "Producer"},
	{string(catalog.CategoryCollaborator), "Collaborator"},
}

var (
	relationshipTypes = []option{
		{string(catalog.RelMemberOf), "member of"},
		{string(catalog.RelProducerFor), "producer for"},
		{string(catalog.RelCollectiveOrImprint), "collective or imprint"},
		{string(catalog.RelCollaboratedWith), "collaborated with"},
		{string(catalog.RelAssociatedWith), "associated with"},
	}
	relationshipStatuses = []option{
		{string(catalog.RelationshipProposed), "proposed"},
		{string(catalog.RelationshipVerified), "verified"},
		{string(catalog.RelationshipRejected), "rejected"},
	}
	confidences = []option{{"", "—"}, {"low", "low"}, {"medium", "medium"}, {"high", "high"}}
)

func evidenceOptions() []option {
	opts := []option{{"", "—"}}
	for _, t := range admin.EvidenceTypes() {
		label := strings.ReplaceAll(string(t), "_", " ")
		if t == catalog.EvidenceNameMatch || t == catalog.EvidenceCredit {
			label += " (not independent)"
		}
		opts = append(opts, option{string(t), label})
	}
	return opts
}

type artistFormValues struct {
	Slug, Name, PublicName, Category string
	Aliases, Notes, ExternalURL      string
	Order, Revision                  string
	Roles                            map[string]bool
	Enabled                          bool
	ConfirmSlugChange                bool
}

func formFromArtist(a catalog.Artist, revision string) artistFormValues {
	f := artistFormValues{
		Slug: a.Slug, Name: a.Name, PublicName: a.PublicName, Category: string(a.Category),
		Aliases: strings.Join(a.Aliases, "\n"), Notes: a.Notes, ExternalURL: a.ExternalURL,
		Revision: revision, Roles: map[string]bool{}, Enabled: a.Enabled,
	}
	if a.EditorialOrder > 0 {
		f.Order = strconv.Itoa(a.EditorialOrder)
	}
	for _, r := range a.Roles {
		f.Roles[string(r)] = true
	}
	return f
}

func readArtistForm(v url.Values) artistFormValues {
	f := artistFormValues{
		Slug: strings.TrimSpace(v.Get("slug")), Name: v.Get("name"), PublicName: v.Get("public_name"),
		Category: v.Get("category"), Aliases: v.Get("aliases"), Notes: v.Get("notes"),
		ExternalURL: v.Get("external_url"), Order: strings.TrimSpace(v.Get("editorial_order")),
		Revision: v.Get("revision"), Roles: map[string]bool{}, Enabled: v.Get("enabled") != "",
		ConfirmSlugChange: v.Get("confirm_slug_change") != "",
	}
	for _, r := range v["roles"] {
		f.Roles[r] = true
	}
	return f
}

// input converts the form into the use-case input, reporting unparsable
// values per field. Domain rules are left to the use case.
func (f artistFormValues) input() (admin.ArtistInput, map[string][]string) {
	errs := map[string][]string{}
	in := admin.ArtistInput{
		Slug: f.Slug, Name: f.Name, PublicName: f.PublicName, Category: catalog.Category(f.Category),
		Enabled: f.Enabled, Notes: f.Notes, ExternalURL: f.ExternalURL,
	}
	for _, o := range categoryOptions {
		if f.Roles[o.Value] {
			in.Roles = append(in.Roles, catalog.Category(o.Value))
		}
	}
	for _, line := range strings.Split(f.Aliases, "\n") {
		in.Aliases = append(in.Aliases, strings.TrimSpace(line))
	}
	if f.Order != "" {
		n, err := strconv.Atoi(f.Order)
		if err != nil || n < 0 {
			errs["editorial_order"] = append(errs["editorial_order"], "must be a whole number, 0 or greater")
		}
		in.EditorialOrder = n
	}
	return in, errs
}

// fieldKey maps a domain issue field to the form control that owns it.
func fieldKey(field string) string {
	if i := strings.IndexAny(field, "[."); i >= 0 {
		field = field[:i]
	}
	switch field {
	case "spotify_id":
		return "enabled" // the only artist field that can trip the Spotify ID rule
	case "":
		return "_"
	}
	return field
}

type summaryItem struct {
	Anchor  string
	Message string
}

// issueView splits validation issues into per-field messages and a summary.
func issueView(issues []catalog.ValidationIssue, extra map[string][]string) (map[string][]string, []summaryItem) {
	byField := map[string][]string{}
	var summary []summaryItem
	for key, msgs := range extra {
		for _, m := range msgs {
			byField[key] = append(byField[key], m)
			summary = append(summary, summaryItem{key, strings.ReplaceAll(key, "_", " ") + ": " + m})
		}
	}
	for _, issue := range issues {
		key := fieldKey(issue.Field)
		byField[key] = append(byField[key], issue.Message)
		label := issue.Field
		if label == "" {
			label = "catalog"
		}
		summary = append(summary, summaryItem{key, strings.ReplaceAll(label, "_", " ") + ": " + issue.Message})
	}
	sort.SliceStable(summary, func(i, j int) bool { return summary[i].Message < summary[j].Message })
	return byField, summary
}

// --- page model ---

type relRow struct {
	Type, From, To, Direction string
	OtherSlug, OtherName      string
	Status, Confidence        string
	Start, End, Era           string
	Commentary, Reviewer      string
	ReviewedAt                string
	Evidence                  []evidenceRow
	Revision                  string
	Verified, Unsourced       bool
	Period                    string
	Path                      string
	Edit                      relFields
	Idx                       string
}

type evidenceRow struct{ Type, URL, Rationale string }

// relFields feeds the shared relationship field set, for adding a new edge or
// editing an existing one.
type relFields struct {
	Vals          map[string]string
	Errors        map[string][]string
	RelTypes      []option
	RelStatuses   []option
	Confidences   []option
	EvidenceTypes []option
	Idx           string
}

func (p artistPage) relFields(idx string, vals map[string]string, errs map[string][]string) relFields {
	return relFields{Vals: vals, Errors: errs, RelTypes: p.RelTypes, RelStatuses: p.RelStatuses, Confidences: p.Confidences, EvidenceTypes: p.EvidenceTypes, Idx: idx}
}

func (r relRow) vals() map[string]string {
	return map[string]string{
		"rel_type": r.Type, "direction": r.Direction, "other": r.OtherSlug, "status": r.Status, "confidence": r.Confidence,
		"start": r.Start, "end": r.End, "era": r.Era, "commentary": r.Commentary, "reviewer": r.Reviewer,
	}
}

func relRows(views []admin.RelationshipView, base string) []relRow {
	rows := make([]relRow, 0, len(views))
	for _, v := range views {
		r := v.Relationship
		row := relRow{
			Type: string(r.Type), From: r.From, To: r.To, Direction: v.Direction, OtherSlug: v.OtherSlug, OtherName: v.OtherName,
			Status: string(r.Status), Confidence: string(r.Confidence), Start: r.Start, End: r.End, Era: r.Era,
			Commentary: r.Commentary, Reviewer: r.Reviewer, ReviewedAt: r.ReviewedAt, Revision: v.Revision,
			Verified: r.Status == catalog.RelationshipVerified, Path: base,
		}
		for _, e := range r.Evidence {
			row.Evidence = append(row.Evidence, evidenceRow{string(e.Type), e.URL, e.Rationale})
		}
		row.Unsourced = len(r.Evidence) == 0
		switch {
		case r.Start != "" && r.End != "":
			row.Period = r.Start + " – " + r.End
		case r.Start != "":
			row.Period = "since " + r.Start
		case r.End != "":
			row.Period = "until " + r.End
		}
		if r.Era != "" {
			if row.Period != "" {
				row.Period += " · "
			}
			row.Period += r.Era
		}
		rows = append(rows, row)
	}
	return rows
}

type profileRow struct {
	ID, Status, Reviewer, ReviewedAt string
	Linked                           bool
	Evidence                         []evidenceRow
}

type slugConfirm struct {
	NewSlug string
	Refs    []relRow
}

type artistPage struct {
	Kind, KindLabel string
	BasePath        string
	New             bool
	Editable        bool
	Form            artistFormValues
	Errors          map[string][]string
	Summary         []summaryItem
	Categories      []option
	Detail          admin.ArtistDetail
	Rels            []relRow
	Choices         []admin.Choice
	RelForm         map[string]string
	RelErrors       map[string][]string
	RelSummary      []summaryItem
	RelTypes        []option
	RelStatuses     []option
	Confidences     []option
	EvidenceTypes   []option
	Profiles        []profileRow
	SlugConfirm     *slugConfirm
	Notice          string
	DeleteRefs      []relRow
	CanDelete       bool
	Members         []relRow
	Memberships     []relRow
	Other           []relRow
	IsGroup         bool
	AddRel          relFields
	Resolver        bool
}

func kindLabel(kind string) string {
	if kind == "groups" {
		return "group"
	}
	return "artist"
}

func (s *Server) loadPage(ctx context.Context, kind string, detail admin.ArtistDetail) (artistPage, error) {
	choices, err := s.editor.Choices(ctx)
	if err != nil {
		return artistPage{}, err
	}
	base := "/" + kind + "/" + detail.Artist.Slug
	p := artistPage{
		Kind: kind, KindLabel: kindLabel(kind), BasePath: base, Editable: detail.Editable, Detail: detail,
		Form: formFromArtist(detail.Artist, detail.Revision), Categories: categoryOptions, Choices: choices,
		RelTypes: relationshipTypes, RelStatuses: relationshipStatuses, Confidences: confidences, EvidenceTypes: evidenceOptions(),
		Rels: relRows(detail.Relationships, base), IsGroup: detail.IsGroup, Resolver: s.resolver != nil,
		RelForm: map[string]string{"direction": "outbound", "status": "proposed"},
	}
	for _, r := range p.Rels {
		switch {
		case r.Type == string(catalog.RelMemberOf) && r.Direction == "inbound":
			p.Members = append(p.Members, r)
		case r.Type == string(catalog.RelMemberOf):
			p.Memberships = append(p.Memberships, r)
		default:
			p.Other = append(p.Other, r)
		}
	}
	p.AddRel = p.relFields("add", p.RelForm, nil)
	editable := func(rows []relRow, prefix string) {
		for i := range rows {
			rows[i].Idx = prefix + strconv.Itoa(i)
			rows[i].Edit = p.relFields(rows[i].Idx, rows[i].vals(), nil)
		}
	}
	editable(p.Members, "m")
	editable(p.Memberships, "g")
	editable(p.Other, "o")
	p.DeleteRefs = p.Rels
	p.CanDelete = len(p.Rels) == 0
	for _, prof := range detail.Artist.SpotifyProfiles {
		row := profileRow{ID: prof.ID, Status: string(prof.Status), Reviewer: prof.Reviewer, ReviewedAt: prof.ReviewedAt, Linked: detail.Artist.HasSpotifyID(prof.ID)}
		for _, e := range prof.Evidence {
			row.Evidence = append(row.Evidence, evidenceRow{string(e.Type), e.URL, e.Rationale})
		}
		p.Profiles = append(p.Profiles, row)
	}
	for _, id := range detail.Artist.AllSpotifyIDs() {
		found := false
		for _, prof := range p.Profiles {
			found = found || prof.ID == id
		}
		if !found {
			p.Profiles = append(p.Profiles, profileRow{ID: id, Status: string(catalog.ProfileUnreviewed), Linked: true})
		}
	}
	return p, nil
}

// --- handlers ---

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request, kind string) {
	choices, err := s.editor.Choices(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	category := string(catalog.CategoryCore)
	if kind == "groups" {
		category = string(catalog.CategoryAffiliateGroup)
	}
	s.render(w, r, "artist", "New "+kindLabel(kind), artistPage{
		Kind: kind, KindLabel: kindLabel(kind), New: true, Editable: s.canEdit(), Categories: categoryOptions, Choices: choices,
		Form: artistFormValues{Category: category, Roles: map[string]bool{}}, BasePath: "/" + kind,
	})
}

func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request, kind string) {
	slug := r.PathValue("slug")
	detail, err := s.editor.Artist(r.Context(), slug)
	if errors.Is(err, admin.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not_found", "artist not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// One record, two layouts: send each slug to the page that fits it.
	if canonical := canonicalKind(detail); canonical != kind {
		http.Redirect(w, r, "/"+canonical+"/"+slug, http.StatusSeeOther)
		return
	}
	page, err := s.loadPage(r.Context(), kind, detail)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+detail.Revision+`"`)
	s.render(w, r, "artist", detail.Artist.Name, page)
}

func canonicalKind(d admin.ArtistDetail) string {
	if d.IsGroup {
		return "groups"
	}
	return "artists"
}

// postForm parses a mutating request and checks its CSRF token.
func (s *Server) postForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		s.formError(w, r, err)
		return false
	}
	if !s.sessions.checkCSRF(s.sessionID(r), r.PostForm.Get("csrf_token")) {
		s.fail(w, r, http.StatusForbidden, "invalid_csrf", "invalid or missing CSRF token")
		return false
	}
	if !s.canEdit() {
		s.fail(w, r, http.StatusForbidden, "read_only", "the catalog is read-only")
		return false
	}
	return true
}

func isFetch(r *http.Request) bool { return r.Header.Get("X-Admin-Fetch") == "1" }

// done confirms a successful write. A fetch client gets the server's own
// confirmation as JSON; a plain form gets a redirect and a one-shot toast.
func (s *Server) done(w http.ResponseWriter, r *http.Request, message, location string) {
	if isFetch(r) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "redirect": location})
		return
	}
	s.sessions.setFlash(s.sessionID(r), flash{Message: message})
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// refreshHint is the fixed post-edit hint; the layout renders the same
// sentence with a link to refreshHintLink.
const (
	refreshHint     = "Catalog changed — the database, snapshot and exports may need refreshing. Review in Operations."
	refreshHintLink = "/operations"
)

// catalogChanged confirms a successful catalog write (an edit or a Spotify
// profile decision). It adds the fixed refresh hint and marks the cached
// freshness check as outdated; it never computes freshness and never starts
// anything.
func (s *Server) catalogChanged(w http.ResponseWriter, r *http.Request, message, location string) {
	s.fresh.markStale()
	s.pendCache.invalidate()
	if isFetch(r) {
		// The script follows the redirect, so the page it lands on keeps the
		// hint after the toast is gone.
		s.sessions.setFlash(s.sessionID(r), flash{Refresh: true})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "redirect": location,
			"hint": refreshHint, "hint_link": refreshHintLink})
		return
	}
	s.sessions.setFlash(s.sessionID(r), flash{Message: message, Refresh: true})
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// saveFailed reports a storage failure without leaking paths.
// catalogBusyMessage answers a catalog write refused because a maintenance
// job holds the catalog.
const catalogBusyMessage = "A maintenance job is updating the catalog; try again when it finishes."

// catalogBusy answers 409 when err says a maintenance job holds the catalog
// (the write was refused before anything was written).
func (s *Server) catalogBusy(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, jobs.ErrConflict) {
		return false
	}
	s.fail(w, r, http.StatusConflict, "job_running", catalogBusyMessage)
	return true
}

func (s *Server) saveFailed(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Printf("save failed on %s %s: %v", r.Method, r.URL.Path, err)
	s.fail(w, r, http.StatusInternalServerError, "save_failed", "could not write the catalog; nothing was changed")
}

// handleSave serves both create (POST /artists) and update (POST /artists/{slug}).
func (s *Server) handleSave(w http.ResponseWriter, r *http.Request, kind string) {
	if !s.postForm(w, r) {
		return
	}
	slug := r.PathValue("slug")
	creating := slug == ""
	form := readArtistForm(r.PostForm)
	in, formErrs := form.input()
	intent := r.PostForm.Get("intent")

	req := admin.ChangeRequest{Kind: admin.ChangeUpdate, Slug: slug, Revision: form.Revision, Input: in, ConfirmSlugChange: form.ConfirmSlugChange}
	if creating {
		req.Kind = admin.ChangeCreate
	}

	page := artistPage{
		Kind: kind, KindLabel: kindLabel(kind), New: creating, Editable: true, Form: form, Categories: categoryOptions,
		BasePath: "/" + kind + "/" + slug,
	}
	if creating {
		page.BasePath = "/" + kind
	} else {
		detail, err := s.editor.Artist(r.Context(), slug)
		if errors.Is(err, admin.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "not_found", "artist not found")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if page, err = s.loadPage(r.Context(), kind, detail); err != nil {
			s.internal(w, r, err)
			return
		}
		page.Form = form
	}
	if choices, err := s.editor.Choices(r.Context()); err == nil {
		page.Choices = choices
	}
	title := page.Form.Name
	if title == "" {
		title = "New " + page.KindLabel
	}

	if intent == "edit" {
		s.render(w, r, "artist", title, page)
		return
	}
	if len(formErrs) > 0 {
		page.Errors, page.Summary = issueView(nil, formErrs)
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "artist", title, page)
		return
	}

	// A preview is the dry run of the same use case, so what it shows is
	// exactly what a save would validate and write.
	if intent == "preview" {
		preview := req
		preview.DryRun, preview.ConfirmSlugChange = true, true
		result, err := s.editor.ApplyChange(r.Context(), preview)
		if s.changeFailed(w, r, err, &page, title, req) {
			return
		}
		pv := previewPage{
			Heading: "Review changes", Action: r.URL.Path, Fields: echo(r.PostForm), Changes: result.Changes,
			Warnings: result.Warnings, KeepEditing: true, Cancel: page.BasePath, Slug: result.Slug,
		}
		if !creating && form.Slug != "" && form.Slug != slug {
			pv.SlugConfirm = &slugConfirm{NewSlug: form.Slug, Refs: relRows(result.References, page.BasePath)}
		}
		s.render(w, r, "preview", "Review changes", pv)
		return
	}

	result, err := s.editor.ApplyChange(r.Context(), req)
	if s.changeFailed(w, r, err, &page, title, req) {
		return
	}
	verb := "Saved"
	if creating {
		verb = "Created"
	}
	s.catalogChanged(w, r, fmt.Sprintf("%s %s.", verb, form.nameOr(result.Slug)), "/"+kind+"/"+result.Slug)
}

func (f artistFormValues) nameOr(slug string) string {
	if n := strings.TrimSpace(f.Name); n != "" {
		return n
	}
	return slug
}

// changeFailed renders the right response for a failed change and reports
// whether it did.
func (s *Server) changeFailed(w http.ResponseWriter, r *http.Request, err error, page *artistPage, title string, req admin.ChangeRequest) bool {
	if err == nil {
		return false
	}
	if s.catalogBusy(w, r, err) {
		return true
	}
	var (
		invalid    *admin.ValidationError
		conflict   *admin.ConflictError
		confirm    *admin.ConfirmationError
		referenced *admin.ReferencedError
	)
	switch {
	case errors.As(err, &invalid):
		page.Errors, page.Summary = issueView(invalid.Issues, nil)
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "artist", title, *page)
	case errors.As(err, &conflict):
		s.renderStatus(w, r, http.StatusConflict, "conflict", "Edit conflict", s.conflictPage(*page, conflict, req))
	case errors.As(err, &confirm):
		if confirm.Action == "delete" {
			page.Errors, page.Summary = issueView(nil, map[string][]string{"confirm_slug": {"type the slug to confirm the delete"}})
		} else {
			page.SlugConfirm = &slugConfirm{NewSlug: req.Input.Slug, Refs: relRows(confirm.References, page.BasePath)}
			page.Errors, page.Summary = issueView(nil, map[string][]string{"slug": {"changing the slug needs explicit confirmation"}})
		}
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "artist", title, *page)
	case errors.As(err, &referenced):
		page.Notice = fmt.Sprintf("%s is referenced by %d relationship(s), so it cannot be deleted. Archive it instead, or remove the relationships first.", referenced.Slug, len(referenced.References))
		page.DeleteRefs = relRows(referenced.References, page.BasePath)
		s.renderStatus(w, r, http.StatusConflict, "artist", title, *page)
	case errors.Is(err, admin.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "artist not found")
	case errors.Is(err, admin.ErrReadOnly):
		s.fail(w, r, http.StatusForbidden, "read_only", "the catalog is read-only")
	default:
		s.saveFailed(w, r, err)
	}
	return true
}

type conflictRow struct{ Field, Mine, Theirs, Before string }

type conflictPage struct {
	Slug      string
	Back      string
	Revision  string
	BaseKnown bool
	Changed   []admin.FieldChange
	Fields    []hiddenField
	Mine      []conflictRow
	Message   string
}

func (s *Server) conflictPage(page artistPage, c *admin.ConflictError, req admin.ChangeRequest) conflictPage {
	cp := conflictPage{Slug: c.Slug, Back: page.BasePath, Revision: c.CurrentRevision, BaseKnown: c.BaseKnown, Changed: c.Changed, Message: c.Error()}
	in := req.Input
	cp.Mine = []conflictRow{
		{Field: "name", Mine: in.Name}, {Field: "public_name", Mine: in.PublicName}, {Field: "category", Mine: string(in.Category)},
		{Field: "aliases", Mine: strings.Join(in.Aliases, " | ")}, {Field: "enabled", Mine: strconv.FormatBool(in.Enabled)},
		{Field: "editorial_order", Mine: strconv.Itoa(in.EditorialOrder)}, {Field: "notes", Mine: in.Notes}, {Field: "external_url", Mine: in.ExternalURL},
	}
	return cp
}

func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request, kind string, change admin.ChangeKind) {
	if !s.postForm(w, r) {
		return
	}
	slug := r.PathValue("slug")
	req := admin.ChangeRequest{Kind: change, Slug: slug, Revision: r.PostForm.Get("revision")}
	page, ok := s.pageFor(w, r, kind, slug)
	if !ok {
		return
	}
	result, err := s.editor.ApplyChange(r.Context(), req)
	if s.changeFailed(w, r, err, &page, page.Detail.Artist.Name, req) {
		return
	}
	verb := map[admin.ChangeKind]string{admin.ChangeArchive: "Archived", admin.ChangeRestore: "Restored"}[change]
	s.catalogChanged(w, r, fmt.Sprintf("%s %s.", verb, page.Detail.Artist.Name), "/"+kind+"/"+result.Slug)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, kind string) {
	if !s.postForm(w, r) {
		return
	}
	slug := r.PathValue("slug")
	page, ok := s.pageFor(w, r, kind, slug)
	if !ok {
		return
	}
	req := admin.ChangeRequest{
		Kind: admin.ChangeDelete, Slug: slug, Revision: r.PostForm.Get("revision"),
		ConfirmDelete: strings.TrimSpace(r.PostForm.Get("confirm_slug")) == slug,
	}
	_, err := s.editor.ApplyChange(r.Context(), req)
	if s.changeFailed(w, r, err, &page, page.Detail.Artist.Name, req) {
		return
	}
	s.catalogChanged(w, r, fmt.Sprintf("Deleted %s. Synced Spotify data was not touched.", page.Detail.Artist.Name), "/"+kind)
}

// pageFor loads the record page model, answering the request itself on error.
func (s *Server) pageFor(w http.ResponseWriter, r *http.Request, kind, slug string) (artistPage, bool) {
	detail, err := s.editor.Artist(r.Context(), slug)
	if errors.Is(err, admin.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not_found", "artist not found")
		return artistPage{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return artistPage{}, false
	}
	page, err := s.loadPage(r.Context(), kind, detail)
	if err != nil {
		s.internal(w, r, err)
		return artistPage{}, false
	}
	return page, true
}

func (s *Server) handleRelationship(w http.ResponseWriter, r *http.Request, kind string, change admin.RelationshipKind) {
	if !s.postForm(w, r) {
		return
	}
	slug := r.PathValue("slug")
	page, ok := s.pageFor(w, r, kind, slug)
	if !ok {
		return
	}
	v := r.PostForm
	req := admin.RelationshipRequest{Kind: change, Revision: v.Get("rel_revision")}
	if change != admin.RelAdd {
		keyType := v.Get("rel_key_type")
		if keyType == "" {
			keyType = v.Get("rel_type")
		}
		req.Key = catalog.NewRelationshipKey(catalog.RelationshipType(keyType), v.Get("rel_from"), v.Get("rel_to"))
	}
	if change != admin.RelRemove {
		req.Input = relationshipInput(v, slug)
	}

	intent := v.Get("intent")
	if intent == "preview" {
		req.DryRun = true
	}
	result, err := s.editor.ApplyRelationship(r.Context(), req)
	if err != nil {
		s.relationshipFailed(w, r, err, page, v)
		return
	}
	if intent == "preview" {
		s.render(w, r, "preview", "Review changes", previewPage{
			Heading: "Review relationship change", Action: r.URL.Path, Fields: echo(v), Changes: result.Changes, Cancel: page.BasePath,
		})
		return
	}
	message := map[admin.RelationshipKind]string{admin.RelAdd: "Added", admin.RelUpdate: "Updated", admin.RelRemove: "Removed"}[change]
	s.catalogChanged(w, r, fmt.Sprintf("%s relationship %s.", message, result.Key), "/"+kind+"/"+slug)
}

// relationshipInput reads the add/update form. "direction" says which side of
// the edge the page's artist is on; undirected types are normalised later.
func relationshipInput(v url.Values, slug string) admin.RelationshipInput {
	in := admin.RelationshipInput{
		Type: catalog.RelationshipType(v.Get("rel_type")), Status: catalog.RelationshipStatus(v.Get("status")),
		Confidence: catalog.CandidateConfidence(v.Get("confidence")), Start: v.Get("start"), End: v.Get("end"), Era: v.Get("era"),
		Commentary: v.Get("commentary"), Reviewer: v.Get("reviewer"),
		EvidenceType: catalog.EvidenceType(v.Get("evidence_type")), EvidenceURL: v.Get("evidence_url"), EvidenceRationale: v.Get("evidence_rationale"),
	}
	other := strings.TrimSpace(v.Get("other"))
	if v.Get("direction") == "inbound" {
		in.From, in.To = other, slug
	} else {
		in.From, in.To = slug, other
	}
	return in
}

func (s *Server) relationshipFailed(w http.ResponseWriter, r *http.Request, err error, page artistPage, v url.Values) {
	if s.catalogBusy(w, r, err) {
		return
	}
	var (
		invalid  *admin.ValidationError
		conflict *admin.ConflictError
	)
	switch {
	case errors.As(err, &invalid):
		page.RelErrors, page.RelSummary = issueView(invalid.Issues, nil)
		vals := map[string]string{}
		for key := range v {
			vals[key] = v.Get(key)
		}
		page.RelForm = vals
		page.AddRel = page.relFields("add", vals, page.RelErrors)
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "artist", page.Detail.Artist.Name, page)
	case errors.As(err, &conflict):
		s.renderStatus(w, r, http.StatusConflict, "conflict", "Edit conflict", conflictPage{
			Slug: conflict.Slug, Back: page.BasePath, Message: "This relationship changed since you opened the page. Reload and try again.", Revision: conflict.CurrentRevision,
		})
	case errors.Is(err, admin.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "relationship not found")
	default:
		s.saveFailed(w, r, err)
	}
}

// --- preview ---

type hiddenField struct{ Name, Value string }

type previewPage struct {
	Heading     string
	Action      string
	Fields      []hiddenField
	Changes     []admin.FieldChange
	Warnings    []string
	KeepEditing bool
	Cancel      string
	Slug        string
	SlugConfirm *slugConfirm
}

// echo carries a submitted form into the confirmation form unchanged, minus
// the per-page tokens and the intent that the page sets itself.
func echo(v url.Values) []hiddenField {
	keys := make([]string, 0, len(v))
	for k := range v {
		if k != "csrf_token" && k != "intent" && k != "confirm_slug_change" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var fields []hiddenField
	for _, k := range keys {
		for _, val := range v[k] {
			fields = append(fields, hiddenField{k, val})
		}
	}
	return fields
}

// --- artist list view ---

type sortCol struct {
	URL  string
	Aria string
}

type artistsPage struct {
	Page     admin.Page[admin.ArtistRow]
	Pager    pager
	Query    url.Values
	Filters  filterState
	Sorts    map[string]sortCol
	Editable bool
	Reset    string
	Options  filterOptions
}

type filterState struct {
	Search, Category, Enabled, IDs, Rel, Sort, Dir string
	Archived                                       bool
}

type filterOptions struct {
	Categories []option
	Enabled    []option
	IDs        []option
	Relations  []option
}

func (s *Server) artistsView(r *http.Request, page admin.Page[admin.ArtistRow]) artistsPage {
	query := r.URL.Query()
	f := filterState{
		Search: page.Search, Category: query.Get("category"), Enabled: query.Get("enabled"), IDs: query.Get("ids"),
		Rel: query.Get("rel"), Sort: query.Get("sort"), Dir: query.Get("dir"), Archived: query.Get("archived") == "1",
	}
	sorts := map[string]sortCol{}
	for _, col := range []string{"name", "slug", "category", "ids", "order"} {
		current := f.Sort
		if current == "" {
			current = "name"
		}
		next := url.Values{}
		for k, v := range query {
			if k != "page" {
				next[k] = v
			}
		}
		next.Set("sort", col)
		sc := sortCol{}
		if current == col {
			if f.Dir == "desc" {
				next.Del("dir")
				sc.Aria = "descending"
			} else {
				next.Set("dir", "desc")
				sc.Aria = "ascending"
			}
		} else {
			next.Del("dir")
		}
		sc.URL = "/artists?" + next.Encode()
		sorts[col] = sc
	}
	return artistsPage{
		Page: page, Pager: newPager("/artists", query, page.Page, page.Pages, page.Total, page.PageSize, page.Search),
		Query: query, Filters: f, Sorts: sorts, Editable: s.canEdit(), Reset: "/artists",
		Options: filterOptions{
			Categories: append([]option{{"", "All categories"}}, categoryOptions...),
			Enabled:    []option{{"", "Enabled or not"}, {"yes", "Enabled"}, {"no", "Disabled"}},
			IDs:        []option{{"", "Any Spotify state"}, {"verified", "Verified"}, {"pending", "Pending review"}, {"unresolved", "Unresolved"}},
			Relations:  append([]option{{"", "Any relationship"}}, relationshipTypes...),
		},
	}
}
