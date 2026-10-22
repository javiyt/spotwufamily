package httpadmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Operator is the set of maintenance-action use cases the adapter consumes. A
// Service that also implements it gets the operations dashboard, the
// preview/confirm pages and their API.
type Operator interface {
	// Actions lists every registered action and whether it can run now. It
	// must return promptly: costly checks report a cached or "checking" state.
	Actions() []maintenance.ActionInfo
	// Action describes one action, computing that action's availability
	// only; unknown names report false.
	Action(name string) (maintenance.ActionInfo, bool)
	// PreviewAction validates params and returns what a run would do, plus
	// the opaque token that authorises running exactly that preview.
	PreviewAction(ctx context.Context, name string, p maintenance.Params) (maintenance.Preview, maintenance.Token, error)
	// RunAction re-checks the preview against the token and the typed
	// confirmation and submits the job. It returns *jobs.ConflictError,
	// maintenance.ErrStalePreview, ErrConfirmationRequired, ErrUnavailable
	// or safe errors.
	RunAction(ctx context.Context, name string, p maintenance.Params, tok maintenance.Token, typed string) (jobs.Job, error)
	// Freshness is costly (seconds); the adapter only calls it on request.
	Freshness(ctx context.Context) (maintenance.Freshness, error)
	// RecentOperations returns up to n operation log entries, newest first.
	RecentOperations(ctx context.Context, n int) ([]maintenance.OperationEntry, error)
	// ResumeEligible reports whether job id exists, was run by action and
	// may be resumed or retried.
	ResumeEligible(id, action string) bool
}

const (
	// freshnessTTL is how long a freshness check is reused.
	freshnessTTL = 60 * time.Second
	// recentOperations is how many log entries the dashboard shows.
	recentOperations = 10
)

// reservedFields are form fields that are never action parameters.
var reservedFields = map[string]bool{"csrf_token": true, "token": true, "typed": true, guidedField: true}

func (s *Server) registerOperator() {
	s.routes["/api/v1/operations"] = route{methods: []string{http.MethodGet}, handler: s.apiOperations}
	s.routes["/api/v1/freshness"] = route{methods: []string{http.MethodGet}, handler: s.apiFreshness}
	if s.dynamic == nil {
		s.dynamic = http.NewServeMux()
	}
	s.dynamic.HandleFunc("POST /operations/freshness", s.handleFreshnessCheck)
	s.dynamic.HandleFunc("GET /operations/{action}", s.handleOperationForm)
	s.dynamic.HandleFunc("POST /operations/{action}/preview", s.handleOperationPreview)
	s.dynamic.HandleFunc("POST /operations/{action}/run", s.handleOperationRun)
	s.registerPublish()
}

// --- static per-action UI metadata ---

type fieldSpec struct {
	Name, Label, Type, Help string // Type: select, text or checkbox
	Options                 []option
	Required                bool
}

type actionSpec struct {
	Group       string
	Description string
	Fields      []fieldSpec
	// Guided names the page that owns this action's flow (the release
	// actions live on /publish). Its dashboard card links there, the generic
	// form redirects there, and the generic preview and run accept only
	// requests carrying that page's hidden from= field (see guidedRedirect).
	Guided string
}

// guidedField is the hidden form field a guided page's forms carry.
const guidedField = "from"

// guidedFrom is the from= value the forms of page emit ("/publish" -> "publish").
func guidedFrom(page string) string { return strings.TrimPrefix(page, "/") }

var actionGroups = []string{"Discover", "Audit", "Sync", "Refresh/Export", "Reconcile", "Release", "Other"}

