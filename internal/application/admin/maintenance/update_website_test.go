package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// world is the shared state the fake stage actions change: the editorial
// catalog, the database digest and the provenance record.
type world struct {
	mu       sync.Mutex
	cat      catalog.EditorialCatalog
	catErr   error
	dbExists bool
	prov     *fakeProvenance
	dbN      int
	pushed   atomic.Int32
}

func newWorld(applied, current catalog.EditorialCatalog) *world {
	w := &world{cat: current, dbExists: true, prov: newFakeProvenance()}
	a := artists.FingerprintCatalog(applied)
	w.prov.applied = &a
	w.prov.db = "db-0"
	return w
}

func (w *world) fingerprint() artists.AppliedCatalog {
	w.mu.Lock()
	defer w.mu.Unlock()
	return artists.FingerprintCatalog(w.cat)
}

func (w *world) catalog(context.Context) (artists.AppliedCatalog, error) {
	w.mu.Lock()
	err := w.catErr
	w.mu.Unlock()
	if err != nil {
		return artists.AppliedCatalog{}, err
	}
	return w.fingerprint(), nil
}

func (w *world) edit(fn func(*catalog.EditorialCatalog)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(&w.cat)
}

// changeDB simulates a write to the database.
func (w *world) changeDB() {
	w.prov.mu.Lock()
	defer w.prov.mu.Unlock()
	w.dbN++
	w.prov.db = fmt.Sprintf("db-%d", w.dbN)
}

func (w *world) deps() PendingDeps {
	return PendingDeps{
		DatabaseExists: func() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.dbExists },
		Catalog:        w.catalog,
		Provenance:     w.prov,
	}
}

// stageFake is a registered stage action whose job applies its effect to
// the world.
type stageFake struct {
	name        string
	w           *world
	mu          sync.Mutex
	available   bool
	reason      string
	quickReason string
	runs        atomic.Int32
	verified    []string
	scope       []ScopeLine
	// runFn replaces the default effect when set.
	runFn func(ctx context.Context) (jobs.Outcome, error)
	// onPreview runs during every preview after the second (the composite's
	// preview and its confirmation preview come first).
	onPreview func()
	previews  atomic.Int32
}

func (f *stageFake) Name() string            { return f.name }
func (f *stageFake) Validate(p Params) error { return RejectUnknown(p, "scope", "resume") }
func (f *stageFake) Available() (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available, f.reason
}

func (f *stageFake) AvailableNow() (bool, string) {
	f.mu.Lock()
	q := f.quickReason
	f.mu.Unlock()
	if q != "" {
		return false, q
	}
	return f.Available()
}

func (f *stageFake) setUnavailable(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.available, f.reason = false, reason
}

func (f *stageFake) Preview(context.Context, Params) (Preview, error) {
	if n := f.previews.Add(1); n > 2 && f.onPreview != nil {
		f.onPreview()
	}
	return Preview{Title: f.name, Fingerprint: f.name + "-fp", Scope: f.scope}, nil
}

func (f *stageFake) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	fp, ok := VerifiedFingerprint(ctx)
	if !ok {
		return jobs.Spec{}, ErrConfirmationRequired
	}
	f.mu.Lock()
	f.verified = append(f.verified, fp)
	f.mu.Unlock()
	return jobs.Spec{Kind: f.name, Resources: []string{ResourceDatabase}, Run: func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
		f.runs.Add(1)
		if f.runFn != nil {
			return f.runFn(ctx)
		}
		return f.effect(ctx)
	}}, nil
}

