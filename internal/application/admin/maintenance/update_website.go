package maintenance

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Per-stage outcomes of an update-website run (OutcomeFailed is shared with
// reconcile-all-apply).
const (
	OutcomeDone    = "done"
	OutcomeSkipped = "skipped"
	OutcomeNotRun  = "not_run"
)

// Fixed messages of update-website.
const (
	updateWebsiteName   = "update-website"
	updateWebsitePhrase = "update website"
	msgCatalogChanged   = "The catalog changed outside the admin while the update was running; the run stopped " +
		"before the next step and nothing more was run. Review the plan again."
	msgPlanChanged = "The database changed outside this run and a step that was not in the confirmed plan is " +
		"now needed; the run stopped. Review the plan again."
	msgNothingToUpdate = "Nothing to update: every stage is done for the current catalog and database"
	msgPlanUnavailable = "The update plan could not be computed"
	msgSkippedDone     = "Already done for the current catalog and database."
	msgSkippedNotNeed  = "Not needed for the current catalog and database."
	msgValidated       = "The catalog validates and matches the confirmed plan."
	msgMergeDeploy     = "Merging the pull request and the GitHub Pages deploy happen on GitHub; this run stops " +
		"once the pull request is open."
	msgUnpinned = "The release is published before the pull request: until the pull request is merged, " +
		"the published release is not pinned by the site, and if it is never merged it stays unpinned."
	msgEditsBlocked = "Catalog edits in the admin are refused while the run is active; an edit made outside " +
		"the admin stops the run before its next step."
	msgReconcileAll = "Reconcile is all or nothing: a failure rolls every artist back and stops the run."
	msgStageCancel  = "Cancelled while this step was running."
)

var updateWebsiteColumns = []string{"stage", "outcome", "detail"}

// plannedStage is one stage of a confirmed plan.
type plannedStage struct {
	def  stageDef
	need StageNeed
	run  bool
}

// websitePlan is the plan a preview shows and a run executes.
type websitePlan struct {
	facts  pendingFacts
	view   PendingView
	stages []plannedStage
	// reconcileFP is the bulk reconcile plan checksum the run is bound to.
	reconcileFP string
	fingerprint string
	// blocked is why the plan cannot run, "" when it can.
	blocked string
}

func (p websitePlan) runs(name string) bool {
	for _, st := range p.stages {
		if st.def.name == name {
			return st.run
		}
	}
	return false
}

type updateWebsiteAction struct{ s *Service }

// newUpdateWebsite returns update-website: one confirmation (typed phrase
// "update website") runs every required stage, in order, as the registered
// actions' own jobs inside this one job. Each stage re-checks its own
// preview and takes its own locks; this job holds the in-process catalog,
// database, export and release resources for the whole run, so admin
// catalog edits are refused meanwhile. The run checks the catalog digest
// against the confirmed one before every stage, skips stages whose digests
// already match and stops at the first failure or cancel.
func newUpdateWebsite(s *Service) Action { return &updateWebsiteAction{s: s} }

func (a *updateWebsiteAction) Name() string { return updateWebsiteName }

func (a *updateWebsiteAction) Describe() Description {
	return Description{Title: "Update website", UsesNetwork: true, Destructive: true}
}

func (a *updateWebsiteAction) Validate(p Params) error { return RejectUnknown(p) }

// AvailableNow is cheap: the full plan (database digest, release checks) is
// only computed by Available, the preview and the run.
func (a *updateWebsiteAction) AvailableNow() (bool, string) {
	if !a.s.pending.DatabaseExists() {
		return false, noDatabaseReason
	}
	return true, ""
}

func (a *updateWebsiteAction) Available() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), availabilityTimeout)
	defer cancel()
	pl, err := a.plan(ctx)
	if err != nil {
		return false, msgPlanUnavailable
	}
	if pl.blocked != "" {
		return false, pl.blocked
	}
	return true, ""
}