var (
	syncScopeOptions = []option{
		{"", "Choose…"}, {"artist", "One artist"}, {"selected", "Selected artists"}, {"all", "All enabled artists"},
	}
	syncDryRunFields = []fieldSpec{
		{Name: "scope", Label: "Scope", Type: "select", Required: true, Options: syncScopeOptions},
		{Name: "artists", Label: "Artist slugs", Type: "text", Help: "Comma-separated catalog slugs; leave empty for all."},
		{Name: "resume", Label: "Resume the last partial run", Type: "checkbox", Help: "Skips artists the interrupted run already completed."},
	}
	syncFields = append([]fieldSpec{{Name: "scope", Label: "Scope", Type: "select", Required: true, Options: syncScopeOptions,
		Help: "Syncing every artist, or more than five, needs a typed confirmation."}}, syncDryRunFields[1:]...)
	reconcileFields = []fieldSpec{
		{Name: "artist", Label: "Artist slug", Type: "text", Required: true, Help: "The catalog slug of the artist to reconcile."},
	}
	refreshFields = []fieldSpec{
		{Name: "force", Label: "Ignore the refresh TTL", Type: "checkbox", Help: "Fetches every artist again, even recently refreshed ones."},
	}
)

var actionSpecs = map[string]actionSpec{
	"discover-preview": {Group: "Discover", Description: "List Wu-Tang affiliates from Wikipedia that are not in the catalog yet. Writes nothing."},
	"discover-import":  {Group: "Discover", Description: "Add the discovered candidates to the catalog as disabled artists for review."},
	"audit-profiles": {Group: "Audit", Description: "Check Spotify profile mappings against the evidence and report suspicious ones.", Fields: []fieldSpec{
		{Name: "artist", Label: "Artist slug", Type: "text", Help: "Optional: audit one artist instead of the whole catalog."},
		{Name: "offline", Label: "Offline (no Spotify calls)", Type: "checkbox", Help: "Uses cached data only."},
	}},
	"audit":                    {Group: "Audit", Description: "Check the snapshot and the generated export for consistency."},
	"sync-dry-run":             {Group: "Sync", Description: "Plan a sync from the catalog without contacting Spotify or writing anything.", Fields: syncDryRunFields},
	"sync":                     {Group: "Sync", Description: "Sync artists from Spotify into the database. Backs up the database first.", Fields: syncFields},
	"refresh-metadata-preview": {Group: "Refresh/Export", Description: "Show which artists' metadata a refresh would fetch. Writes nothing.", Fields: refreshFields},
	"refresh-metadata":         {Group: "Refresh/Export", Description: "Fetch artist metadata from Spotify into the catalog file. It does not update the database: run a sync afterwards.", Fields: refreshFields},
	"db-verify":                {Group: "Refresh/Export", Description: "Verify the database integrity."},
	"db-snapshot":              {Group: "Refresh/Export", Description: "Write the database snapshot (the previous one is kept)."},
	"export":                   {Group: "Refresh/Export", Description: "Regenerate the static JSON the site reads."},
	"refresh-export":           {Group: "Refresh/Export", Description: "Verify, snapshot, export and audit in one job."},
	"reconcile-dry-run":        {Group: "Reconcile", Description: "Show the association changes reconciling one artist would make.", Fields: reconcileFields},
	"reconcile-apply":          {Group: "Reconcile", Description: "Apply one artist's pending association changes to the database.", Fields: reconcileFields},
	"reconcile-all-dry-run":    {Group: "Reconcile", Description: "Show the association changes reconciling every artist would make. Writes nothing."},
	"reconcile-all-apply":      {Group: "Reconcile", Description: "Apply every artist's pending association changes in one step: one backup first, and any failure rolls every artist back."},
	"release-prepare":          {Group: "Release", Description: "Build the catalog release package.", Guided: "/publish"},
	"release-publish":          {Group: "Release", Description: "Publish the prepared release to GitHub.", Guided: "/publish"},
	"release-pr":               {Group: "Release", Description: "Commit the catalog, its review log and the release manifest to a new branch from origin and open a pull request. Your checkout is not changed; nothing is merged."},
}

func specFor(name string) actionSpec {
	if sp, ok := actionSpecs[name]; ok {
		return sp
	}
	return actionSpec{Group: "Other"}
}