// effect is what the real action records: database stages change the
// database and the applied record, the others checkpoint themselves.
func (f *stageFake) effect(ctx context.Context) (jobs.Outcome, error) {
	w := f.w
	cur := w.fingerprint()
	ok := jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: f.name + " ok."}}
	switch f.name {
	case "reconcile-all-apply":
		prev, has, _ := w.prov.AppliedCatalog(ctx)
		if has {
			var runs []appliedRun
			for _, c := range artists.DiffCatalog(prev, cur) {
				// New Spotify IDs await their resync: only the sync records them.
				if c.NeedsReconcile && !c.NeedsSync {
					runs = append(runs, appliedRun{slug: c.Slug, used: cur})
				}
			}
			w.changeDB()
			_ = mergeApplied(ctx, w.prov, artists.StageReconcile, runs, artists.MergeReconciled)
		}
	case "db-seed":
		w.changeDB()
		_ = recordSeeded(ctx, w.prov, cur)
	case "sync":
		w.changeDB()
		_ = recordApplied(ctx, w.prov, artists.StageSync, cur)
	case "db-snapshot":
		_ = recordCheckpoint(ctx, w.prov, artists.StageSnapshot, cur.Digest)
	case "export":
		_ = recordCheckpoint(ctx, w.prov, artists.StageExport, cur.Digest)
	case "release-prepare":
		_ = recordCheckpoint(ctx, w.prov, artists.StageReleasePrepare, cur.Digest)
	case "release-publish":
		_ = recordCheckpoint(ctx, w.prov, artists.StageRelease, cur.Digest)
	case "release-pr":
		w.pushed.Add(1)
		_ = recordCheckpoint(ctx, w.prov, artists.StagePR, cur.Digest)
		ok.Result.Summary = "Opened pull request https://github.com/javiyt/spotwufamily/pull/7."
	}
	return ok, nil
}

var stageActionNames = []string{"reconcile-all-apply", "db-seed", "sync", "db-verify", "db-snapshot", "export",
	"release-prepare", "release-publish", "release-pr"}

type harness struct {
	w      *world
	s      *Service
	m      *jobs.Manager
	stages map[string]*stageFake
}

func newHarness(t *testing.T, w *world, store jobs.Store) *harness {
	t.Helper()
	if store == nil {
		store = &memStore{}
	}
	h := &harness{w: w, stages: map[string]*stageFake{}}
	var actions []Action
	for _, n := range stageActionNames {
		f := &stageFake{name: n, w: w, available: true}
		if n == "release-pr" {
			f.scope = []ScopeLine{{Label: "Repository", Value: "javiyt/spotwufamily"}, {Label: "Base branch", Value: "main"},
				{Label: "Branch", Value: "catalog-update-aaaaaaaaaaaa-bbbbbbbbbbbb"}, {Label: "Files", Value: strings.Join(ReleasePRFiles, ", ")}}
		}
		h.stages[n] = f
		actions = append(actions, f)
	}
	var n atomic.Int32
	h.m = jobs.NewManager(store, time.Now, func() string { return fmt.Sprintf("job-%d", n.Add(1)) })
	h.s = NewService(h.m, actions, []byte("secret"), time.Now, WithPending(w.deps()))
	return h
}

func (h *harness) pending(t *testing.T) PendingView {
	t.Helper()
	v, err := h.s.Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	return v
}

