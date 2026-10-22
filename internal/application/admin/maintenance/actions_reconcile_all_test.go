package maintenance

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// bulkDB is a per-artist in-memory artists.BulkReconcileStore whose
// ApplyReconciliations is all-or-nothing, as the SQLite transaction.
type bulkDB struct {
	log          *callLog
	associations map[string][]artists.Association
	impact       map[string]artists.ImpactState
	backups      []artists.BackupRequest
	batches      int
	cleared      int
	// failSlug makes the transaction fail on that artist.
	failSlug string
	// onApply runs while the transaction applies the artist.
	onApply func(slug string)
}

func (s *bulkDB) ActiveAssociations(_ context.Context, slug string) ([]artists.Association, error) {
	return slices.Clone(s.associations[slug]), nil
}

func (s *bulkDB) Impact(_ context.Context, slug string) (artists.ImpactState, error) {
	return s.impact[slug], nil
}

func (s *bulkDB) PendingReconciliations(context.Context) ([]artists.PendingReconciliation, error) {
	return nil, nil
}

func (s *bulkDB) BackupDatabase(_ context.Context, r artists.BackupRequest) (artists.BackupInfo, error) {
	s.log.add("backup")
	s.backups = append(s.backups, r)
	return artists.BackupInfo{Path: "/secret/backups/" + r.Name + ".db", SHA256: "sha"}, nil
}

func (s *bulkDB) ApplyReconciliation(context.Context, artists.ApplyRequest) error {
	return errors.New("bulk apply never applies one artist at a time")
}

func (s *bulkDB) ApplyReconciliations(ctx context.Context, reqs []artists.ApplyRequest) error {
	s.log.add("apply-all")
	work := maps.Clone(s.associations)
	for _, r := range reqs {
		if s.onApply != nil {
			s.onApply(r.Slug)
		}
		if err := ctx.Err(); err != nil {
			return &artists.BulkApplyError{Slug: r.Slug, Err: err}
		}
		if r.Slug == s.failSlug {
			return &artists.BulkApplyError{Slug: r.Slug, Err: errors.New("disk I/O error at /secret/catalog.db")}
		}
		retired := append(slices.Clone(r.AlbumIDs), r.TrackIDs...)
		work[r.Slug] = slices.DeleteFunc(slices.Clone(work[r.Slug]), func(a artists.Association) bool {
			return slices.Contains(retired, a.SpotifyID)
		})
	}
	// Commit.
	s.batches++
	s.associations = work
	return nil
}

func (s *bulkDB) RestoreAssociations(context.Context, string, string) (int, int, error) {
	return 0, 0, errors.New("not used")
}

func (s *bulkDB) ClearProvenance(context.Context) error {
	s.log.add("clear-provenance")
	s.cleared++
	return nil
}

// bulkPort drives the real bulk use case over the in-memory stores.
type bulkPort struct {
	fakeReconcilePort
	bulk     artists.ReconcileAll
	planAlls int
	// onPlanAll runs before each PlanAll with its 1-based call number.
	onPlanAll func(n int)
	applyAlls []string
}

func (p *bulkPort) PlanAll(ctx context.Context) (artists.BulkPlan, error) {
	p.planAlls++
	if p.onPlanAll != nil {
		p.onPlanAll(p.planAlls)
	}
	return p.bulk.Plan(ctx, "artists.yaml")
}

func (p *bulkPort) ApplyAll(ctx context.Context, checksum string) ([]artists.ReconcileReport, error) {
	p.applyAlls = append(p.applyAlls, checksum)
	return p.bulk.Apply(ctx, artists.ReconcileAllInput{
		CatalogPath: "artists.yaml", ConfirmChecksum: checksum, BackupDir: "/secret/backups",
		Now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
	})
}

type bulkFixture struct {
	log    *callLog
	cat    *reconcileCatalog
	db     *bulkDB
	port   *bulkPort
	locker *fakeLocker
	prov   *fakeProvenance
	exists bool
	deps   ReconcileDeps
}

