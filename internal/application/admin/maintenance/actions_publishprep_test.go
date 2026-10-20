package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/catalogaudit"
	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

type fakeVerifier struct {
	log    *callLog
	result VerifyResult
	err    error
	calls  []bool // checkSnapshot of each call
}

func (f *fakeVerifier) Verify(_ context.Context, checkSnapshot bool) (VerifyResult, error) {
	f.log.add("verify")
	f.calls = append(f.calls, checkSnapshot)
	return f.result, f.err
}

type fakeSnapshot struct {
	log    *callLog
	result SnapshotWrite
	err    error
}

func (f *fakeSnapshot) Write(context.Context) (SnapshotWrite, error) {
	f.log.add("snapshot")
	return f.result, f.err
}

// fakeExport writes files into the export writer. cancel, when set, is called
// after the first file, and later writes see the cancelled context.
type fakeExport struct {
	log    *callLog
	files  map[string]string
	cancel context.CancelFunc
	err    error
}

func (f *fakeExport) Run(ctx context.Context, w catalogexport.Writer) (catalogexport.Report, error) {
	f.log.add("export")
	names := make([]string, 0, len(f.files))
	for name := range f.files {
		names = append(names, name)
	}
	slices.Sort(names)
	var rep catalogexport.Report
	for i, name := range names {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		changed, err := w.WriteFile(name, []byte(f.files[name]))
		if err != nil {
			return rep, err
		}
		if changed {
			rep.FilesWritten++
		} else {
			rep.FilesKept++
		}
		if i == 0 && f.cancel != nil {
			f.cancel()
		}
	}
	return rep, f.err
}

// dirStager stages files under root/.staging and publishes them with a
// rename, like the filesystem adapter.
type dirStager struct {
	log  *callLog
	root string
}

func (s *dirStager) Stage() (StagedExport, error) {
	s.log.add("stage")
	dir, err := os.MkdirTemp(s.root, ".staging-*")
	if err != nil {
		return nil, err
	}
	return &dirStaged{log: s.log, dir: dir}, nil
}

type dirStaged struct {
	log     *callLog
	dir     string
	targets map[string]string // staged -> target
}

func (s *dirStaged) WriteFile(path string, data []byte) (bool, error) {
	if s.targets == nil {
		s.targets = map[string]string{}
	}
	f, err := os.CreateTemp(s.dir, "f-*.tmp")
	if err != nil {
		return false, err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	s.targets[f.Name()] = path
	return true, err
}

func (s *dirStaged) PruneDir(string, map[string]struct{}) error { return nil }

func (s *dirStaged) Commit() error {
	s.log.add("commit")
	for staged, target := range s.targets {
		if err := os.Rename(staged, target); err != nil {
			return err
		}
	}
	return os.RemoveAll(s.dir)
}

func (s *dirStaged) Discard() error { return os.RemoveAll(s.dir) }

type fakeRepoAudit struct {
	log    *callLog
	report catalogaudit.Report
	err    error
}

func (f *fakeRepoAudit) run(context.Context) (catalogaudit.Report, error) {
	f.log.add("audit")
	return f.report, f.err
}

type prepFixture struct {
	log      *callLog
	root     string
	exists   bool
	digest   string
	locker   *fakeLocker
	verifier *fakeVerifier
	snapshot *fakeSnapshot
	export   *fakeExport
	audit    *fakeRepoAudit
	deps     PublishPrepDeps
}

func newPrepFixture(t *testing.T) *prepFixture {
	t.Helper()
	log := &callLog{}
	fx := &prepFixture{
		log:      log,
		root:     t.TempDir(),
		exists:   true,
		digest:   "d1",
		locker:   &fakeLocker{log: log},
		verifier: &fakeVerifier{log: log, result: VerifyResult{Migrations: 7, Checks: []string{"integrity_check", "migrations"}}},
		snapshot: &fakeSnapshot{log: log, result: SnapshotWrite{Changed: true, PreviousKept: true}},
		audit: &fakeRepoAudit{log: log, report: catalogaudit.Report{Checks: []catalogaudit.Check{
			{Name: "snapshot", Status: catalogaudit.StatusOK, Detail: "matches"},
		}}},
	}
	fx.export = &fakeExport{log: log, files: map[string]string{
		filepath.Join(fx.root, "a.json"): "new-a",
		filepath.Join(fx.root, "b.json"): "new-b",
	}}
	fx.deps = PublishPrepDeps{
		DatabaseExists: func() bool { return fx.exists },
		Digest:         func(context.Context) (string, error) { return fx.digest, nil },
		Verifier:       fx.verifier,
		Snapshot:       fx.snapshot,
		Export:         fx.export,
		Stager:         &dirStager{log: log, root: fx.root},
		Audit:          fx.audit.run,
		Locker:         fx.locker,
		DatabaseName:   "catalog.db",
		SnapshotName:   "catalog.snapshot.sql.gz",
		ExportNames:    []string{"data/generated/", "static/search-index.json", "content/generated/"},
	}
	return fx
}

func prepActions(d PublishPrepDeps) map[string]Action {
	out := map[string]Action{}
	for _, a := range NewPublishPrepActions(d) {
		out[a.Name()] = a
	}
	return out
}

func assertSafeCode(t *testing.T, err error, code string) *SafeError {
	t.Helper()
	var se *SafeError
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("want SafeError %q, got %v (%T)", code, err, err)
	}
	return se
}

