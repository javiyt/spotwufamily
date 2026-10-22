package httpadmin

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

// pendingTTL is how long a computed PendingView is reused. Pending reads the
// database, and the banner asks for it on every HTML page.
const pendingTTL = 5 * time.Second

const (
	updateWebsiteAction = "update-website"
	baselineAction      = "reconcile-all-dry-run"
)

// pendingTimeout bounds one Pending computation. It runs detached from any
// request, so one slow or cancelled request cannot hang or poison the others.
var pendingTimeout = 2 * time.Second

// pendingErrTTL is how long a failed computation is reused.
const pendingErrTTL = 2 * time.Second

type pendingCall struct {
	done chan struct{}
	v    maintenance.PendingView
	ok   bool
}

// pendingCache keeps the last result (success or failure) and the computation
// in flight (single flight). The mutex is never held while Pending runs.
type pendingCache struct {
	mu       sync.Mutex
	val      maintenance.PendingView
	valid    bool // a result is cached
	failed   bool
	at       time.Time
	gen      uint64
	inflight *pendingCall
}

// invalidate drops the cached result (the catalog was just written by the
// admin); a computation already running will not be cached.
func (c *pendingCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valid = false
	c.gen++
	c.inflight = nil
}

// pendingView returns the (cached) pending view; ok is false when the
// operator cannot report it, computing it failed or timed out. It is computed
// lazily, so pages that never ask do not pay for it.
func (s *Server) pendingView(ctx context.Context) (maintenance.PendingView, bool) {
	if s.pend == nil {
		return maintenance.PendingView{}, false
	}
	c := &s.pendCache
	c.mu.Lock()
	if c.valid {
		ttl := pendingTTL
		if c.failed {
			ttl = pendingErrTTL
		}
		if s.now().Sub(c.at) < ttl {
			v, ok := c.val, !c.failed
			c.mu.Unlock()
			return v, ok
		}
	}
	call := c.inflight
	if call == nil {
		call = &pendingCall{done: make(chan struct{})}
		c.inflight = call
		gen := c.gen
		go s.computePending(call, gen)
	}
	c.mu.Unlock()
	select {
	case <-call.done:
		return call.v, call.ok
	case <-ctx.Done():
		return maintenance.PendingView{}, false
	}
}

func (s *Server) computePending(call *pendingCall, gen uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), pendingTimeout)
	defer cancel()
	type result struct {
		v   maintenance.PendingView
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := s.pend.Pending(ctx)
		ch <- result{v, err}
	}()
	// Race the provider against the deadline: a provider that ignores its
	// context must not keep the computation in flight. Its late result is
	// dropped.
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		res.err = ctx.Err()
	}
	v, err := res.v, res.err
	if err != nil {
		s.logger.Printf("pending changes: %s", maintenance.LogText(err))
		v = maintenance.PendingView{}
	}
	call.v, call.ok = v, err == nil
	c := &s.pendCache
	c.mu.Lock()
	if c.gen == gen {
		c.val, c.valid, c.failed, c.at = v, true, err != nil, s.now()
		c.inflight = nil
	}
	c.mu.Unlock()
	close(call.done)
}

// registerPending wires the pending-changes provider and its API route.
func (s *Server) registerPending() {
	if p, ok := s.op.(PendingProvider); ok {
		s.pend = p
		s.routes["/api/v1/pending"] = route{methods: []string{http.MethodGet}, handler: s.apiPending}
	}
}

// banner is the site-wide notice about catalog edits the database has not
// applied. It is empty when there is nothing to say.
type banner struct {
	Text string
}

