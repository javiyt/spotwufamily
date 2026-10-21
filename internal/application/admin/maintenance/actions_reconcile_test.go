package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	gzaCurrentID = "gzaCurrentProfile00001"
	gzaOldID     = "gzaHomonymProfile00001"
)

// reconcileCatalog is an in-memory artists.CatalogStore; Save is never used.
type reconcileCatalog struct{ c catalog.EditorialCatalog }

func (s *reconcileCatalog) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return s.c, nil
}

func (s *reconcileCatalog) Save(context.Context, string, catalog.EditorialCatalog) error {
	return errors.New("the reconcile use case never saves the catalog")
}

// reconcileDB is an in-memory artists.ReconcileStore that logs the writes.
type reconcileDB struct {
	log          *callLog
	associations []artists.Association
	impact       artists.ImpactState
	backupErr    error
	applyErr     error
	backups      []artists.BackupRequest
	applied      []artists.ApplyRequest
}

func (s *reconcileDB) ActiveAssociations(context.Context, string) ([]artists.Association, error) {
	return slices.Clone(s.associations), nil
}

func (s *reconcileDB) Impact(context.Context, string) (artists.ImpactState, error) {
	return s.impact, nil
}

func (s *reconcileDB) PendingReconciliations(context.Context) ([]artists.PendingReconciliation, error) {
	return nil, nil
}

func (s *reconcileDB) BackupDatabase(_ context.Context, r artists.BackupRequest) (artists.BackupInfo, error) {
	s.log.add("backup")
	if s.backupErr != nil {
		return artists.BackupInfo{}, s.backupErr
	}
	s.backups = append(s.backups, r)
	return artists.BackupInfo{Path: "/secret/backups/" + r.Name + ".db", SHA256: "sha"}, nil
}

// ApplyReconciliation retires the requested associations, as the SQLite
// adapter does in one transaction.
func (s *reconcileDB) ApplyReconciliation(_ context.Context, r artists.ApplyRequest) error {
	s.log.add("apply")
	if s.applyErr != nil {
		return s.applyErr
	}
	s.applied = append(s.applied, r)
	retired := append(slices.Clone(r.AlbumIDs), r.TrackIDs...)
	s.associations = slices.DeleteFunc(s.associations, func(a artists.Association) bool {
		return slices.Contains(retired, a.SpotifyID)
	})
	s.impact.Baseline = artists.MappingBaseline{SpotifyIDs: r.TargetIDs, Source: artists.BaselineSnapshot}
	return nil
}

func (s *reconcileDB) RestoreAssociations(context.Context, string, string) (int, int, error) {
	return 0, 0, errors.New("restore is not exposed")
}

// fakeReconcilePort drives the real reconcile use case over the in-memory
// stores, so the backup-before-apply order and the checksum checks are the
// use case's own.
type fakeReconcilePort struct {
	log     *callLog
	use     artists.ReconcileArtist
	migErr  error
	onMig   func()
	applies []string // confirm checksums
	plans   int
	// onPlan runs before each Plan with its 1-based call number.
	onPlan func(n int)
	// applyResult, when set, replaces the use case's Apply.
	applyResult func() (artists.ReconcileReport, error)
}

func (p *fakeReconcilePort) Plan(ctx context.Context, slug string) (artists.ReconcileReport, error) {
	p.plans++
	if p.onPlan != nil {
		p.onPlan(p.plans)
	}
	return p.use.Run(ctx, artists.ReconcileInput{Slug: slug})
}

func (p *fakeReconcilePort) Migrate(context.Context) error {
	p.log.add("migrate")
	if p.onMig != nil {
		p.onMig()
	}
	return p.migErr
}

func (p *fakeReconcilePort) Apply(ctx context.Context, slug, checksum string) (artists.ReconcileReport, error) {
	p.applies = append(p.applies, checksum)
	if p.applyResult != nil {
		return p.applyResult()
	}
	return p.use.Run(ctx, artists.ReconcileInput{
		Slug: slug, Apply: true, ConfirmChecksum: checksum, BackupDir: "/secret/backups",
		Now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	})
}

type reconcileFixture struct {
	log    *callLog
	cat    *reconcileCatalog
	db     *reconcileDB
	port   *fakeReconcilePort
	locker *fakeLocker
	exists bool
	deps   ReconcileDeps
}