func TestPublishPrepActionNames(t *testing.T) {
	var names []string
	for _, a := range NewPublishPrepActions(newPrepFixture(t).deps) {
		names = append(names, a.Name())
	}
	if want := []string{"db-verify", "db-snapshot", "export", "audit", "refresh-export"}; !slices.Equal(names, want) {
		t.Fatalf("names %v", names)
	}
}

func TestExportCancelledLeavesPreviousArtifacts(t *testing.T) {
	fx := newPrepFixture(t)
	for name, data := range map[string]string{"a.json": "old-a", "b.json": "old-b"} {
		if err := os.WriteFile(filepath.Join(fx.root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.export.cancel = cancel

	spec, err := prepActions(fx.deps)["export"].Spec(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = spec.Run(ctx, &recReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	for name, want := range map[string]string{"a.json": "old-a", "b.json": "old-b"} {
		got, err := os.ReadFile(filepath.Join(fx.root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want previous bytes %q", name, got, err, want)
		}
	}
	_ = filepath.WalkDir(fx.root, func(path string, d fs.DirEntry, err error) error {
		if path != fx.root && (strings.Contains(d.Name(), ".tmp") || strings.HasPrefix(d.Name(), ".staging")) {
			t.Errorf("leftover %s", d.Name())
		}
		return nil
	})
	if got := fx.log.get(); slices.Contains(got, "commit") || got[len(got)-1] != "release" {
		t.Fatalf("calls %v", got)
	}
}

func TestExportPublishesStagedFiles(t *testing.T) {
	fx := newPrepFixture(t)
	spec, out, r, err := runAction(t, prepActions(fx.deps)["export"], Params{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != jobs.StatusSucceeded {
		t.Fatalf("outcome %+v", out)
	}
	if !slices.Equal(spec.Resources, []string{ResourceDatabase, ResourceExport}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	got, _ := os.ReadFile(filepath.Join(fx.root, "b.json"))
	if string(got) != "new-b" {
		t.Fatalf("b.json %q", got)
	}
	if want := []string{"lock", "stage", "export", "commit", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls %v", fx.log.get())
	}
	if !slices.Contains(r.steps, "publishing export") {
		t.Fatalf("steps %v", r.steps)
	}
	if !slices.Equal(out.Result.Report.Columns, []string{"check", "status", "detail"}) {
		t.Fatalf("columns %v", out.Result.Report.Columns)
	}
}

func TestRefreshExportStepsInOrder(t *testing.T) {
	fx := newPrepFixture(t)
	spec, out, r, err := runAction(t, prepActions(fx.deps)["refresh-export"], Params{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != jobs.StatusSucceeded {
		t.Fatalf("outcome %+v", out)
	}
	want := []string{"lock", "verify", "snapshot", "stage", "export", "commit", "audit", "release"}
	if got := fx.log.get(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	if fx.verifier.calls[0] {
		t.Fatal("the composite verify must not fail on the snapshot it is about to rewrite")
	}
	steps := []string{"verifying database", "writing snapshot", "exporting", "publishing export", "auditing"}
	var seen []string
	for _, s := range r.steps {
		if slices.Contains(steps, s) {
			seen = append(seen, s)
		}
	}
	if !slices.Equal(seen, steps) {
		t.Fatalf("steps %v", r.steps)
	}
	if !slices.Equal(spec.Resources, []string{ResourceDatabase, ResourceExport}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	rows := out.Result.Report.Rows
	if !slices.IsSortedFunc(rows, slices.Compare[[]string]) {
		t.Fatalf("rows not sorted: %v", rows)
	}
}

func TestVerifyFailureStopsComposite(t *testing.T) {
	fx := newPrepFixture(t)
	fx.verifier.result = VerifyResult{Checks: []string{"integrity_check"}, FailedCheck: "foreign_key_check"}
	fx.verifier.err = fmt.Errorf("%w: foreign key violation in /abs/catalog.db", ErrDatabaseInvalid)

	_, _, _, err := runAction(t, prepActions(fx.deps)["refresh-export"], Params{})
	se := assertSafeCode(t, err, "check_failed")
	if !strings.Contains(se.Message, "foreign_key_check") || strings.Contains(se.Message, "/abs") {
		t.Fatalf("message %q", se.Message)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock", "verify", "release"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestCompositeLaterFailureNamesCompletedSteps(t *testing.T) {
	fx := newPrepFixture(t)
	fx.export.err = errors.New("disk full at /abs/site")
	_, _, _, err := runAction(t, prepActions(fx.deps)["refresh-export"], Params{})
	se := assertSafeCode(t, err, "internal")
	if !strings.Contains(se.Message, "Export failed") || !strings.Contains(se.Message, "verify, snapshot") || strings.Contains(se.Message, "/abs") {
		t.Fatalf("message %q", se.Message)
	}
	if got := fx.log.get(); slices.Contains(got, "audit") || slices.Contains(got, "commit") || got[len(got)-1] != "release" {
		t.Fatalf("calls %v", got)
	}
}

func TestCompositeCancelBetweenStepsStops(t *testing.T) {
	fx := newPrepFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.deps.Snapshot = snapshotFunc(func(context.Context) (SnapshotWrite, error) {
		fx.log.add("snapshot")
		cancel()
		return SnapshotWrite{Changed: true}, nil
	})
	spec, err := prepActions(fx.deps)["refresh-export"].Spec(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run(ctx, &recReporter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock", "verify", "snapshot", "release"}) {
		t.Fatalf("calls %v", got)
	}
}

type snapshotFunc func(context.Context) (SnapshotWrite, error)

func (f snapshotFunc) Write(ctx context.Context) (SnapshotWrite, error) { return f(ctx) }

func TestPublishPrepUnavailableWithoutDatabase(t *testing.T) {
	fx := newPrepFixture(t)
	fx.exists = false
	for _, a := range NewPublishPrepActions(fx.deps) {
		ok, reason := a.Available()
		if ok || reason != "No database found; run the init workflow first" {
			t.Fatalf("%s: available=%v reason=%q", a.Name(), ok, reason)
		}
		if _, err := a.Spec(context.Background(), Params{}); err == nil {
			t.Fatalf("%s: spec without a database", a.Name())
		}
	}
	// The database can disappear after the job was queued.
	fx.exists = true
	spec, err := prepActions(fx.deps)["db-snapshot"].Spec(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	fx.exists = false
	_, err = spec.Run(context.Background(), &recReporter{})
	assertSafeCode(t, err, "unavailable")
	if got := fx.log.get(); slices.Contains(got, "snapshot") {
		t.Fatalf("calls %v", got)
	}
}

func TestPublishPrepLockedFailsSafely(t *testing.T) {
	for _, name := range []string{"db-snapshot", "export", "refresh-export"} {
		fx := newPrepFixture(t)
		fx.locker.err = fmt.Errorf("lock /abs/catalog.db.lock: %w", ErrLocked)
		_, _, _, err := runAction(t, prepActions(fx.deps)[name], Params{})
		assertSafeCode(t, err, "conflict")
		if got := fx.log.get(); !slices.Equal(got, []string{"lock"}) {
			t.Fatalf("%s: calls %v", name, got)
		}
	}
}

func TestPublishPrepRechecksFingerprintInsideLock(t *testing.T) {
	for _, name := range []string{"db-snapshot", "export", "refresh-export"} {
		fx := newPrepFixture(t)
		a := prepActions(fx.deps)[name]
		spec, err := a.Spec(context.Background(), Params{})
		if err != nil {
			t.Fatal(err)
		}
		fx.digest = "changed while queued"
		_, err = spec.Run(context.Background(), &recReporter{})
		assertSafeCode(t, err, "stale_preview")
		if got := fx.log.get(); !slices.Equal(got, []string{"lock", "release"}) {
			t.Fatalf("%s: calls %v", name, got)
		}
	}
}

func TestPublishPrepLockReleasedOnFailures(t *testing.T) {
	cases := map[string]func(fx *prepFixture){
		"verify":   func(fx *prepFixture) { fx.verifier.err = ErrDatabaseInvalid },
		"snapshot": func(fx *prepFixture) { fx.snapshot.err = errors.New("boom") },
		"export":   func(fx *prepFixture) { fx.export.err = errors.New("boom") },
		"audit":    func(fx *prepFixture) { fx.audit.err = errors.New("boom") },
	}
	for name, mutate := range cases {
		fx := newPrepFixture(t)
		mutate(fx)
		if _, _, _, err := runAction(t, prepActions(fx.deps)["refresh-export"], Params{}); err == nil {
			t.Fatalf("%s: want failure", name)
		}
		got := fx.log.get()
		if got[0] != "lock" || got[len(got)-1] != "release" || slices.Contains(got[1:len(got)-1], "release") {
			t.Fatalf("%s: calls %v", name, got)
		}
	}
}

func TestReadOnlyActionsTakeNoLock(t *testing.T) {
	fx := newPrepFixture(t)
	for _, name := range []string{"db-verify", "audit"} {
		spec, _, _, err := runAction(t, prepActions(fx.deps)[name], Params{})
		if err != nil {
			t.Fatal(err)
		}
		if len(spec.Resources) != 0 {
			t.Fatalf("%s resources %v", name, spec.Resources)
		}
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"verify", "audit"}) {
		t.Fatalf("calls %v", got)
	}
	if !fx.verifier.calls[0] {
		t.Fatal("db-verify checks the snapshot like the CLI")
	}
}

func TestDBVerifyReportsStaleSnapshotAsPartial(t *testing.T) {
	fx := newPrepFixture(t)
	fx.verifier.result.Snapshot = SnapshotStale
	_, out, _, err := runAction(t, prepActions(fx.deps)["db-verify"], Params{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != jobs.StatusPartial || !slices.ContainsFunc(out.Result.Report.Rows, func(r []string) bool {
		return r[0] == "snapshot" && r[1] == "stale"
	}) {
		t.Fatalf("outcome %+v", out)
	}
}

func TestAuditActionRowsSortedAndStaleIsPartial(t *testing.T) {
	fx := newPrepFixture(t)
	fx.audit.report = catalogaudit.Report{Checks: []catalogaudit.Check{
		{Name: "snapshot", Status: catalogaudit.StatusStale, Detail: "catalog.snapshot.sql.gz does not match the database; run db-snapshot"},
		{Name: "export-fresh", Status: catalogaudit.StatusOK, Detail: "matches the database"},
		{Name: "export-artifact generated/albums/index.json", Status: catalogaudit.StatusOK, Detail: "present"},
	}}
	_, out, _, err := runAction(t, prepActions(fx.deps)["audit"], Params{})
	if err != nil {
		t.Fatal(err)
	}
	rep := out.Result.Report
	if !slices.Equal(rep.Columns, []string{"check", "status", "detail"}) {
		t.Fatalf("columns %v", rep.Columns)
	}
	var names []string
	for _, row := range rep.Rows {
		names = append(names, row[0])
	}
	if want := []string{"export-artifact generated/albums/index.json", "export-fresh", "snapshot"}; !slices.Equal(names, want) {
		t.Fatalf("rows %v", names)
	}
	if out.Status != jobs.StatusPartial || !strings.Contains(out.Result.Summary, "1 of 3") {
		t.Fatalf("outcome %+v", out)
	}
}

func TestPublishPrepPreviewShowsFileNamesOnly(t *testing.T) {
	fx := newPrepFixture(t)
	for _, a := range NewPublishPrepActions(fx.deps) {
		pv, err := a.Preview(context.Background(), Params{})
		if err != nil {
			t.Fatal(err)
		}
		if pv.Destructive || pv.RequiresTyped != "" || pv.UsesNetwork || pv.Fingerprint == "" {
			t.Fatalf("%s preview %+v", a.Name(), pv)
		}
		for _, line := range pv.Scope {
			if strings.Contains(line.Value, fx.root) {
				t.Fatalf("%s leaks a path: %+v", a.Name(), line)
			}
		}
		if err := a.Validate(Params{"force": "true"}); err == nil {
			t.Fatalf("%s accepts unknown params", a.Name())
		}
	}
	snap, _ := prepActions(fx.deps)["db-snapshot"].Preview(context.Background(), Params{})
	if !slices.ContainsFunc(snap.Scope, func(l ScopeLine) bool {
		return strings.Contains(l.Value, "catalog.snapshot.sql.gz.prev")
	}) {
		t.Fatalf("snapshot preview must document the .prev rotation: %+v", snap.Scope)
	}
	other := newPrepFixture(t)
	other.digest = "d2"
	pv2, _ := prepActions(other.deps)["db-snapshot"].Preview(context.Background(), Params{})
	if pv2.Fingerprint == snap.Fingerprint {
		t.Fatal("fingerprint must depend on the digest")
	}
}

func TestSnapshotRowReportsPreviousVersion(t *testing.T) {
	fx := newPrepFixture(t)
	_, out, _, err := runAction(t, prepActions(fx.deps)["db-snapshot"], Params{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock", "snapshot", "release"}) {
		t.Fatalf("calls %v", got)
	}
	row := out.Result.Report.Rows[0]
	if row[0] != "snapshot" || row[1] != "written" || !strings.Contains(row[2], "catalog.snapshot.sql.gz.prev") {
		t.Fatalf("row %v", row)
	}
}

// failingStager publishes the first staged file and then fails, like a disk
// error part way through Commit.
type failingStager struct{ dirStager }

func (s *failingStager) Stage() (StagedExport, error) {
	staged, err := s.dirStager.Stage()
	if err != nil {
		return nil, err
	}
	return &failingStaged{dirStaged: staged.(*dirStaged)}, nil
}

type failingStaged struct{ *dirStaged }

func (s *failingStaged) Commit() error {
	s.log.add("commit")
	for staged, target := range s.targets {
		if err := os.Rename(staged, target); err != nil {
			return err
		}
		break
	}
	return errors.New("no space left on device: /abs/site")
}

func TestExportCommitFailureIsReportedHonestly(t *testing.T) {
	for _, name := range []string{"export", "refresh-export"} {
		fx := newPrepFixture(t)
		fx.deps.Stager = &failingStager{dirStager{log: fx.log, root: fx.root}}
		_, _, _, err := runAction(t, prepActions(fx.deps)[name], Params{})
		se := assertSafeCode(t, err, "write_failed")
		if !strings.Contains(se.Message, "run export again") || strings.Contains(se.Message, "/abs") {
			t.Fatalf("%s: message %q", name, se.Message)
		}
		if name == "refresh-export" && !strings.Contains(se.Message, "Export failed after verify, snapshot completed.") {
			t.Fatalf("composite message %q", se.Message)
		}
		got := fx.log.get()
		if slices.Contains(got, "audit") || got[len(got)-1] != "release" || !slices.Contains(got, "commit") {
			t.Fatalf("%s: calls %v", name, got)
		}

		// The job manager reports the outcome as failed with the safe message.
		s, m, _ := newTestService(t, prepActions(fx.deps)[name])
		_, tok, err := s.Preview(context.Background(), name, Params{})
		if err != nil {
			t.Fatal(err)
		}
		j, err := s.Run(context.Background(), name, Params{}, tok, "")
		if err != nil {
			t.Fatal(err)
		}
		m.Wait(j.ID)
		if got, _ := m.Get(j.ID); got.Status != jobs.StatusFailed || !strings.Contains(got.Message, "run export again") {
			t.Fatalf("%s: job %+v", name, got)
		}
	}
}