// lookupAction validates the path name and finds the action, answering 404
// otherwise. It never computes another action's availability.
func (s *Server) lookupAction(w http.ResponseWriter, r *http.Request) (maintenance.ActionInfo, bool) {
	name := r.PathValue("action")
	if actionPattern.MatchString(name) {
		if a, ok := s.op.Action(name); ok && a.Name == name {
			return a, true
		}
	}
	s.fail(w, r, http.StatusNotFound, "not_found", "action not found")
	return maintenance.ActionInfo{}, false
}

// guidedRedirect keeps guided actions on their own page: the generic form
// always redirects there, and a preview or run without the page's from=
// field is sent back to it (303) before the action is even looked up. The
// field is user-tamperable on purpose. It is not a security boundary (the
// session, CSRF and same-origin checks, the preview token, the typed phrase
// and the service's fresh prerequisite re-checks are); it only keeps the
// explicit guided checklist the one way to reach these actions in the UI.
// It reports whether it answered the request.
func (s *Server) guidedRedirect(w http.ResponseWriter, r *http.Request, post bool) bool {
	sp, ok := actionSpecs[r.PathValue("action")]
	if !ok || sp.Guided == "" {
		return false
	}
	if post && r.PostForm.Get(guidedField) == guidedFrom(sp.Guided) {
		return false
	}
	http.Redirect(w, r, sp.Guided, http.StatusSeeOther)
	return true
}

// flowLinks returns the hidden from= value and the "back" page of an
// action's preview and form pages.
func flowLinks(name string) (from, back string) {
	if sp := specFor(name); sp.Guided != "" {
		return guidedFrom(sp.Guided), sp.Guided
	}
	return "", "/operations/" + name
}

func unavailableReason(a maintenance.ActionInfo) string {
	if a.UnavailableReason != "" {
		return a.UnavailableReason
	}
	return "This action is not available right now."
}

// --- dashboard ---

type actionCard struct {
	maintenance.ActionInfo
	Description, Page string
}

type cardGroup struct {
	Name  string
	Cards []actionCard
}

type freshView struct {
	Checked bool
	At      string
	Age     string
	// Stale means the catalog was written after this check.
	Stale bool
	maintenance.Freshness
}

type opEntryRow struct {
	At, Action, JobID, ShortJobID, Event, Outcome, Summary string
}

type operationsPage struct {
	dashboardViewModel
	Operator  bool
	Groups    []cardGroup
	Fresh     freshView
	Recent    []opEntryRow
	RecentErr string
}

func actionPage(a maintenance.ActionInfo) string {
	if sp := specFor(a.Name); sp.Guided != "" {
		return sp.Guided
	}
	return "/operations/" + a.Name
}

func (s *Server) actionCards() []cardGroup {
	byGroup := map[string][]actionCard{}
	for _, a := range s.op.Actions() {
		if !actionPattern.MatchString(a.Name) {
			continue
		}
		sp := specFor(a.Name)
		byGroup[sp.Group] = append(byGroup[sp.Group], actionCard{ActionInfo: a, Description: sp.Description, Page: actionPage(a)})
	}
	var out []cardGroup
	for _, g := range actionGroups {
		if cards := byGroup[g]; len(cards) > 0 {
			out = append(out, cardGroup{Name: g, Cards: cards})
		}
	}
	return out
}

// recentLogError turns a RecentOperations error into the notice shown in
// place of the log. A canceled request (the client left) is neither logged nor
// shown; a real failure is logged and tells the operator to retry.
func (s *Server) recentLogError(err error) string {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return ""
	default:
		s.logger.Printf("operations log: %v", err)
		return "The operations log could not be read. Reload the page to retry."
	}
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	d, err := s.svc.Dashboard(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	page := operationsPage{dashboardViewModel: dashboardView(d)}
	if s.op != nil {
		page.Operator = true
		page.Groups = s.actionCards()
		page.Fresh = s.freshView()
		entries, err := s.op.RecentOperations(r.Context(), recentOperations)
		page.RecentErr = s.recentLogError(err)
		for i, e := range entries {
			if i == recentOperations {
				break
			}
			row := opEntryRow{At: rfc3339(e.At), Action: e.Action, Event: e.Kind, Outcome: e.Outcome, Summary: e.Summary}
			if jobIDPattern.MatchString(e.JobID) {
				row.JobID, row.ShortJobID = e.JobID, shortID(e.JobID)
			}
			page.Recent = append(page.Recent, row)
		}
	}
	s.render(w, r, "operations", "Operations", page)
}