func stageOf(t *testing.T, v PendingView, name string) StageView {
	t.Helper()
	for _, st := range v.Stages {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no stage %q in %+v", name, v.Stages)
	return StageView{}
}

// start previews and confirms update-website and returns the submitted job.
func (h *harness) start(t *testing.T) jobs.Job {
	t.Helper()
	ctx := context.Background()
	_, tok, err := h.s.Preview(ctx, updateWebsiteName, Params{})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	j, err := h.s.Run(ctx, updateWebsiteName, Params{}, tok, updateWebsitePhrase)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return j
}

func (h *harness) runToEnd(t *testing.T) jobs.Job {
	t.Helper()
	j := h.start(t)
	h.m.Wait(j.ID)
	got, _ := h.m.Get(j.ID)
	return got
}

func stageOutcomes(t *testing.T, j jobs.Job) map[string]string {
	t.Helper()
	if j.Result == nil || j.Result.Report == nil {
		t.Fatalf("job %s has no report: %+v", j.Status, j)
	}
	out := map[string]string{}
	for _, r := range j.Result.Report.Rows {
		out[r[0]] = r[1]
	}
	return out
}

func baseCatalog() catalog.EditorialCatalog {
	return provCatalog(provArtist("gza", "id-gza"), provArtist("rza", "id-rza"))
}

func metadataEdited() catalog.EditorialCatalog {
	c := baseCatalog()
	c.Artists[0].Name = "GZA / Genius"
	return c
}

func idEdited() catalog.EditorialCatalog {
	return provCatalog(provArtist("gza", "id-gza", "id-genius"), provArtist("rza", "id-rza"))
}

func TestPendingMetadataOnlyChangeNeedsSeedNotSync(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), metadataEdited()), nil)
	v := h.pending(t)
	if v.State != PendingChanges || v.Updated {
		t.Fatalf("state %q updated %v", v.State, v.Updated)
	}
	if len(v.Changes) != 1 || v.Changes[0].Slug != "gza" {
		t.Fatalf("changes %+v", v.Changes)
	}
	if st := stageOf(t, v, PlanSeed); st.Need != NeedRequired || st.Status != StageTodo || st.Action != "db-seed" {
		t.Fatalf("seed %+v", st)
	}
	sync := stageOf(t, v, PlanSync)
	if sync.Need != NeedNotNeeded || !strings.Contains(sync.Note, "Not every edit needs a Spotify call") {
		t.Fatalf("sync %+v", sync)
	}
	if st := stageOf(t, v, PlanReconcile); st.Need != NeedNotNeeded {
		t.Fatalf("reconcile %+v", st)
	}
	if len(v.CatalogDigest) != 12 {
		t.Fatalf("catalog digest %q", v.CatalogDigest)
	}
	var names []string
	for _, st := range v.Stages {
		names = append(names, st.Name)
	}
	want := []string{"validate", "reconcile", "seed", "sync", "verify", "snapshot", "export", "build",
		"release-prepare", "release-publish", "pr", "deploy"}
	if !slices.Equal(names, want) {
		t.Fatalf("stages %v", names)
	}
	if st := stageOf(t, v, PlanBuild); st.Need != NeedManual || !strings.Contains(st.Note, "make site-build") {
		t.Fatalf("build %+v", st)
	}
	if st := stageOf(t, v, PlanDeploy); st.Status != StageUnknown || !strings.Contains(st.Note, "after merging") {
		t.Fatalf("deploy %+v", st)
	}
}

func TestPendingSpotifyIDChangeNeedsReconcileAndSync(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	v := h.pending(t)
	for _, name := range []string{PlanReconcile, PlanSeed, PlanSync, PlanVerify, PlanSnapshot, PlanExport,
		PlanReleasePrepare, PlanReleasePublish, PlanPR} {
		if st := stageOf(t, v, name); st.Need != NeedRequired || st.Status != StageTodo {
			t.Fatalf("%s: %+v", name, st)
		}
	}
}