// plan computes the stages to run from the current digests.
func (a *updateWebsiteAction) plan(ctx context.Context) (websitePlan, error) {
	f, err := a.s.facts(ctx)
	if err != nil {
		return websitePlan{}, err
	}
	pl := websitePlan{facts: f}
	switch {
	case f.offline:
		pl.blocked = noDatabaseReason
		return pl, nil
	case f.catalogErr != nil:
		pl.blocked = catalogInvalidOrUnreadable(f.catalogErr)
		return pl, nil
	case f.unreadable:
		pl.blocked = databaseUnreadableNote
		return pl, nil
	}
	pl.view = a.s.view(f, fullAvailable)
	runsAny := false
	for i, st := range pl.view.Stages {
		def := stageDefs[i]
		ps := plannedStage{def: def, need: st.Need}
		ps.run = st.Need == NeedRequired && (def.name == PlanValidate || def.action != "")
		if ps.run && pl.blocked == "" && (st.Status == StageBlocked || (st.Status == StageUnknown && st.BlockedReason != "")) {
			pl.blocked = st.Title + ": " + st.BlockedReason
		}
		runsAny = runsAny || (ps.run && def.name != PlanValidate)
		pl.stages = append(pl.stages, ps)
	}
	if !runsAny && pl.blocked == "" {
		pl.blocked = msgNothingToUpdate
	}
	if pl.runs(PlanReconcile) {
		pv, err := a.s.byName[stageDefs[1].action].Preview(ctx, Params{})
		if err != nil {
			return websitePlan{}, err
		}
		pl.reconcileFP = pv.Fingerprint
	}
	parts := []string{a.Name(), f.catalog.Digest, pl.reconcileFP}
	for _, st := range pl.stages {
		parts = append(parts, st.def.name+":"+string(st.need)+":"+yesNo(st.run))
	}
	pl.fingerprint = fingerprint(parts...)
	return pl, nil
}

func (a *updateWebsiteAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	pl, err := a.plan(ctx)
	if err != nil {
		return Preview{}, err
	}
	if pl.blocked != "" {
		return Preview{}, &SafeError{Code: "unavailable", Message: pl.blocked + "."}
	}
	pv := Preview{
		Title:         a.Describe().Title,
		Destructive:   true,
		UsesNetwork:   true,
		RequiresTyped: updateWebsitePhrase,
		Fingerprint:   pl.fingerprint,
	}
	pv.Scope = append(pv.Scope,
		ScopeLine{Label: "Catalog digest", Value: pl.view.CatalogDigest},
		ScopeLine{Label: "Affected artists", Value: affectedLine(pl)})
	for i, st := range pl.stages {
		pv.Scope = append(pv.Scope, ScopeLine{
			Label: "Step " + strconv.Itoa(i+1) + ": " + st.def.title,
			Value: planLine(st),
		})
	}
	pv.Scope = append(pv.Scope, a.prScope(ctx, pl)...)
	pv.Warnings = append(pv.Warnings, msgMergeDeploy, msgUnpinned, msgEditsBlocked)
	if pl.runs(PlanReconcile) {
		pv.Warnings = append(pv.Warnings, msgReconcileAll)
	}
	if pl.runs(PlanSync) {
		pv.Warnings = append(pv.Warnings, syncQuotaCaveat)
	}
	return pv, nil
}

func affectedLine(pl websitePlan) string {
	if !pl.facts.applied {
		return "unknown: the database records no applied catalog; this run records it"
	}
	slugs := make([]string, 0, len(pl.facts.changes))
	for _, c := range pl.facts.changes {
		if validSlug(c.Slug) {
			slugs = append(slugs, c.Slug)
		}
	}
	if len(slugs) == 0 {
		return "none"
	}
	return plural(len(slugs), "artist") + ": " + joinLimited(slugs, 20)
}

func planLine(st plannedStage) string {
	switch {
	case st.need == NeedManual && st.def.name == PlanBuild:
		return "manual, not run here (make site-build, then make audit-fast)"
	case st.need == NeedManual:
		return "happens on GitHub after merging, not run here"
	case st.run:
		return "required, will run"
	case st.def.name == PlanSync:
		return "not needed, skipped (no Spotify ID, verified or enabled change)"
	default:
		return "not needed, skipped"
	}
}

// prScope shows the pull request target. The branch is only final when the
// database and the release do not change during the run.
func (a *updateWebsiteAction) prScope(ctx context.Context, pl websitePlan) []ScopeLine {
	lines := map[string]string{}
	if pr, ok := a.s.byName[stageDefs[10].action]; ok {
		if pv, err := pr.Preview(ctx, Params{}); err == nil {
			for _, l := range pv.Scope {
				lines[l.Label] = l.Value
			}
		}
	}
	branch := lines["Branch"]
	if pl.runs(PlanReconcile) || pl.runs(PlanSeed) || pl.runs(PlanSync) || pl.runs(PlanReleasePublish) || branch == "" || branch == "none" {
		branch = prBranchPrefix + pl.view.CatalogDigest + "-<first 12 characters of the database content hash after this run>"
	}
	files := lines["Files"]
	if files == "" || files == "none" {
		files = strings.Join(ReleasePRFiles, ", ")
	}
	return []ScopeLine{
		{Label: "Repository", Value: orNone(lines["Repository"])},
		{Label: "Base branch", Value: orNone(lines["Base branch"])},
		{Label: "Branch", Value: branch},
		{Label: "Files", Value: files},
	}
}