// --- freshness ---

// freshnessCache keeps the last freshness check. run serialises checks so
// concurrent clicks share one computation; mu guards the value so reads never
// wait for a running check.
type freshnessCache struct {
	run sync.Mutex
	mu  sync.Mutex
	val maintenance.Freshness
	at  time.Time
	ok  bool
	// stale is set by a catalog write after the check; the next check
	// recomputes regardless of its age. writes counts catalog writes so a
	// write during a running check keeps its result stale.
	stale  bool
	writes uint64
}

func (c *freshnessCache) get() (maintenance.Freshness, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, c.at, c.ok
}

func (c *freshnessCache) isStale() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ok && c.stale
}

func (c *freshnessCache) writeCount() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

// set stores a check that started after writes catalog writes.
func (c *freshnessCache) set(f maintenance.Freshness, at time.Time, writes uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.val, c.at, c.ok, c.stale = f, at, true, c.writes != writes
}

// markStale records that the catalog changed after the last check. It is
// cheap and computes nothing.
func (c *freshnessCache) markStale() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	c.stale = c.ok
}

// checkFreshness computes freshness unless a check younger than freshnessTTL
// exists; a click that waited for a running check reuses its result.
func (s *Server) checkFreshness(ctx context.Context) error {
	s.fresh.run.Lock()
	defer s.fresh.run.Unlock()
	if _, at, ok := s.fresh.get(); ok && !s.fresh.isStale() && s.now().Sub(at) < freshnessTTL {
		return nil
	}
	writes := s.fresh.writeCount()
	f, err := s.op.Freshness(ctx)
	if err != nil {
		return err
	}
	s.fresh.set(f, s.now(), writes)
	return nil
}

func (s *Server) freshView() freshView {
	f, at, ok := s.fresh.get()
	if !ok {
		return freshView{}
	}
	age := s.now().Sub(at).Round(time.Second)
	if age < 0 {
		age = 0
	}
	return freshView{Checked: true, At: rfc3339(at), Age: age.String(), Stale: s.fresh.isStale(), Freshness: f}
}

func (s *Server) handleFreshnessCheck(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	back := "/operations#freshness"
	if r.PostForm.Get(guidedField) == guidedFrom(publishPath) {
		back = publishPath + "#readiness"
	}
	if err := s.checkFreshness(r.Context()); err != nil {
		s.logger.Printf("freshness check failed: %v", err)
		s.refused(w, r, "freshness_unavailable", "Data freshness could not be checked.", back)
		return
	}
	s.done(w, r, "Data freshness checked.", back)
}

func (s *Server) apiFreshness(w http.ResponseWriter, _ *http.Request) {
	f, at, ok := s.fresh.get()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"checked": false})
		return
	}
	age := max(s.now().Sub(at), 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"checked": true, "checked_at": rfc3339(at), "age_seconds": int(age.Seconds()), "stale": s.fresh.isStale(),
		"seed_needed": f.SeedNeeded, "seed_unknown": f.SeedUnknown, "pending_changes": len(f.Pending), "snapshot_needed": f.SnapshotNeeded, "export_needed": f.ExportNeeded,
		"rebuild_needed": f.RebuildNeeded, "differs_from_published": f.DiffersFromPublished,
		"offline": f.Offline, "unreadable": f.Unreadable, "reasons": nonNilStrings(append([]string(nil), f.Reasons...)),
	})
}