func TestPendingBlockedReasonsAreDistinct(t *testing.T) {
	reasons := map[string]string{}

	noDB := newHarness(t, newWorld(baseCatalog(), metadataEdited()), nil)
	noDB.w.dbExists = false
	v := noDB.pending(t)
	if v.State != PendingOffline {
		t.Fatalf("state %q", v.State)
	}
	st := stageOf(t, v, PlanSeed)
	if st.Status != StageBlocked {
		t.Fatalf("seed %+v", st)
	}
	reasons["db"] = st.BlockedReason

	noSpotify := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	noSpotify.stages["sync"].setUnavailable(spotifyUnavailableReason)
	st = stageOf(t, noSpotify.pending(t), PlanSync)
	if st.Status != StageBlocked {
		t.Fatalf("sync %+v", st)
	}
	reasons["spotify"] = st.BlockedReason

	noGh := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	noGh.stages["release-publish"].setUnavailable(ghNotAuthenticatedReason)
	noGh.stages["release-pr"].setUnavailable(ghNotAuthenticatedReason)
	v = noGh.pending(t)
	if st = stageOf(t, v, PlanReleasePublish); st.Status != StageBlocked {
		t.Fatalf("publish %+v", st)
	}
	st = stageOf(t, v, PlanPR)
	if st.Status != StageBlocked {
		t.Fatalf("pr %+v", st)
	}
	reasons["gh"] = st.BlockedReason

	release := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	release.stages["release-prepare"].setUnavailable(releaseScriptReason)
	st = stageOf(t, release.pending(t), PlanReleasePrepare)
	if st.Status != StageBlocked {
		t.Fatalf("prepare %+v", st)
	}
	reasons["release"] = st.BlockedReason

	seen := map[string]string{}
	for k, r := range reasons {
		if r == "" {
			t.Fatalf("%s: empty reason", k)
		}
		if other, dup := seen[r]; dup {
			t.Fatalf("%s and %s share the reason %q", k, other, r)
		}
		seen[r] = k
		if strings.Contains(r, "/Users") || strings.Contains(r, "/tmp") {
			t.Fatalf("path in reason %q", r)
		}
	}

	// Unavailable: preview refuses with the blocked stage's reason.
	_, _, err := noSpotify.s.Preview(context.Background(), updateWebsiteName, Params{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("preview with a blocked stage: %v", err)
	}
	if ok, reason := noSpotify.s.byName[updateWebsiteName].Available(); ok || !strings.Contains(reason, spotifyUnavailableReason) {
		t.Fatalf("available %v %q", ok, reason)
	}
}

func TestPendingPRWaitsForPublishInsteadOfBlocking(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	h.stages["release-pr"].setUnavailable(prManifestReason)
	if st := stageOf(t, h.pending(t), PlanPR); st.Status != StageTodo {
		t.Fatalf("pr before publish %+v", st)
	}
	h.stages["release-pr"].quickReason = prCheckingReason
	if st := stageOf(t, h.pending(t), PlanPR); st.Status != StageUnknown || st.BlockedReason != prCheckingReason {
		t.Fatalf("pr while checking %+v", st)
	}
}

func TestPendingLegacyDatabaseIsUnknownWithBaselineCheck(t *testing.T) {
	w := newWorld(baseCatalog(), baseCatalog())
	w.prov.applied = nil
	h := newHarness(t, w, nil)
	v := h.pending(t)
	if v.State != PendingUnknown || v.Updated || len(v.Changes) != 0 {
		t.Fatalf("legacy view %+v", v)
	}
	seed := stageOf(t, v, PlanSeed)
	if seed.Status != StageUnknown || seed.Action != "reconcile-all-dry-run" || !strings.Contains(seed.Note, "unknown") {
		t.Fatalf("seed %+v", seed)
	}
	if st := stageOf(t, v, PlanSync); st.Need != NeedRequired {
		t.Fatalf("legacy sync must establish the baseline: %+v", st)
	}
}

func TestPendingInvalidCatalogBlocksEverything(t *testing.T) {
	w := newWorld(baseCatalog(), metadataEdited())
	w.catErr = ErrCatalogInvalid
	h := newHarness(t, w, nil)
	v := h.pending(t)
	if v.State != PendingUnknown {
		t.Fatalf("state %q", v.State)
	}
	if st := stageOf(t, v, PlanValidate); st.Status != StageBlocked || st.BlockedReason != catalogInvalidReason {
		t.Fatalf("validate %+v", st)
	}
}

func TestUpdateWebsitePreviewShowsPlanTargetAndWarnings(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), metadataEdited()), nil)
	pv, _, err := h.s.Preview(context.Background(), updateWebsiteName, Params{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.RequiresTyped != "update website" || !pv.Destructive || !pv.UsesNetwork {
		t.Fatalf("preview flags %+v", pv)
	}
	lines := map[string]string{}
	for _, l := range pv.Scope {
		lines[l.Label] = l.Value
	}
	if !strings.Contains(lines["Affected artists"], "gza") {
		t.Fatalf("affected %q", lines["Affected artists"])
	}
	if lines["Step 3: Seed the database from the catalog"] != "required, will run" ||
		!strings.HasPrefix(lines["Step 4: Sync from Spotify"], "not needed") ||
		!strings.HasPrefix(lines["Step 8: Build and audit the site locally"], "manual") {
		t.Fatalf("plan lines %v", lines)
	}
	if lines["Repository"] != "javiyt/spotwufamily" || lines["Base branch"] != "main" ||
		!strings.HasPrefix(lines["Branch"], "catalog-update-") || lines["Files"] != strings.Join(ReleasePRFiles, ", ") {
		t.Fatalf("target %v", lines)
	}
	all := strings.Join(pv.Warnings, " ")
	for _, want := range []string{"GitHub Pages deploy happen on GitHub", "not pinned"} {
		if !strings.Contains(all, want) {
			t.Fatalf("warnings %q lack %q", all, want)
		}
	}
}

func TestUpdateWebsiteRefusesAStalePlan(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), metadataEdited()), nil)
	ctx := context.Background()
	_, tok, err := h.s.Preview(ctx, updateWebsiteName, Params{})
	if err != nil {
		t.Fatal(err)
	}
	h.w.edit(func(c *catalog.EditorialCatalog) { c.Artists[1].Name = "RZA / Bobby Digital" })
	if _, err := h.s.Run(ctx, updateWebsiteName, Params{}, tok, updateWebsitePhrase); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("stale run: %v", err)
	}
	if _, err := h.s.Run(ctx, updateWebsiteName, Params{}, tok, "wrong"); err == nil {
		t.Fatal("wrong phrase accepted")
	}
}