// pendingBanner computes the banner for an HTML page. It is only reached for
// authenticated pages, so login, static files and API calls never trigger it.
func (s *Server) pendingBanner(ctx context.Context) *banner {
	if s.pend == nil {
		return nil
	}
	v, ok := s.pendingView(ctx)
	if !ok {
		return &banner{Text: unknownBanner}
	}
	switch {
	case v.State == maintenance.PendingUnknown:
		return &banner{Text: unknownBanner}
	case v.State == maintenance.PendingChanges && len(v.Changes) > 0:
		return &banner{Text: fmt.Sprintf("%d %s modified since last database reconciliation", len(v.Changes), plural(len(v.Changes), "artist", "artists"))}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

var changeLabels = map[artists.ChangeKind]string{
	artists.ChangeAdded:         "Artist added",
	artists.ChangeRemoved:       "Artist removed",
	artists.ChangeIDsAdded:      "Spotify IDs added",
	artists.ChangeIDsRemoved:    "Spotify IDs removed",
	artists.ChangeIDsReordered:  "Spotify IDs reordered",
	artists.ChangeVerified:      "Verified changed",
	artists.ChangeEnabled:       "Enabled changed",
	artists.ChangeRelationships: "Relationships changed",
	artists.ChangeMetadata:      "Metadata changed",
}

func changeLabel(k artists.ChangeKind) string {
	if l, ok := changeLabels[k]; ok {
		return l
	}
	return "Other change"
}

type changeRow struct {
	Slug   string
	Labels []string
	// Reconcile, Sync and SeedOnly are the per-artist badges.
	Reconcile, Sync, SeedOnly bool
}

type stageRow struct {
	N                      int
	Name, Title            string
	Need, NeedClass        string
	Status, StatusClass    string
	Reason, Note           string
	Page                   string // call to action; empty when there is none
	Blocked                bool
	CTADisabled, CTAReason string
}

type pendingPanel struct {
	// Available is false when the operator cannot report pending changes.
	Available bool
	// Failed means Pending returned an error.
	Failed                           bool
	State                            string
	Clean, Unknown, Offline, Pending bool
	Count                            int
	Rows                             []changeRow
	Stages                           []stageRow
	// Primary call to action: the update-website preview page.
	UpdatePage   string
	UpdateReason string // set when the action is unavailable
	// BaselinePage runs the baseline check (unknown state).
	BaselinePage string
	// Honest status lines.
	Database, Build, Release, PR, Deploy string
	PRDone                               bool
}

var needLabels = map[maintenance.StageNeed]string{
	maintenance.NeedRequired: "Required", maintenance.NeedOptional: "Optional",
	maintenance.NeedNotNeeded: "Not needed", maintenance.NeedManual: "Manual",
}

var stageStatusLabels = map[maintenance.StageStatus]string{
	maintenance.StageDone: "Done", maintenance.StageTodo: "To do",
	maintenance.StageBlocked: "Blocked", maintenance.StageUnknown: "Unknown",
}

func needClass(n maintenance.StageNeed) string {
	if n == maintenance.NeedRequired {
		return "warn"
	}
	return ""
}

func stageStatusClass(st maintenance.StageStatus) string {
	switch st {
	case maintenance.StageDone:
		return "ok"
	case maintenance.StageBlocked:
		return "bad"
	case maintenance.StageTodo:
		return "warn"
	}
	return ""
}

// pendingPanel builds the panel from the cached pending view. avail maps the
// registered actions by name.
func (s *Server) pendingPanel(ctx context.Context) pendingPanel {
	if s.pend == nil {
		return pendingPanel{}
	}
	p := pendingPanel{Available: true}
	v, ok := s.pendingView(ctx)
	if !ok {
		p.Failed = true
		return p
	}
	avail := map[string]maintenance.ActionInfo{}
	for _, a := range s.op.Actions() {
		avail[a.Name] = a
	}
	p.State = string(v.State)
	p.Clean = v.State == maintenance.PendingClean
	p.Unknown = v.State == maintenance.PendingUnknown
	p.Offline = v.State == maintenance.PendingOffline
	p.Pending = v.State == maintenance.PendingChanges
	p.Count = len(v.Changes)

	for _, c := range v.Changes {
		row := changeRow{Slug: c.Slug, Reconcile: c.NeedsReconcile, Sync: c.NeedsSync, SeedOnly: !c.NeedsReconcile && !c.NeedsSync}
		for _, k := range c.Kinds {
			row.Labels = append(row.Labels, changeLabel(k))
		}
		p.Rows = append(p.Rows, row)
	}

	if a, ok := avail[updateWebsiteAction]; ok {
		if a.Available {
			p.UpdatePage = actionPage(a)
		} else {
			p.UpdateReason = unavailableReason(a)
		}
	} else {
		p.UpdateReason = "The update-website action is not available in this admin."
	}
	if a, ok := avail[baselineAction]; ok && a.Available {
		p.BaselinePage = actionPage(a)
	}

	for i, st := range v.Stages {
		row := stageRow{N: i + 1, Name: st.Name, Title: st.Title,
			Need: needLabels[st.Need], NeedClass: needClass(st.Need),
			Status: stageStatusLabels[st.Status], StatusClass: stageStatusClass(st.Status),
			Reason: st.BlockedReason, Note: st.Note, Blocked: st.Status == maintenance.StageBlocked}
		if st.Action != "" && st.Status != maintenance.StageDone && st.Need != maintenance.NeedNotNeeded {
			switch a, found := avail[st.Action]; {
			case row.Blocked:
				row.CTADisabled, row.CTAReason = "Review", st.BlockedReason
			case !found:
				row.CTADisabled, row.CTAReason = "Review", stageMissing
			case !a.Available:
				row.CTADisabled, row.CTAReason = "Review", unavailableReason(a)
			default:
				row.Page = actionPage(a)
			}
		}
		p.Stages = append(p.Stages, row)
		switch st.Name {
		case maintenance.PlanBuild:
			if st.Status == maintenance.StageDone {
				p.Build = "Local site rebuilt"
			}
		case maintenance.PlanReleasePublish:
			if st.Status == maintenance.StageDone {
				p.Release = "Release published"
			}
		case maintenance.PlanPR:
			p.PRDone = st.Status == maintenance.StageDone
		}
	}
	switch v.State {
	case maintenance.PendingClean:
		p.Database = "Local database up to date"
	case maintenance.PendingChanges:
		p.Database = "Local database not reconciled with the catalog"
	case maintenance.PendingOffline:
		p.Database = "No local database"
	default:
		p.Database = "Local database state unknown"
	}
	if p.Build == "" {
		p.Build = "Local site build: not tracked from here (build it with make site-build)"
	}
	if p.Release == "" {
		p.Release = "Release not published yet"
	}
	if p.PRDone {
		p.PR = "Pull request open — waiting for merge and GitHub Pages deploy"
	} else {
		p.PR = "Pull request not opened yet"
	}
	p.Deploy = "Published website updated: not confirmed — check GitHub Pages after merging. Merging and the Pages deploy happen on GitHub."
	return p
}

const unknownBanner = "Database catalog state unknown — check pending changes"

const stageMissing = "This step is not available in this admin"

func (s *Server) apiPending(w http.ResponseWriter, r *http.Request) {
	v, ok := s.pendingView(r.Context())
	if !ok {
		s.fail(w, r, http.StatusServiceUnavailable, "pending_unavailable", "Pending changes could not be read.")
		return
	}
	type change struct {
		Slug           string   `json:"slug"`
		Kinds          []string `json:"kinds"`
		NeedsReconcile bool     `json:"needs_reconcile"`
		NeedsSync      bool     `json:"needs_sync"`
	}
	type stage struct {
		Name          string `json:"name"`
		Title         string `json:"title"`
		Need          string `json:"need"`
		Status        string `json:"status"`
		BlockedReason string `json:"blocked_reason,omitempty"`
		Action        string `json:"action,omitempty"`
	}
	changes := []change{}
	for _, c := range v.Changes {
		kinds := []string{}
		for _, k := range c.Kinds {
			kinds = append(kinds, string(k))
		}
		changes = append(changes, change{Slug: c.Slug, Kinds: kinds, NeedsReconcile: c.NeedsReconcile, NeedsSync: c.NeedsSync})
	}
	stages := []stage{}
	for _, st := range v.Stages {
		stages = append(stages, stage{Name: st.Name, Title: st.Title, Need: string(st.Need), Status: string(st.Status),
			BlockedReason: st.BlockedReason, Action: st.Action})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": string(v.State), "pending_changes": len(changes), "changes": changes, "stages": stages,
	})
}