// newBulkFixture: gza, meth and rza each carry a homonym association; odb is
// clean. rza has no checkpoint for its current IDs, so it awaits a resync
// once applied.
func newBulkFixture() *bulkFixture {
	log := &callLog{}
	ids := map[string]string{"gza": "G1", "meth": "M1", "odb": "O1", "rza": "R1"}
	db := &bulkDB{log: log,
		associations: map[string][]artists.Association{
			"gza": {
				{Kind: artists.AssociationAlbum, SpotifyID: "al-ok", Name: "Liquid Swords", CreditedArtistIDs: []string{"G1"}},
				{Kind: artists.AssociationAlbum, SpotifyID: "al-bad", Name: "Wrong Album", CreditedArtistIDs: []string{"homonym"}},
			},
			"meth": {{Kind: artists.AssociationTrack, SpotifyID: "tr-meth", Name: "Wrong Track", CreditedArtistIDs: []string{"homonym"}}},
			"odb":  {{Kind: artists.AssociationAlbum, SpotifyID: "al-odb", Name: "Return to the 36 Chambers", CreditedArtistIDs: []string{"O1"}}},
			"rza":  {{Kind: artists.AssociationTrack, SpotifyID: "tr-rza", Name: "Other Track", CreditedArtistIDs: []string{"homonym"}}},
		},
		impact: map[string]artists.ImpactState{},
	}
	var cat []catalog.Artist
	for _, slug := range []string{"odb", "rza", "gza", "meth"} {
		id := ids[slug]
		st := artists.ImpactState{Baseline: artists.MappingBaseline{SpotifyIDs: []string{id}, Source: artists.BaselineSnapshot}}
		if slug != "rza" {
			st.Checkpoints = []artists.Checkpoint{{RunID: 1, Status: "completed", SpotifyIDs: `["` + id + `"]`}}
		}
		db.impact[slug] = st
		cat = append(cat, provArtist(slug, id))
	}
	fx := &bulkFixture{
		log: log, db: db, cat: &reconcileCatalog{c: provCatalog(cat...)},
		locker: &fakeLocker{log: log}, prov: newFakeProvenance(), exists: true,
	}
	fx.prov.applied = fingerprintOf(fx.cat.c)
	fx.port = &bulkPort{
		fakeReconcilePort: fakeReconcilePort{log: log},
		bulk:              artists.NewReconcileAll(fx.cat, db),
	}
	fx.deps = ReconcileDeps{
		Port:           fx.port,
		DatabaseExists: func() bool { return fx.exists },
		Artists:        func(context.Context) ([]string, error) { return slices.Sorted(maps.Keys(ids)), nil },
		Locker:         fx.locker,
		BackupName:     "build/admin/backups",
		Provenance:     fx.prov,
	}
	return fx
}

func rowsFor(rep *jobs.Report, col int, value string) [][]string {
	var out [][]string
	for _, r := range rep.Rows {
		if r[col] == value {
			out = append(out, r)
		}
	}
	return out
}

func outcomes(t *testing.T, rep *jobs.Report) map[string]string {
	t.Helper()
	if rep == nil || !slices.Equal(rep.Columns, []string{"artist", "outcome", "changes", "reason"}) {
		t.Fatalf("report %+v", rep)
	}
	out := map[string]string{}
	for _, r := range rep.Rows {
		out[r[0]] = r[1]
	}
	return out
}

func requireNoPaths(t *testing.T, out jobs.Outcome, err error) {
	t.Helper()
	texts := []string{out.Result.Summary}
	if err != nil {
		texts = append(texts, err.Error())
	}
	if out.Result.Report != nil {
		for _, r := range out.Result.Report.Rows {
			texts = append(texts, r...)
		}
	}
	for _, s := range texts {
		if strings.Contains(s, "/secret") {
			t.Fatalf("a path leaked: %q", s)
		}
	}
}