func TestUpdateWebsiteFullRunMakesPendingClean(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	if v := h.pending(t); v.State != PendingChanges {
		t.Fatalf("before %q", v.State)
	}
	j := h.runToEnd(t)
	if j.Status != jobs.StatusSucceeded {
		t.Fatalf("status %s %q", j.Status, j.Message)
	}
	got := stageOutcomes(t, j)
	for _, n := range []string{"validate", "reconcile", "seed", "sync", "verify", "snapshot", "export",
		"release-prepare", "release-publish", "pr"} {
		if got[n] != OutcomeDone && !(n == "seed" && got[n] == OutcomeSkipped) {
			t.Fatalf("%s: %s (%v)", n, got[n], j.Result.Report.Rows)
		}
	}
	if got["build"] != OutcomeSkipped || got["deploy"] != OutcomeSkipped {
		t.Fatalf("manual stages %v", got)
	}
	if !strings.Contains(j.Result.Summary, "pull/7") || !strings.Contains(j.Result.Summary, "GitHub Pages") {
		t.Fatalf("summary %q", j.Result.Summary)
	}
	// Sub-actions were bound to fingerprints; the reconcile to the confirmed plan.
	if fps := h.stages["reconcile-all-apply"].verified; len(fps) != 1 || fps[0] != "reconcile-all-apply-fp" {
		t.Fatalf("reconcile bound to %v", fps)
	}
	v := h.pending(t)
	if v.State != PendingClean || !v.Updated {
		t.Fatalf("after a full run: %+v", v)
	}
	// Nothing left: the action reports it instead of re-running.
	if ok, reason := h.s.byName[updateWebsiteName].Available(); ok || reason != msgNothingToUpdate {
		t.Fatalf("available after run: %v %q", ok, reason)
	}
}

func TestUpdateWebsiteMetadataOnlyRunSkipsSync(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), metadataEdited()), nil)
	j := h.runToEnd(t)
	if j.Status != jobs.StatusSucceeded {
		t.Fatalf("status %s %q", j.Status, j.Message)
	}
	if got := stageOutcomes(t, j); got["sync"] != OutcomeSkipped || got["seed"] != OutcomeDone || got["reconcile"] != OutcomeSkipped {
		t.Fatalf("outcomes %v", got)
	}
	if h.stages["sync"].runs.Load() != 0 {
		t.Fatal("sync ran for a metadata-only change")
	}
	if v := h.pending(t); v.State != PendingClean {
		t.Fatalf("after run %+v", v)
	}
}