func newReconcileFixture() *reconcileFixture {
	log := &callLog{}
	fx := &reconcileFixture{
		log: log,
		cat: &reconcileCatalog{c: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
			{Slug: "gza", Name: "GZA", SpotifyID: gzaCurrentID, Enabled: true},
			{Slug: "rza", Name: "RZA", Enabled: true},
		}}},
		db: &reconcileDB{
			log: log,
			associations: []artists.Association{
				{Kind: artists.AssociationAlbum, SpotifyID: "al-ok", Name: "Liquid Swords", CreditedArtistIDs: []string{gzaCurrentID}},
				{Kind: artists.AssociationAlbum, SpotifyID: "al-bad", Name: "Wrong Album", CreditedArtistIDs: []string{gzaOldID}},
				{Kind: artists.AssociationTrack, SpotifyID: "tr-shared", Name: "Shared Song", CreditedArtistIDs: []string{gzaOldID}, SharedWith: []string{"rza"}},
				{Kind: artists.AssociationTrack, SpotifyID: "tr-none", Name: "No Credits"},
			},
			impact: artists.ImpactState{Baseline: artists.MappingBaseline{SpotifyIDs: []string{gzaOldID}, Source: artists.BaselineSnapshot}},
		},
		locker: &fakeLocker{log: log},
		exists: true,
	}
	fx.port = &fakeReconcilePort{log: log, use: artists.NewReconcileArtist(fx.cat, fx.db)}
	fx.deps = ReconcileDeps{
		Port:           fx.port,
		DatabaseExists: func() bool { return fx.exists },
		Artists: func(context.Context) ([]string, error) {
			var slugs []string
			for _, a := range fx.cat.c.Artists {
				slugs = append(slugs, a.Slug)
			}
			return slugs, nil
		},
		Locker:     fx.locker,
		BackupName: "build/admin/backups",
	}
	return fx
}

func TestReconcileDryRunWritesNothing(t *testing.T) {
	fx := newReconcileFixture()
	a := NewReconcileDryRun(fx.deps)
	if a.Name() != "reconcile-dry-run" {
		t.Fatalf("name %q", a.Name())
	}
	p := Params{"artist": "gza"}
	pv := previewSync(t, a, p)
	if pv.Destructive || pv.RequiresTyped != "" || pv.UsesNetwork {
		t.Fatalf("a dry run is read-only: %+v", pv)
	}
	plan, _ := fx.port.Plan(context.Background(), "gza")
	if pv.Fingerprint != plan.PlanChecksum || !scopeHas(pv, plan.PlanChecksum) {
		t.Fatalf("preview should show and use the plan checksum: %+v", pv)
	}

	spec, out, r, err := runAction(t, a, p)
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	if len(spec.Resources) != 0 {
		t.Fatalf("a dry run holds no resources: %v", spec.Resources)
	}
	if len(fx.log.get()) != 0 || len(fx.db.backups) != 0 || len(fx.db.applied) != 0 || len(fx.port.applies) != 0 {
		t.Fatalf("a dry run must not lock, migrate, back up or apply: %v", fx.log.get())
	}
	if len(fx.db.associations) != 4 {
		t.Fatal("nothing retired")
	}
	rep := out.Result.Report
	if rep == nil || !slices.Equal(rep.Columns, []string{"kind", "artist", "item", "from", "to"}) {
		t.Fatalf("report %+v", rep)
	}
	if !slices.IsSortedFunc(rep.Rows, slices.Compare[[]string]) {
		t.Fatalf("rows not sorted: %v", rep.Rows)
	}
	wantRows := [][]string{
		{"album", "gza", "al-bad Wrong Album", "active (orphan)", "unlinked"},
		{"album", "gza", "al-ok Liquid Swords", "active (supported)", "active"},
		{"spotify_id", "gza", gzaCurrentID, "unlinked", "linked"},
		{"spotify_id", "gza", gzaOldID, "linked", "unlinked"},
		{"track", "gza", "tr-none No Credits", "active (unverifiable)", "active"},
		{"track", "gza", "tr-shared Shared Song", "active (orphan)", "unlinked (track kept: shared with rza)"},
	}
	if !slices.EqualFunc(rep.Rows, wantRows, slices.Equal[[]string]) {
		t.Fatalf("rows:\n%v\nwant\n%v", rep.Rows, wantRows)
	}
	for _, s := range []string{"Dry run", "1 album", "1 track", "would be unlinked", plan.PlanChecksum} {
		if !strings.Contains(out.Result.Summary, s) {
			t.Fatalf("summary %q lacks %q", out.Result.Summary, s)
		}
	}
	if r.counts["unlinked"] != 2 {
		t.Fatalf("counts %v", r.counts)
	}
}