// Spec binds the job to the verified plan fingerprint (fails closed without
// one) and refuses a plan that already differs.
func (a *updateWebsiteAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	fp, err := verifiedFingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	pl, err := a.plan(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	if pl.blocked != "" {
		return jobs.Spec{}, &SafeError{Code: "unavailable", Message: pl.blocked + "."}
	}
	if pl.fingerprint != fp {
		return jobs.Spec{}, ErrStalePreview
	}
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: []string{ResourceCatalogYAML, ResourceDatabase, ResourceExport, ResourceRelease},
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, pl)
		},
	}, nil
}

// runState is the per-stage report of one run.
type runState struct {
	pl   websitePlan
	rows [][]string
	pr   string // the pull request stage's summary
}

func (rs *runState) set(i int, outcome, detail string) {
	rs.rows[i][1], rs.rows[i][2] = outcome, detail
}

func (rs *runState) report() *jobs.Report {
	rows := make([][]string, len(rs.rows))
	for i, r := range rs.rows {
		rows[i] = append([]string(nil), r...)
	}
	return &jobs.Report{Name: updateWebsiteName, Columns: updateWebsiteColumns, Rows: rows}
}

func (rs *runState) names(outcome string) []string {
	var out []string
	for _, r := range rs.rows {
		if r[1] == outcome {
			out = append(out, r[0])
		}
	}
	return out
}

func (rs *runState) progressLine() string {
	line := " Completed: " + orNone(strings.Join(rs.names(OutcomeDone), ", ")) + "."
	if notRun := rs.names(OutcomeNotRun); len(notRun) > 0 {
		line += " Not run: " + strings.Join(notRun, ", ") + "."
	}
	return line
}

func (a *updateWebsiteAction) run(ctx context.Context, r jobs.Reporter, pl websitePlan) (jobs.Outcome, error) {
	rs := &runState{pl: pl, rows: make([][]string, len(pl.stages))}
	for i, st := range pl.stages {
		rs.rows[i] = []string{st.def.name, OutcomeNotRun, ""}
	}
	confirmed := pl.facts.catalog.Digest
	for i, st := range pl.stages {
		r.Progress(i, len(pl.stages))
		if err := ctx.Err(); err != nil {
			return rs.cancelled(err)
		}
		r.Step(st.def.title)
		switch st.def.name {
		case PlanBuild:
			rs.set(i, OutcomeSkipped, buildNote)
			continue
		case PlanDeploy:
			rs.set(i, OutcomeSkipped, deployNote)
			continue
		}
		// An edit made outside the admin (admin edits are refused while this
		// job holds the catalog) stops the run before the next stage.
		current, err := a.s.pending.Catalog(ctx)
		if ctx.Err() != nil {
			return rs.cancelled(ctx.Err())
		}
		if err != nil {
			return rs.failed(i, &SafeError{Code: "validation", Message: catalogInvalidOrUnreadable(err) + "."})
		}
		if current.Digest != confirmed {
			return rs.failed(i, &SafeError{Code: "stale_preview", Message: msgCatalogChanged})
		}
		if st.def.name == PlanValidate {
			rs.set(i, OutcomeDone, msgValidated)
			continue
		}
		f, err := a.s.facts(ctx)
		if err != nil {
			return rs.cancelled(err)
		}
		if !f.usable() {
			reason := databaseUnreadableNote
			if f.offline {
				reason = noDatabaseReason
			}
			return rs.failed(i, &SafeError{Code: "unavailable", Message: reason + "."})
		}
		live := needs(f)[st.def.name] == NeedRequired
		switch {
		case !live && st.run:
			rs.set(i, OutcomeSkipped, msgSkippedDone)
			continue
		case !live:
			detail := msgSkippedNotNeed
			if st.def.name == PlanSync {
				detail += " " + syncNotNeededNote
			}
			rs.set(i, OutcomeSkipped, detail)
			continue
		case !st.run:
			return rs.failed(i, &SafeError{Code: "stale_preview", Message: msgPlanChanged})
		}
		out, err := a.runStage(ctx, r, st, pl.reconcileFP)
		switch {
		case err != nil && ctx.Err() != nil && !isFinal(err):
			rs.set(i, OutcomeFailed, joinDetail(msgStageCancel, out.Result.Summary))
			return rs.cancelled(ctx.Err())
		case err != nil:
			return rs.failedWith(i, err, out.Result.Summary)
		case out.Status != jobs.StatusSucceeded:
			rs.set(i, OutcomeFailed, joinDetail("The step did not complete.", out.Result.Summary))
			summary := "Update website stopped: " + st.def.title + " did not complete." + rs.progressLine() +
				" Run update website again to continue; completed steps are skipped."
			return jobs.Outcome{Status: jobs.StatusPartial, Resumable: true,
				Result: jobs.Result{Summary: summary, Report: rs.report()}}, nil
		}
		rs.set(i, OutcomeDone, out.Result.Summary)
		if st.def.name == PlanPR {
			rs.pr = out.Result.Summary
		}
	}
	r.Progress(len(pl.stages), len(pl.stages))
	summary := "Website update finished for catalog " + pl.view.CatalogDigest + "." + rs.progressLine()
	if rs.pr != "" {
		summary += " " + rs.pr
	}
	summary += " " + msgMergeDeploy
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: rs.report()}}, nil
}