func TestExportAndRefreshAloneNeverMakePendingClean(t *testing.T) {
	w := newWorld(baseCatalog(), metadataEdited())
	h := newHarness(t, w, nil)
	ctx := context.Background()
	cur := w.fingerprint()
	for _, stage := range checkpointStages {
		if err := recordCheckpoint(ctx, w.prov, stage, cur.Digest); err != nil {
			t.Fatal(err)
		}
	}
	v := h.pending(t)
	if v.State == PendingClean || v.Updated {
		t.Fatalf("export/snapshot/release checkpoints alone made the catalog clean: %+v", v)
	}
	if st := stageOf(t, v, PlanSeed); st.Need != NeedRequired {
		t.Fatalf("seed %+v", st)
	}
	if st := stageOf(t, v, PlanExport); st.Need != NeedRequired {
		t.Fatalf("export must rerun after the seed: %+v", st)
	}
}

func TestUpdateWebsiteAbortsBeforePRWhenCatalogEditedMidRun(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	pub := h.stages["release-publish"]
	pub.runFn = func(ctx context.Context) (jobs.Outcome, error) {
		out, err := pub.effect(ctx)
		h.w.edit(func(c *catalog.EditorialCatalog) { c.Artists[1].Name = "Edited by hand" })
		return out, err
	}
	j := h.runToEnd(t)
	if j.Status != jobs.StatusFailed {
		t.Fatalf("status %s", j.Status)
	}
	if h.stages["release-pr"].runs.Load() != 0 || h.w.pushed.Load() != 0 {
		t.Fatal("release-pr ran after an external edit")
	}
	got := stageOutcomes(t, j)
	if got["release-publish"] != OutcomeDone || got["pr"] != OutcomeFailed || got["deploy"] != OutcomeNotRun {
		t.Fatalf("outcomes %v", got)
	}
	if !strings.Contains(j.Message, "The catalog changed outside the admin") {
		t.Fatalf("message %q", j.Message)
	}
}

func TestUpdateWebsiteCancelAfterReconcileStopsBeforeTheNextStage(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	rec := h.stages["reconcile-all-apply"]
	reached := make(chan struct{})
	rec.runFn = func(ctx context.Context) (jobs.Outcome, error) {
		out, err := rec.effect(ctx)
		close(reached)
		<-ctx.Done()
		return out, err
	}
	j := h.start(t)
	<-reached
	if err := h.m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	h.m.Wait(j.ID)
	j, _ = h.m.Get(j.ID)
	if j.Status != jobs.StatusCancelled {
		t.Fatalf("status %s", j.Status)
	}
	got := stageOutcomes(t, j)
	if got["reconcile"] != OutcomeDone {
		t.Fatalf("reconcile %v", got)
	}
	for _, n := range []string{"seed", "sync", "verify", "snapshot", "export", "build", "release-prepare", "release-publish", "pr", "deploy"} {
		if got[n] != OutcomeNotRun {
			t.Fatalf("%s: %s", n, got[n])
		}
	}
	for _, n := range []string{"db-seed", "sync", "export", "release-pr"} {
		if h.stages[n].runs.Load() != 0 {
			t.Fatalf("%s ran after the cancel", n)
		}
	}
	if !strings.Contains(j.Result.Summary, "cancelled") || !j.Resumable {
		t.Fatalf("result %+v resumable %v", j.Result, j.Resumable)
	}
}

func TestUpdateWebsiteFailedStageStopsTheRun(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	h.stages["db-seed"].runFn = func(context.Context) (jobs.Outcome, error) {
		return jobs.Outcome{}, &SafeError{Code: "check_failed", Message: "The seed failed."}
	}
	j := h.runToEnd(t)
	if j.Status != jobs.StatusFailed {
		t.Fatalf("status %s", j.Status)
	}
	got := stageOutcomes(t, j)
	if got["reconcile"] != OutcomeDone || got["seed"] != OutcomeFailed || got["sync"] != OutcomeNotRun || got["pr"] != OutcomeNotRun {
		t.Fatalf("outcomes %v", got)
	}
	if h.stages["sync"].runs.Load() != 0 || h.stages["release-publish"].runs.Load() != 0 {
		t.Fatal("stages ran after a failure")
	}
	if !strings.Contains(j.Message, "The seed failed.") || !strings.Contains(j.Message, "Completed: validate, reconcile") {
		t.Fatalf("message %q", j.Message)
	}
	if v := h.pending(t); v.State == PendingClean {
		t.Fatal("a failed run left the catalog clean")
	}
}