func TestReconcileDryRunIsDeterministic(t *testing.T) {
	fx := newReconcileFixture()
	a := NewReconcileDryRun(fx.deps)
	_, first, _, err := runAction(t, a, Params{"artist": "gza"})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(fx.db.associations)
	_, second, _, err := runAction(t, a, Params{"artist": "gza"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Result.Summary != second.Result.Summary ||
		!slices.EqualFunc(first.Result.Report.Rows, second.Result.Report.Rows, slices.Equal[[]string]) {
		t.Fatal("the dry run must not depend on store order")
	}
}

func TestReconcileApplyRequiresTypedPhrase(t *testing.T) {
	fx := newReconcileFixture()
	a := NewReconcileApply(fx.deps)
	if a.Name() != "reconcile-apply" {
		t.Fatalf("name %q", a.Name())
	}
	p := Params{"artist": "gza"}
	pv := previewSync(t, a, p)
	plan, _ := fx.port.Plan(context.Background(), "gza")
	if !pv.Destructive || pv.RequiresTyped != "reconcile gza" || pv.Fingerprint != plan.PlanChecksum {
		t.Fatalf("preview %+v (checksum %s)", pv, plan.PlanChecksum)
	}
	if !scopeHas(pv, "build/admin/backups") {
		t.Fatalf("preview should name the backup directory: %+v", pv.Scope)
	}

	s, m, _ := newTestService(t, a)
	_, tok, err := s.Preview(context.Background(), "reconcile-apply", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"", "reconcile rza", "RECONCILE GZA"} {
		if _, err := s.Run(context.Background(), "reconcile-apply", p, tok, typed); !errors.Is(err, ErrConfirmationRequired) {
			t.Fatalf("typed %q: want ErrConfirmationRequired, got %v", typed, err)
		}
	}
	if len(fx.port.applies) != 0 || len(fx.log.get()) != 0 {
		t.Fatalf("nothing may run without the phrase: %v", fx.log.get())
	}
	j, err := s.Run(context.Background(), "reconcile-apply", p, tok, "reconcile gza")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	if got, _ := m.Get(j.ID); got.Status != jobs.StatusSucceeded {
		t.Fatalf("job %+v", got)
	}
	if !slices.Equal(fx.port.applies, []string{plan.PlanChecksum}) {
		t.Fatalf("apply must confirm the previewed checksum: %v", fx.port.applies)
	}
}

func TestReconcileApplyTakesBackupFirst(t *testing.T) {
	fx := newReconcileFixture()
	spec, out, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("apply: %+v %v", out, err)
	}
	if !slices.Equal(spec.Resources, []string{ResourceDatabase}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	if want := []string{"lock", "migrate", "backup", "apply", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v, want %v", fx.log.get(), want)
	}
	if len(fx.db.backups) != 1 || !strings.Contains(string(fx.db.backups[0].Manifest), `"plan_checksum"`) {
		t.Fatalf("one backup with the plan manifest: %+v", fx.db.backups)
	}
	req := fx.db.applied[0]
	if !slices.Equal(req.AlbumIDs, []string{"al-bad"}) || !slices.Equal(req.TrackIDs, []string{"tr-shared"}) {
		t.Fatalf("apply request %+v", req)
	}
	for _, s := range []string{"Applied", "1 album", "1 track", "unlinked", "gza-20261020T120000Z.db"} {
		if !strings.Contains(out.Result.Summary, s) {
			t.Fatalf("summary %q lacks %q", out.Result.Summary, s)
		}
	}
	if strings.Contains(out.Result.Summary, "/secret") {
		t.Fatalf("summary leaks the backup path: %q", out.Result.Summary)
	}

	failing := newReconcileFixture()
	failing.db.backupErr = errors.New("vacuum into /secret/backups: disk full")
	_, _, _, err = runAction(t, NewReconcileApply(failing.deps), Params{"artist": "gza"})
	if safeCode(err) != "internal" || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("backup failure: %v", err)
	}
	if want := []string{"lock", "migrate", "backup", "release"}; !slices.Equal(failing.log.get(), want) {
		t.Fatalf("nothing applied after a failed backup: %v", failing.log.get())
	}
}

func TestReconcileApplyRefusedIfPlanChanged(t *testing.T) {
	fx := newReconcileFixture()
	a := NewReconcileApply(fx.deps)
	spec, err := specFor(t, a, Params{"artist": "gza"})
	if err != nil {
		t.Fatal(err)
	}
	// A sync lands while the job is queued: a new orphan album appears.
	fx.db.associations = append(fx.db.associations, artists.Association{
		Kind: artists.AssociationAlbum, SpotifyID: "al-new", Name: "New", CreditedArtistIDs: []string{gzaOldID},
	})
	_, err = spec.Run(context.Background(), &recReporter{})
	if safeCode(err) != "stale_preview" {
		t.Fatalf("want stale preview, got %v", err)
	}
	if want := []string{"lock", "migrate", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
	if len(fx.port.applies) != 0 || len(fx.db.backups) != 0 || len(fx.db.applied) != 0 {
		t.Fatal("nothing may be backed up or applied for a stale preview")
	}
}

func TestReconcileApplyCancelledBeforeApply(t *testing.T) {
	fx := newReconcileFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.port.onMig = cancel
	spec, err := specFor(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = spec.Run(ctx, &recReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if want := []string{"lock", "migrate", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
	if len(fx.port.applies) != 0 {
		t.Fatal("a cancelled job must not apply")
	}
}

func TestReconcileApplyLockedFailsWithConflict(t *testing.T) {
	fx := newReconcileFixture()
	fx.locker.err = fmt.Errorf("lock /secret/catalog.db.lock: %w", ErrLocked)
	_, _, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if safeCode(err) != "conflict" || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("want safe conflict, got %v", err)
	}
	if want := []string{"lock"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
}

func TestReconcileApplyReleasesLockOnFailures(t *testing.T) {
	migrate := newReconcileFixture()
	migrate.port.migErr = errors.New("migrate /secret/catalog.db: locked")
	_, _, _, err := runAction(t, NewReconcileApply(migrate.deps), Params{"artist": "gza"})
	if safeCode(err) != "internal" || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("migrate failure: %v", err)
	}
	if want := []string{"lock", "migrate", "release"}; !slices.Equal(migrate.log.get(), want) {
		t.Fatalf("calls = %v", migrate.log.get())
	}

	apply := newReconcileFixture()
	apply.db.applyErr = errors.New("sqlite: constraint failed")
	_, _, _, err = runAction(t, NewReconcileApply(apply.deps), Params{"artist": "gza"})
	if safeCode(err) != "internal" {
		t.Fatalf("apply failure: %v", err)
	}
	if want := []string{"lock", "migrate", "backup", "apply", "release"}; !slices.Equal(apply.log.get(), want) {
		t.Fatalf("calls = %v", apply.log.get())
	}

	gone := newReconcileFixture()
	spec, err := specFor(t, NewReconcileApply(gone.deps), Params{"artist": "gza"})
	if err != nil {
		t.Fatal(err)
	}
	gone.exists = false
	_, err = spec.Run(context.Background(), &recReporter{})
	if safeCode(err) != "unavailable" {
		t.Fatalf("database removed while queued: %v", err)
	}
	if want := []string{"lock", "release"}; !slices.Equal(gone.log.get(), want) {
		t.Fatalf("calls = %v", gone.log.get())
	}
}

func TestReconcileApplyCleanPlanIsNoOp(t *testing.T) {
	fx := newReconcileFixture()
	if _, _, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"}); err != nil {
		t.Fatal(err)
	}
	fx.log = &callLog{}
	fx.db.log, fx.port.log, fx.locker.log = fx.log, fx.log, fx.log

	pv := previewSync(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if !strings.Contains(strings.Join(pv.Warnings, " "), "Nothing to apply") {
		t.Fatalf("a clean plan should say so: %+v", pv.Warnings)
	}
	_, out, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("no-op apply: %+v %v", out, err)
	}
	if want := []string{"lock", "migrate", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("a clean plan takes no backup and applies nothing: %v", fx.log.get())
	}
	if !strings.Contains(out.Result.Summary, "Nothing to apply") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestReconcileRejectsUnknownOrInvalidArtist(t *testing.T) {
	fx := newReconcileFixture()
	for _, a := range []Action{NewReconcileDryRun(fx.deps), NewReconcileApply(fx.deps)} {
		for _, p := range []Params{{}, {"artist": ""}, {"artist": "Not A Slug"}, {"artist": "gza", "restore": "x"}} {
			if err := a.Validate(p); safeCode(err) != "validation" {
				t.Fatalf("%s Validate(%v): %v", a.Name(), p, err)
			}
		}
		p := Params{"artist": "cappadonna"}
		if err := a.Validate(p); err != nil {
			t.Fatalf("well-formed slug passes static validation: %v", err)
		}
		_, err := a.Preview(context.Background(), p)
		var se *SafeError
		if !errors.As(err, &se) || se.Code != "validation" || !strings.Contains(se.Message, `"cappadonna"`) {
			t.Fatalf("%s: want validation error naming the slug, got %v", a.Name(), err)
		}
		if _, err := specFor(t, a, p); safeCode(err) != "validation" {
			t.Fatalf("%s spec: %v", a.Name(), err)
		}
	}
	if len(fx.log.get()) != 0 {
		t.Fatalf("nothing should run: %v", fx.log.get())
	}
}

func TestReconcileUnavailableWithoutDatabase(t *testing.T) {
	fx := newReconcileFixture()
	fx.exists = false
	for _, a := range []Action{NewReconcileDryRun(fx.deps), NewReconcileApply(fx.deps)} {
		if ok, reason := a.Available(); ok || reason != "No database found; run the init workflow first" {
			t.Fatalf("%s available=%v reason=%q", a.Name(), ok, reason)
		}
		if _, err := a.Preview(context.Background(), Params{"artist": "gza"}); safeCode(err) != "unavailable" {
			t.Fatalf("%s preview: %v", a.Name(), err)
		}
	}
}

func TestReconcileApplyRefusesPlanChangedBetweenRunAndSpec(t *testing.T) {
	fx := newReconcileFixture()
	a := NewReconcileApply(fx.deps)
	s, m, _ := newTestService(t, a)
	p := Params{"artist": "gza"}
	_, tok, err := s.Preview(context.Background(), "reconcile-apply", p)
	if err != nil {
		t.Fatal(err)
	}
	// Plans: 1 = the user's preview, 2 = Run's re-preview (verified against
	// the token), 3 = Spec. A sync lands between 2 and 3.
	fx.port.onPlan = func(n int) {
		if n == 3 {
			fx.db.associations = append(fx.db.associations, artists.Association{
				Kind: artists.AssociationAlbum, SpotifyID: "al-new", Name: "New", CreditedArtistIDs: []string{gzaOldID},
			})
		}
	}
	_, err = s.Run(context.Background(), "reconcile-apply", p, tok, "reconcile gza")
	if !errors.Is(err, ErrStalePreview) {
		t.Fatalf("want ErrStalePreview, got %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("no job may be submitted: %+v", m.List())
	}
	if len(fx.port.applies) != 0 || len(fx.db.backups) != 0 || len(fx.log.get()) != 0 {
		t.Fatalf("nothing may be locked, backed up or applied: %v", fx.log.get())
	}
}

func TestReconcileApplySpecWithoutVerifiedFingerprintFailsClosed(t *testing.T) {
	fx := newReconcileFixture()
	_, err := NewReconcileApply(fx.deps).Spec(context.Background(), Params{"artist": "gza"})
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("want ErrConfirmationRequired, got %v", err)
	}
	if _, err := NewReconcileDryRun(fx.deps).Spec(context.Background(), Params{"artist": "gza"}); err != nil {
		t.Fatalf("the read-only dry run needs no confirmation: %v", err)
	}
	if len(fx.log.get()) != 0 || len(fx.port.applies) != 0 {
		t.Fatalf("calls %v", fx.log.get())
	}
}

func TestReconcileApplyStaleWhenUseCaseSeesOtherChecksum(t *testing.T) {
	fx := newReconcileFixture()
	fx.port.applyResult = func() (artists.ReconcileReport, error) {
		return artists.ReconcileReport{PlanChecksum: "other"},
			errors.New("plan changed since review (confirmed x, current other); rerun the dry run")
	}
	_, _, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if safeCode(err) != "stale_preview" {
		t.Fatalf("want stale preview, got %v", err)
	}
	if want := []string{"lock", "migrate", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
}