func TestReconcileAllDryRunReportsOnlyArtistsWithChanges(t *testing.T) {
	fx := newBulkFixture()
	a := NewReconcileAllDryRun(fx.deps)
	if a.Name() != "reconcile-all-dry-run" || a.(Describer).Describe().Destructive {
		t.Fatalf("%s %+v", a.Name(), a.(Describer).Describe())
	}
	pv := previewSync(t, a, Params{})
	plan, _ := fx.port.PlanAll(context.Background())
	if pv.Destructive || pv.RequiresTyped != "" || pv.Fingerprint != plan.Checksum {
		t.Fatalf("preview %+v (checksum %s)", pv, plan.Checksum)
	}

	spec, out, _, err := runAction(t, a, Params{})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	if len(spec.Resources) != 0 || len(fx.log.get()) != 0 {
		t.Fatalf("a dry run holds nothing and writes nothing: %v %v", spec.Resources, fx.log.get())
	}
	rep := out.Result.Report
	if rep == nil || !slices.Equal(rep.Columns, reconcileColumns) || !slices.IsSortedFunc(rep.Rows, slices.Compare[[]string]) {
		t.Fatalf("report %+v", rep)
	}
	if rows := rowsFor(rep, 1, "odb"); len(rows) != 0 {
		t.Fatalf("a clean artist is counted, not listed: %v", rows)
	}
	for _, want := range [][]string{
		{"album", "gza", "al-bad Wrong Album", "active (orphan)", "unlinked"},
		{"track", "meth", "tr-meth Wrong Track", "active (orphan)", "unlinked"},
		{"track", "rza", "tr-rza Other Track", "active (orphan)", "unlinked"},
	} {
		if !slices.ContainsFunc(rep.Rows, func(r []string) bool { return slices.Equal(r, want) }) {
			t.Fatalf("missing row %v in %v", want, rep.Rows)
		}
	}
	for _, slug := range []string{"gza", "meth", "rza"} {
		var summary int
		for _, r := range rowsFor(rep, 1, slug) {
			if r[0] == "summary" {
				summary++
			}
		}
		if summary != 1 {
			t.Fatalf("%s needs one summary row: %v", slug, rep.Rows)
		}
	}
	for _, s := range []string{"3 artists", "1 artist already reconciled", plan.Checksum} {
		if !strings.Contains(out.Result.Summary, s) {
			t.Fatalf("summary %q lacks %q", out.Result.Summary, s)
		}
	}
	if len(fx.db.associations["gza"]) != 2 {
		t.Fatal("nothing retired")
	}
}

func TestReconcileAllRejectsParamsAndNeedsDatabase(t *testing.T) {
	fx := newBulkFixture()
	for _, a := range []Action{NewReconcileAllDryRun(fx.deps), NewReconcileAllApply(fx.deps)} {
		if err := a.Validate(Params{"artist": "gza"}); safeCode(err) != "validation" {
			t.Fatalf("%s takes no params: %v", a.Name(), err)
		}
		if ok, _ := a.Available(); !ok {
			t.Fatalf("%s should be available", a.Name())
		}
	}
	fx.exists = false
	for _, a := range []Action{NewReconcileAllDryRun(fx.deps), NewReconcileAllApply(fx.deps)} {
		if ok, reason := a.Available(); ok || reason != noDatabaseReason {
			t.Fatalf("%s: %v %q", a.Name(), ok, reason)
		}
	}
}

func TestReconcileAllApplyRequiresTypedPhrase(t *testing.T) {
	fx := newBulkFixture()
	a := NewReconcileAllApply(fx.deps)
	if a.Name() != "reconcile-all-apply" || !a.(Describer).Describe().Destructive {
		t.Fatalf("%s %+v", a.Name(), a.(Describer).Describe())
	}
	pv := previewSync(t, a, Params{})
	plan, _ := fx.port.PlanAll(context.Background())
	if !pv.Destructive || pv.RequiresTyped != "reconcile all" || pv.Fingerprint != plan.Checksum {
		t.Fatalf("preview %+v", pv)
	}
	if !scopeHas(pv, "build/admin/backups") || !scopeHas(pv, "gza, meth, rza") {
		t.Fatalf("preview scope %+v", pv.Scope)
	}

	s, m, _ := newTestService(t, a)
	_, tok, err := s.Preview(context.Background(), "reconcile-all-apply", Params{})
	if err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"", "reconcile", "RECONCILE ALL", "reconcile gza"} {
		if _, err := s.Run(context.Background(), "reconcile-all-apply", Params{}, tok, typed); !errors.Is(err, ErrConfirmationRequired) {
			t.Fatalf("typed %q: want ErrConfirmationRequired, got %v", typed, err)
		}
	}
	if len(fx.log.get()) != 0 {
		t.Fatalf("nothing may run without the phrase: %v", fx.log.get())
	}
	j, err := s.Run(context.Background(), "reconcile-all-apply", Params{}, tok, "reconcile all")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	if got, _ := m.Get(j.ID); got.Status != jobs.StatusSucceeded {
		t.Fatalf("job %+v", got)
	}
	if !slices.Equal(fx.port.applyAlls, []string{plan.Checksum}) {
		t.Fatalf("apply must confirm the previewed fingerprint: %v", fx.port.applyAlls)
	}
}