func (s *Server) apiOperations(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		Name              string            `json:"name"`
		Title             string            `json:"title"`
		Group             string            `json:"group"`
		Description       string            `json:"description"`
		Available         bool              `json:"available"`
		UnavailableReason string            `json:"unavailable_reason,omitempty"`
		UsesNetwork       bool              `json:"uses_network"`
		Destructive       bool              `json:"destructive"`
		Links             map[string]string `json:"links"`
	}
	items := []item{}
	for _, a := range s.op.Actions() {
		if !actionPattern.MatchString(a.Name) {
			continue
		}
		sp := specFor(a.Name)
		it := item{Name: a.Name, Title: a.Title, Group: sp.Group, Description: sp.Description, Available: a.Available,
			UsesNetwork: a.UsesNetwork, Destructive: a.Destructive, Links: map[string]string{"page": actionPage(a)}}
		if !a.Available {
			it.UnavailableReason = unavailableReason(a)
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- parameter form ---

type fieldView struct {
	fieldSpec
	ID, Value string
	Checked   bool
}

type operationForm struct {
	Action      maintenance.ActionInfo
	Description string
	Unavailable string
	Fields      []fieldView
	Error       string
	ResumeOf    string
	ResumeShort string
	ResumeField bool
	ResumeNote  string
	// From and Back keep a guided action on its page.
	From, Back string
}

func buildFields(name string, values maintenance.Params) []fieldView {
	specs := specFor(name).Fields
	out := make([]fieldView, 0, len(specs))
	for _, f := range specs {
		v := fieldView{fieldSpec: f, ID: "f-" + f.Name, Value: values[f.Name]}
		if f.Type == "checkbox" {
			v.Checked, v.Value = values[f.Name] == "true", "true"
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, status int, a maintenance.ActionInfo, values maintenance.Params, message string) {
	page := operationForm{Action: a, Description: specFor(a.Name).Description, Fields: buildFields(a.Name, values), Error: message}
	page.From, page.Back = flowLinks(a.Name)
	if !a.Available {
		page.Unavailable = unavailableReason(a)
	}
	s.renderStatus(w, r, status, "operation", a.Title, page)
}

func (s *Server) handleOperationForm(w http.ResponseWriter, r *http.Request) {
	if s.guidedRedirect(w, r, false) {
		return
	}
	a, ok := s.lookupAction(w, r)
	if !ok {
		return
	}
	page := operationForm{Action: a, Description: specFor(a.Name).Description}
	page.From, page.Back = flowLinks(a.Name)
	if !a.Available {
		page.Unavailable = unavailableReason(a)
		s.render(w, r, "operation", a.Title, page)
		return
	}
	values := maintenance.Params{}
	if id := r.URL.Query().Get("resume_of"); id != "" {
		if jobIDPattern.MatchString(id) && s.op.ResumeEligible(id, a.Name) {
			page.ResumeOf, page.ResumeShort = id, shortID(id)
			for _, f := range specFor(a.Name).Fields {
				if f.Name == "resume" {
					values["resume"], page.ResumeField = "true", true
				}
			}
		} else {
			page.ResumeNote = "That job cannot be resumed from here; you can start a new run below."
		}
	}
	page.Fields = buildFields(a.Name, values)
	s.render(w, r, "operation", a.Title, page)
}

// --- preview and run ---

// formParams passes every non-reserved form field through untouched (first
// value only); the service validates names and values.
func formParams(v url.Values) maintenance.Params {
	p := maintenance.Params{}
	for k, vals := range v {
		if reservedFields[k] || len(vals) == 0 {
			continue
		}
		p[k] = vals[0]
	}
	return p
}

// echoParams lists params as hidden fields in a stable order.
func echoParams(p maintenance.Params) []hiddenField {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]hiddenField, 0, len(keys))
	for _, k := range keys {
		out = append(out, hiddenField{k, p[k]})
	}
	return out
}

type operationPreviewPage struct {
	Action  maintenance.ActionInfo
	Preview maintenance.Preview
	Token   string
	Params  []hiddenField
	Error   string
	// Stale and Conflict replace the confirmation with an explanation.
	Stale    bool
	Conflict bool
	HolderID string
	Holder   string
	// From and Back keep a guided action on its page.
	From, Back string
}

func (s *Server) renderPreview(w http.ResponseWriter, r *http.Request, status int, a maintenance.ActionInfo, p maintenance.Params, message string) {
	pv, tok, err := s.op.PreviewAction(r.Context(), a.Name, p)
	if err != nil {
		s.actionFailed(w, r, a, p, err)
		return
	}
	page := operationPreviewPage{Action: a, Preview: pv, Token: string(tok), Params: echoParams(p), Error: message}
	page.From, page.Back = flowLinks(a.Name)
	s.renderStatus(w, r, status, "operation_preview", "Review: "+a.Title, page)
}

func (s *Server) handleOperationPreview(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) || s.guidedRedirect(w, r, true) {
		return
	}
	a, ok := s.lookupAction(w, r)
	if !ok {
		return
	}
	p := formParams(r.PostForm)
	if !a.Available {
		s.renderForm(w, r, http.StatusConflict, a, p, "")
		return
	}
	s.renderPreview(w, r, http.StatusOK, a, p, "")
}

func (s *Server) handleOperationRun(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) || s.guidedRedirect(w, r, true) {
		return
	}
	a, ok := s.lookupAction(w, r)
	if !ok {
		return
	}
	p := formParams(r.PostForm)
	if !a.Available {
		s.renderForm(w, r, http.StatusConflict, a, p, "")
		return
	}
	tok := r.PostForm.Get("token")
	if tok == "" {
		s.actionFailed(w, r, a, p, maintenance.ErrStalePreview)
		return
	}
	j, err := s.op.RunAction(r.Context(), a.Name, p, maintenance.Token(tok), r.PostForm.Get("typed"))
	switch {
	case err == nil:
		target := "/jobs"
		if jobIDPattern.MatchString(j.ID) {
			target += "/" + j.ID
		}
		s.done(w, r, "Started: "+a.Title+".", target)
	case errors.Is(err, maintenance.ErrConfirmationRequired):
		s.renderPreview(w, r, http.StatusUnprocessableEntity, a, p, "Type the exact phrase shown to confirm.")
	default:
		s.actionFailed(w, r, a, p, err)
	}
}