func TestUpdateWebsitePartialStageIsNeverSuccess(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	h.stages["sync"].runFn = func(context.Context) (jobs.Outcome, error) {
		return jobs.Outcome{Status: jobs.StatusPartial, Resumable: true, Result: jobs.Result{Summary: "Spotify rate limit reached; retry later."}}, nil
	}
	j := h.runToEnd(t)
	if j.Status != jobs.StatusPartial || !j.Resumable {
		t.Fatalf("status %s resumable %v", j.Status, j.Resumable)
	}
	if got := stageOutcomes(t, j); got["sync"] != OutcomeFailed || got["verify"] != OutcomeNotRun {
		t.Fatalf("outcomes %v", got)
	}
}

func TestUpdateWebsiteRestartSkipsOnlyStagesWhoseDigestsMatch(t *testing.T) {
	for _, tc := range []struct {
		name         string
		changeDB     bool
		snapshotRuns int32
	}{
		{name: "digests match", snapshotRuns: 1},
		{name: "database changed meanwhile", changeDB: true, snapshotRuns: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(baseCatalog(), idEdited())
			first := newHarness(t, w, nil)
			first.stages["export"].runFn = func(context.Context) (jobs.Outcome, error) {
				return jobs.Outcome{}, &SafeError{Code: "write_failed", Message: "The export could not be published completely; run export again."}
			}
			j := first.runToEnd(t)
			if j.Status != jobs.StatusFailed {
				t.Fatalf("first run %s", j.Status)
			}
			if tc.changeDB {
				w.changeDB()
			}

			// A new process: a new service and job manager over the same database.
			second := newHarness(t, w, nil)
			second.stages["db-snapshot"].runs.Store(first.stages["db-snapshot"].runs.Load())
			j = second.runToEnd(t)
			if j.Status != jobs.StatusSucceeded {
				t.Fatalf("second run %s %q", j.Status, j.Message)
			}
			got := stageOutcomes(t, j)
			for _, n := range []string{"reconcile", "seed", "sync"} {
				if got[n] != OutcomeSkipped {
					t.Fatalf("%s re-ran: %v", n, got)
				}
			}
			if second.stages["sync"].runs.Load() != 0 || second.stages["reconcile-all-apply"].runs.Load() != 0 {
				t.Fatal("database stages re-ran")
			}
			if n := second.stages["db-snapshot"].runs.Load(); n != tc.snapshotRuns {
				t.Fatalf("snapshot ran %d times in total, want %d", n, tc.snapshotRuns)
			}
			if got["export"] != OutcomeDone || got["pr"] != OutcomeDone {
				t.Fatalf("outcomes %v", got)
			}
			if v := second.pending(t); v.State != PendingClean {
				t.Fatalf("after resume %+v", v)
			}
		})
	}
}

func TestUpdateWebsiteHoldsTheCatalogAgainstAdminEdits(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	rec := h.stages["reconcile-all-apply"]
	reached, release := make(chan struct{}), make(chan struct{})
	rec.runFn = func(ctx context.Context) (jobs.Outcome, error) {
		close(reached)
		<-release
		return rec.effect(ctx)
	}
	j := h.start(t)
	<-reached
	_, err := h.m.Acquire(ResourceCatalogYAML)
	var ce *jobs.ConflictError
	if !errors.Is(err, jobs.ErrConflict) || !errors.As(err, &ce) || ce.HolderID != j.ID {
		t.Fatalf("catalog edit during a run: %v", err)
	}
	close(release)
	h.m.Wait(j.ID)
	rel, err := h.m.Acquire(ResourceCatalogYAML)
	if err != nil {
		t.Fatalf("edit after the run: %v", err)
	}
	rel()
}