func TestReconcileAllApplyLocksMigratesAndBacksUpOnce(t *testing.T) {
	fx := newBulkFixture()
	spec, out, _, err := runAction(t, NewReconcileAllApply(fx.deps), Params{})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("apply: %+v %v", out, err)
	}
	requireNoPaths(t, out, err)
	if !slices.Equal(spec.Resources, []string{ResourceDatabase}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock", "migrate", "backup", "apply-all", "release"}) {
		t.Fatalf("calls %v", got)
	}
	if len(fx.db.backups) != 1 || fx.db.batches != 1 {
		t.Fatalf("one backup and one transaction: %d %d", len(fx.db.backups), fx.db.batches)
	}
	got := outcomes(t, out.Result.Report)
	if !maps.Equal(got, map[string]string{"gza": "applied", "meth": "applied", "rza": "applied"}) {
		t.Fatalf("outcomes %v", got)
	}
	for _, s := range []string{"Reconciled 3 artists", "build/admin/backups", "all-artists-", "Run a sync of rza"} {
		if !strings.Contains(out.Result.Summary, s) {
			t.Fatalf("summary %q lacks %q", out.Result.Summary, s)
		}
	}
	if len(fx.db.associations["gza"]) != 1 || len(fx.db.associations["odb"]) != 1 {
		t.Fatalf("associations %v", fx.db.associations)
	}

	// Provenance: gza and meth are applied; rza awaits its resync.
	if fx.prov.appliedN != 1 {
		t.Fatalf("one merged record, got %d", fx.prov.appliedN)
	}
	if cp, ok := fx.prov.checkpoints[artists.StageReconcile]; !ok || cp.CatalogDigest != fx.prov.applied.Digest {
		t.Fatalf("reconcile checkpoint %+v", cp)
	}
	if fx.db.cleared != 0 {
		t.Fatal("a committed run keeps provenance")
	}
}

func TestReconcileAllApplyRefusesStaleFingerprint(t *testing.T) {
	stale := func(fx *bulkFixture) {
		fx.db.associations["odb"] = append(fx.db.associations["odb"],
			artists.Association{Kind: artists.AssociationAlbum, SpotifyID: "al-new", CreditedArtistIDs: []string{"homonym"}})
	}

	// Changed between the preview and Spec.
	fx := newBulkFixture()
	a := NewReconcileAllApply(fx.deps)
	ctx := confirmedCtx(t, a, Params{})
	stale(fx)
	if _, err := a.Spec(ctx, Params{}); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("want stale preview, got %v", err)
	}

	// Changed once the lock is held.
	fx = newBulkFixture()
	a = NewReconcileAllApply(fx.deps)
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	fx.locker.onLock = func() { stale(fx) }
	_, err = spec.Run(context.Background(), &recReporter{})
	if safeCode(err) != "stale_preview" {
		t.Fatalf("want stale_preview, got %v", err)
	}
	if len(fx.db.backups) != 0 || fx.db.batches != 0 || len(fx.port.applyAlls) != 0 {
		t.Fatal("a stale run backs up and applies nothing")
	}

	// Spec without a verified fingerprint fails closed.
	if _, err := NewReconcileAllApply(newBulkFixture().deps).Spec(context.Background(), Params{}); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("want ErrConfirmationRequired, got %v", err)
	}
}