// actionFailed answers a preview or run failure. Only messages the service
// marked safe are shown; anything else is logged and answered generically.
func (s *Server) actionFailed(w http.ResponseWriter, r *http.Request, a maintenance.ActionInfo, p maintenance.Params, err error) {
	var conflict *jobs.ConflictError
	switch {
	case errors.As(err, &conflict):
		page := operationPreviewPage{Action: a, Conflict: true, Params: echoParams(p)}
		page.From, page.Back = flowLinks(a.Name)
		if jobIDPattern.MatchString(conflict.HolderID) {
			page.HolderID, page.Holder = conflict.HolderID, shortID(conflict.HolderID)
		}
		s.renderStatus(w, r, http.StatusConflict, "operation_preview", "Another job is running", page)
		return
	case errors.Is(err, maintenance.ErrStalePreview):
		page := operationPreviewPage{Action: a, Stale: true, Params: echoParams(p)}
		page.From, page.Back = flowLinks(a.Name)
		s.renderStatus(w, r, http.StatusConflict, "operation_preview", "Preview out of date", page)
		return
	case errors.Is(err, maintenance.ErrUnavailable):
		// Re-read this action so its own reason is shown, not a stale one.
		if fresh, ok := s.op.Action(a.Name); ok {
			a = fresh
		}
		a.Available = false
		s.renderForm(w, r, http.StatusConflict, a, p, "")
		return
	case errors.Is(err, maintenance.ErrUnknownAction):
		s.fail(w, r, http.StatusNotFound, "not_found", "action not found")
		return
	}
	var se *maintenance.SafeError
	if !errors.As(maintenance.Safe(err), &se) || se.Code == "internal" {
		s.internal(w, r, err)
		return
	}
	status := http.StatusUnprocessableEntity
	switch se.Code {
	case "conflict":
		status = http.StatusConflict
	case "unavailable", "rate_limited", "credentials_missing":
		status = http.StatusServiceUnavailable
	}
	s.renderForm(w, r, status, a, p, se.Safe())
}