func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func isFinal(err error) bool {
	var fe *jobs.FinalError
	return errors.As(err, &fe)
}

// cancelled ends the run as cancelled; re-running continues it.
func (rs *runState) cancelled(err error) (jobs.Outcome, error) {
	summary := "Update website was cancelled." + rs.progressLine() +
		" Run update website again to continue; completed steps are skipped."
	return jobs.Outcome{Resumable: true, Result: jobs.Result{Summary: summary, Report: rs.report()}}, err
}

// failed ends the run as failed at stage i with a safe error.
func (rs *runState) failed(i int, se *SafeError) (jobs.Outcome, error) {
	return rs.failedWith(i, se, "")
}

func (rs *runState) failedWith(i int, err error, detail string) (jobs.Outcome, error) {
	se := Safe(err).(*SafeError)
	rs.set(i, OutcomeFailed, joinDetail(se.Message, detailIfNew(detail, se.Message)))
	summary := "Update website stopped at " + rs.pl.stages[i].def.title + ": " + se.Message + rs.progressLine()
	out := jobs.Outcome{Result: jobs.Result{Summary: summary, Report: rs.report()}}
	stop := &SafeError{Code: se.Code, Message: summary, cause: err}
	if isFinal(err) {
		return out, &jobs.FinalError{Err: stop}
	}
	return out, stop
}

func detailIfNew(detail, msg string) string {
	if detail == msg || strings.Contains(msg, detail) {
		return ""
	}
	return detail
}

// runStage runs one stage as its registered action's own job, inside this
// job: it previews it now (its own state checks), binds its spec to that
// preview, as the user's single confirmation of the whole plan covers it,
// and runs its job function. The bulk reconcile is bound to the plan
// checksum the user confirmed instead.
func (a *updateWebsiteAction) runStage(ctx context.Context, r jobs.Reporter, st plannedStage, reconcileFP string) (jobs.Outcome, error) {
	sub, ok := a.s.byName[st.def.action]
	if !ok {
		return jobs.Outcome{}, &SafeError{Code: "unavailable", Message: stageMissingReason + "."}
	}
	params := maps.Clone(st.def.params)
	if params == nil {
		params = Params{}
	}
	if err := sub.Validate(params); err != nil {
		return jobs.Outcome{}, validationErr(err)
	}
	if ok, reason := sub.Available(); !ok {
		return jobs.Outcome{}, &SafeError{Code: "unavailable", Message: reason + "."}
	}
	fp := ""
	if st.def.name == PlanReconcile {
		fp = reconcileFP
	} else {
		pv, err := sub.Preview(ctx, maps.Clone(params))
		if err != nil {
			return jobs.Outcome{}, err
		}
		fp = pv.Fingerprint
	}
	spec, err := sub.Spec(WithVerifiedFingerprint(ctx, fp), maps.Clone(params))
	if err != nil {
		return jobs.Outcome{}, err
	}
	return spec.Run(ctx, stageReporter{r: r, prefix: st.def.name})
}

// stageReporter prefixes a stage's steps; the stage index is the run's
// progress, so the stage's own progress is not reported.
type stageReporter struct {
	r      jobs.Reporter
	prefix string
}

func (s stageReporter) Step(name string)            { s.r.Step(s.prefix + ": " + name) }
func (s stageReporter) Progress(int, int)           {}
func (s stageReporter) Count(key string, delta int) { s.r.Count(key, delta) }