func TestReconcileAllApplyFailureRollsBackEveryArtist(t *testing.T) {
	fx := newBulkFixture()
	fx.db.failSlug = "meth"
	before := maps.Clone(fx.db.associations)
	prevApplied := *fx.prov.applied

	_, out, _, err := runAction(t, NewReconcileAllApply(fx.deps), Params{})
	if err == nil {
		t.Fatalf("a failed run is not a success: %+v", out)
	}
	if safeCode(err) != "reconcile_failed" {
		t.Fatalf("code %q (%v)", safeCode(err), err)
	}
	requireNoPaths(t, out, err)
	got := outcomes(t, out.Result.Report)
	if !maps.Equal(got, map[string]string{"gza": "rolled_back", "meth": "failed", "rza": "not_attempted"}) {
		t.Fatalf("outcomes %v", got)
	}
	for _, r := range out.Result.Report.Rows {
		if r[3] == "" {
			t.Fatalf("every outcome carries a reason: %v", r)
		}
	}
	if !reflect.DeepEqual(before, fx.db.associations) {
		t.Fatal("the database must equal its pre-run state")
	}
	if len(fx.db.backups) != 1 || fx.db.cleared != 1 {
		t.Fatalf("one backup, provenance cleared: %d %d", len(fx.db.backups), fx.db.cleared)
	}
	if fx.prov.appliedN != 0 || fx.prov.applied.Digest != prevApplied.Digest || len(fx.prov.checkpoints) != 0 {
		t.Fatal("a rolled back run records nothing")
	}
	if !strings.Contains(out.Result.Summary, "rolled back") || !strings.Contains(err.Error(), "meth") {
		t.Fatalf("summary %q, err %v", out.Result.Summary, err)
	}
	if calls := fx.log.get(); calls[len(calls)-1] != "release" {
		t.Fatalf("the lock is released: %v", fx.log.get())
	}
}

func TestReconcileAllApplyCancelledMidRunRollsBack(t *testing.T) {
	fx := newBulkFixture()
	before := maps.Clone(fx.db.associations)
	a := NewReconcileAllApply(fx.deps)
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.db.onApply = func(slug string) {
		if slug == "meth" {
			cancel()
		}
	}
	out, err := spec.Run(ctx, &recReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled run ends cancelled, not succeeded: %+v %v", out, err)
	}
	got := outcomes(t, out.Result.Report)
	if !maps.Equal(got, map[string]string{"gza": "rolled_back", "meth": "rolled_back", "rza": "not_attempted"}) {
		t.Fatalf("outcomes %v", got)
	}
	if !reflect.DeepEqual(before, fx.db.associations) {
		t.Fatal("the database must equal its pre-run state")
	}
	if fx.db.cleared != 1 || fx.prov.appliedN != 0 {
		t.Fatalf("cleared %d applied %d", fx.db.cleared, fx.prov.appliedN)
	}
	if !strings.Contains(out.Result.Summary, "cancelled") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestReconcileAllApplyCleanPlanIsNoOp(t *testing.T) {
	fx := newBulkFixture()
	for _, slug := range []string{"gza", "meth", "rza"} {
		delete(fx.db.associations, slug)
	}
	fx.db.impact["rza"] = artists.ImpactState{
		Baseline:    artists.MappingBaseline{SpotifyIDs: []string{"R1"}, Source: artists.BaselineSnapshot},
		Checkpoints: []artists.Checkpoint{{RunID: 1, Status: "completed", SpotifyIDs: `["R1"]`}},
	}
	a := NewReconcileAllApply(fx.deps)
	pv := previewSync(t, a, Params{})
	if !slices.ContainsFunc(pv.Warnings, func(w string) bool { return strings.Contains(w, "Nothing to reconcile") }) {
		t.Fatalf("warnings %v", pv.Warnings)
	}
	_, out, _, err := runAction(t, a, Params{})
	if err != nil || out.Status != jobs.StatusSucceeded || !strings.Contains(out.Result.Summary, "Nothing to reconcile") {
		t.Fatalf("clean: %+v %v", out, err)
	}
	if len(fx.db.backups) != 0 || fx.db.batches != 0 || len(fx.port.applyAlls) != 0 || fx.db.cleared != 0 {
		t.Fatal("a clean plan takes no backup and changes nothing")
	}
	// As a no-op reconcile-apply, the clean plans vouch for the artists'
	// Spotify state.
	if fx.prov.appliedN != 1 {
		t.Fatalf("applied records %d", fx.prov.appliedN)
	}
}

func TestReconcileAllApplyLockedFailsWithConflict(t *testing.T) {
	fx := newBulkFixture()
	fx.locker.err = ErrLocked
	_, _, _, err := runAction(t, NewReconcileAllApply(fx.deps), Params{})
	if safeCode(err) != "conflict" {
		t.Fatalf("want conflict, got %v", err)
	}
	if len(fx.db.backups) != 0 || fx.db.batches != 0 {
		t.Fatal("nothing runs without the lock")
	}
}