func TestUpdateWebsiteResultsAreFreeOfPaths(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	h.stages["db-snapshot"].runFn = func(context.Context) (jobs.Outcome, error) {
		return jobs.Outcome{}, errors.New("open /Users/someone/repo/data/catalog.db: denied")
	}
	j := h.runToEnd(t)
	text := j.Message + j.Result.Summary
	for _, r := range j.Result.Report.Rows {
		text += strings.Join(r, " ")
	}
	if strings.Contains(text, "/Users") || strings.Contains(text, "catalog.db") {
		t.Fatalf("path leaked: %q", text)
	}
}

func TestUpdateWebsiteDatabaseWriteBetweenStagesIsNotSuccess(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	exp := h.stages["export"]
	exp.runFn = func(ctx context.Context) (jobs.Outcome, error) {
		h.w.changeDB() // another process writes between snapshot and export
		return exp.effect(ctx)
	}
	j := h.runToEnd(t)
	if j.Status == jobs.StatusSucceeded {
		t.Fatalf("a run whose snapshot went stale succeeded: %+v", j.Result)
	}
	if h.w.pushed.Load() != 0 {
		t.Fatal("the pull request was opened for a stale snapshot")
	}
	if !strings.Contains(j.Message, "Write the database snapshot") {
		t.Fatalf("message %q", j.Message)
	}
}

func TestUpdateWebsiteUnrecordedCheckpointIsNotSuccess(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	h.stages["release-pr"].runFn = func(context.Context) (jobs.Outcome, error) {
		// The pull request opened but its checkpoint could not be recorded.
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: "Opened." + provenanceWarning}}, nil
	}
	j := h.runToEnd(t)
	if j.Status == jobs.StatusSucceeded {
		t.Fatalf("succeeded without the pr checkpoint: %+v", j.Result)
	}
	if !strings.Contains(j.Message, "Open the catalog pull request") {
		t.Fatalf("message %q", j.Message)
	}
}

func TestUpdateWebsiteConvergesForAnArtistRemovedWithoutIDs(t *testing.T) {
	applied := provCatalog(provArtist("gza", "id-gza"), catalog.Artist{Slug: "old", Name: "old", Enabled: true})
	h := newHarness(t, newWorld(applied, provCatalog(provArtist("gza", "id-gza"))), nil)
	if st := stageOf(t, h.pending(t), PlanReconcile); st.Need != NeedNotNeeded {
		t.Fatalf("reconcile for a removal without IDs: %+v", st)
	}
	j := h.runToEnd(t)
	if j.Status != jobs.StatusSucceeded {
		t.Fatalf("status %s %q", j.Status, j.Message)
	}
	if v := h.pending(t); v.State != PendingClean {
		t.Fatalf("after one run %+v", v)
	}
}

func TestUpdateWebsiteCatalogEditedDuringAStagePreviewAborts(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	h.stages["release-pr"].onPreview = func() {
		h.w.edit(func(c *catalog.EditorialCatalog) { c.Artists[1].Name = "Edited during the preview" })
	}
	j := h.runToEnd(t)
	if j.Status != jobs.StatusFailed {
		t.Fatalf("status %s", j.Status)
	}
	if h.stages["release-pr"].runs.Load() != 0 || h.w.pushed.Load() != 0 {
		t.Fatal("the pull request ran after the catalog changed")
	}
	if !strings.Contains(j.Message, "The catalog changed outside the admin") {
		t.Fatalf("message %q", j.Message)
	}
}

func TestUpdateWebsitePassesTheConfirmedDigestToEveryStage(t *testing.T) {
	w := newWorld(baseCatalog(), idEdited())
	h := newHarness(t, w, nil)
	var got string
	pr := h.stages["release-pr"]
	pr.runFn = func(ctx context.Context) (jobs.Outcome, error) {
		got, _ = ConfirmedCatalogDigest(ctx)
		return pr.effect(ctx)
	}
	if j := h.runToEnd(t); j.Status != jobs.StatusSucceeded {
		t.Fatalf("status %s %q", j.Status, j.Message)
	}
	if got != w.fingerprint().Digest {
		t.Fatalf("release-pr saw confirmed digest %q", got)
	}
}
