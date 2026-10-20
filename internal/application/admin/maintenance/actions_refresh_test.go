package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// callLog records the order of lock, backup and refresh calls.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *callLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

type refreshCall struct{ dryRun, force bool }

type fakeRefresh struct {
	log    *callLog
	report artists.RefreshGenresReport
	err    error
	events []artists.RefreshGenresProgress
	calls  []refreshCall
	// ctxDoneAfterEvents records whether ctx was cancelled once all events were emitted.
	ctxDoneAfterEvents bool
}

func (f *fakeRefresh) Run(ctx context.Context, dryRun, force bool, progress func(artists.RefreshGenresProgress)) (artists.RefreshGenresReport, error) {
	f.log.add("refresh")
	f.calls = append(f.calls, refreshCall{dryRun, force})
	for _, e := range f.events {
		progress(e)
	}
	f.ctxDoneAfterEvents = ctx.Err() != nil
	return f.report, f.err
}

type fakeLocker struct {
	log *callLog
	err error
	// onLock, when set, runs while the lock is being taken.
	onLock func()
}

func (l *fakeLocker) TryLock() (func(), error) {
	l.log.add("lock")
	if l.onLock != nil {
		l.onLock()
	}
	if l.err != nil {
		return nil, l.err
	}
	return func() { l.log.add("release") }, nil
}

type fakeBackuper struct {
	log *callLog
	err error
}

func (b *fakeBackuper) Backup(context.Context) error {
	b.log.add("backup")
	return b.err
}

type retryAfterErr struct{ d time.Duration }

func (e retryAfterErr) Error() string             { return "429 from https://api.spotify.com with token xyz" }
func (e retryAfterErr) RetryAfter() time.Duration { return e.d }

type refreshFixture struct {
	log      *callLog
	runner   *fakeRefresh
	locker   *fakeLocker
	backuper *fakeBackuper
	digest   string
	deps     RefreshDeps
}

func newRefreshFixture(creds bool) *refreshFixture {
	log := &callLog{}
	fx := &refreshFixture{
		log:      log,
		runner:   &fakeRefresh{log: log, report: artists.RefreshGenresReport{ArtistsWithIDs: 2, Updated: 1, Unchanged: 1}},
		locker:   &fakeLocker{log: log},
		backuper: &fakeBackuper{log: log},
		digest:   "d1",
	}
	fx.runner.events = []artists.RefreshGenresProgress{
		{Stage: "artist_updated", ArtistSlug: "rza", ArtistIndex: 1, ArtistTotal: 2, Updated: true},
		{Stage: "artist_unchanged", ArtistSlug: "gza", ArtistIndex: 2, ArtistTotal: 2, Unchanged: true},
	}
	fx.deps = RefreshDeps{
		Runner:      fx.runner,
		Digest:      func(context.Context) (string, error) { return fx.digest, nil },
		Credentials: credentials(creds),
		Locker:      fx.locker,
		Backuper:    fx.backuper,
	}
	return fx
}

func TestRefreshUnavailableWithoutCredentials(t *testing.T) {
	fx := newRefreshFixture(false)
	for _, a := range []Action{NewRefreshMetadataPreview(fx.deps), NewRefreshMetadata(fx.deps)} {
		ok, reason := a.Available()
		if ok || reason != "Spotify credentials are not configured" {
			t.Fatalf("%s: available=%v reason=%q", a.Name(), ok, reason)
		}
		if _, err := specFor(t, a, Params{}); !errors.Is(err, ErrCredentialsMissing) {
			t.Fatalf("%s: spec without credentials: %v", a.Name(), err)
		}
	}
	if len(fx.log.get()) != 0 {
		t.Fatalf("nothing should run: %v", fx.log.get())
	}

	ok := newRefreshFixture(true)
	for _, a := range []Action{NewRefreshMetadataPreview(ok.deps), NewRefreshMetadata(ok.deps)} {
		if avail, reason := a.Available(); !avail {
			t.Fatalf("%s unavailable with credentials: %s", a.Name(), reason)
		}
	}
}

func TestRefreshForceRequiresConfirmation(t *testing.T) {
	fx := newRefreshFixture(true)
	a := NewRefreshMetadata(fx.deps)
	if a.Name() != "refresh-metadata" {
		t.Fatalf("name %q", a.Name())
	}

	plain, err := a.Preview(context.Background(), Params{"force": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Destructive || plain.RequiresTyped != "" || !plain.UsesNetwork {
		t.Fatalf("non-forced refresh: %+v", plain)
	}
	forced, err := a.Preview(context.Background(), Params{"force": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !forced.Destructive || forced.RequiresTyped != "refresh metadata" || len(forced.Warnings) == 0 {
		t.Fatalf("forced refresh: %+v", forced)
	}
	if forced.Fingerprint == plain.Fingerprint {
		t.Fatal("fingerprint must depend on force")
	}

	s, m, _ := newTestService(t, a)
	p := Params{"force": "true"}
	_, tok, err := s.Preview(context.Background(), "refresh-metadata", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "refresh-metadata", p, tok, ""); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("want ErrConfirmationRequired, got %v", err)
	}
	j, err := s.Run(context.Background(), "refresh-metadata", p, tok, "refresh metadata")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	if got, _ := m.Get(j.ID); got.Status != jobs.StatusSucceeded {
		t.Fatalf("job %+v", got)
	}
	if fx.runner.calls[0] != (refreshCall{dryRun: false, force: true}) {
		t.Fatalf("runner call %+v", fx.runner.calls[0])
	}
}

func TestRefreshTakesLockAndBackupBeforeRunning(t *testing.T) {
	fx := newRefreshFixture(true)
	spec, out, r, err := runAction(t, NewRefreshMetadata(fx.deps), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock", "backup", "refresh", "release"}) {
		t.Fatalf("call order %v", got)
	}
	if !slices.Contains(spec.Resources, "database") {
		t.Fatalf("resources %v", spec.Resources)
	}
	if out.Status != jobs.StatusSucceeded || out.Resumable {
		t.Fatalf("outcome %+v", out)
	}
	if r.counts["updated"] != 1 || r.counts["unchanged"] != 1 || r.done != 2 || r.total != 2 {
		t.Fatalf("counts %v progress %d/%d", r.counts, r.done, r.total)
	}
	rows := out.Result.Report.Rows
	if len(rows) != 2 || !slices.Equal(rows[1], []string{"rza", "updated", ""}) {
		t.Fatalf("rows %v", rows)
	}
}

func TestRefreshDryRunTakesNoLockOrBackup(t *testing.T) {
	fx := newRefreshFixture(true)
	a := NewRefreshMetadataPreview(fx.deps)
	if a.Name() != "refresh-metadata-preview" {
		t.Fatalf("name %q", a.Name())
	}
	pv, err := a.Preview(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Destructive || pv.RequiresTyped != "" || !pv.UsesNetwork {
		t.Fatalf("preview %+v", pv)
	}
	spec, out, _, err := runAction(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"refresh"}) {
		t.Fatalf("dry run must not lock or back up: %v", got)
	}
	if !slices.Equal(spec.Resources, []string{"database"}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	if !fx.runner.calls[0].dryRun || out.Status != jobs.StatusSucceeded {
		t.Fatalf("call %+v outcome %+v", fx.runner.calls[0], out)
	}
}

func TestRefreshRateLimitEndsPartialResumable(t *testing.T) {
	fx := newRefreshFixture(true)
	rate := retryAfterErr{d: 90 * time.Second}
	fx.runner.events = []artists.RefreshGenresProgress{
		{Stage: "artist_updated", ArtistSlug: "rza", ArtistIndex: 1, ArtistTotal: 3, Updated: true},
		{Stage: "spotify_failed", ArtistSlug: "gza", SpotifyID: "x", ArtistIndex: 2, ArtistTotal: 3, Err: rate},
	}
	fx.runner.report = artists.RefreshGenresReport{
		ArtistsWithIDs: 3, Updated: 1,
		Errors: []artists.RefreshGenresError{{Slug: "gza", ID: "x", Err: rate}},
	}
	fx.runner.err = fmt.Errorf("refresh genres completed with 1 errors: %w", artists.ErrRefreshCompletedWithErrors)

	_, out, r, err := runAction(t, NewRefreshMetadata(fx.deps), Params{})
	if err != nil {
		t.Fatalf("rate limit is a partial outcome, not a failure: %v", err)
	}
	if out.Status != jobs.StatusPartial || !out.Resumable {
		t.Fatalf("outcome %+v", out)
	}
	if !strings.Contains(out.Result.Summary, "90 seconds") || strings.Contains(out.Result.Summary, "token") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
	if !fx.runner.ctxDoneAfterEvents {
		t.Fatal("the run should be stopped once Spotify asks to back off")
	}
	if r.counts["failed"] != 1 {
		t.Fatalf("counts %v", r.counts)
	}
	for _, row := range out.Result.Report.Rows {
		if strings.Contains(strings.Join(row, ","), "token") {
			t.Fatalf("raw error leaked into report: %v", row)
		}
	}
	if got := fx.log.get(); got[len(got)-1] != "release" {
		t.Fatalf("lock must be released: %v", got)
	}
}

func TestRefreshLockedFailsWithConflict(t *testing.T) {
	fx := newRefreshFixture(true)
	fx.locker.err = fmt.Errorf("lock /secret/catalog.db.lock: %w", ErrLocked)

	_, _, _, err := runAction(t, NewRefreshMetadata(fx.deps), Params{})
	var se *SafeError
	if !errors.As(err, &se) || se.Code != "conflict" {
		t.Fatalf("want safe conflict, got %v", err)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock"}) {
		t.Fatalf("nothing may run without the lock: %v", got)
	}
}

func TestRefreshBackupFailureStopsBeforeRunning(t *testing.T) {
	fx := newRefreshFixture(true)
	fx.backuper.err = errors.New("disk full at /secret")

	_, _, _, err := runAction(t, NewRefreshMetadata(fx.deps), Params{})
	var se *SafeError
	if !errors.As(err, &se) || strings.Contains(se.Error(), "secret") {
		t.Fatalf("want safe error, got %v", err)
	}
	if got := fx.log.get(); !slices.Equal(got, []string{"lock", "backup", "release"}) {
		t.Fatalf("call order %v", got)
	}
}

func TestRefreshCancelledReturnsContextError(t *testing.T) {
	fx := newRefreshFixture(true)
	spec, err := specFor(t, NewRefreshMetadata(fx.deps), Params{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fx.runner.err = context.Canceled
	out, err := spec.Run(ctx, &recReporter{})
	if !errors.Is(err, context.Canceled) || !out.Resumable {
		t.Fatalf("out %+v err %v", out, err)
	}
}

func TestRefreshActionsValidateParams(t *testing.T) {
	fx := newRefreshFixture(true)
	for _, a := range []Action{NewRefreshMetadataPreview(fx.deps), NewRefreshMetadata(fx.deps)} {
		for _, p := range []Params{{"force": "yes"}, {"artist": "rza"}} {
			err := a.Validate(p)
			var se *SafeError
			if !errors.As(err, &se) || se.Code != "validation" {
				t.Fatalf("%s %v: want validation error, got %v", a.Name(), p, err)
			}
		}
		if err := a.Validate(Params{"force": "true"}); err != nil {
			t.Fatalf("%s: %v", a.Name(), err)
		}
	}
}

func TestRefreshFingerprintTracksCatalog(t *testing.T) {
	fx := newRefreshFixture(true)
	a := NewRefreshMetadata(fx.deps)
	first, _ := a.Preview(context.Background(), Params{})
	fx.digest = "d2"
	second, _ := a.Preview(context.Background(), Params{})
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("a changed catalog must change the fingerprint")
	}
}

func TestRefreshFatalErrorAfterArtistErrorsFails(t *testing.T) {
	fx := newRefreshFixture(true)
	fx.runner.report = artists.RefreshGenresReport{
		ArtistsWithIDs: 3, Updated: 1,
		Errors: []artists.RefreshGenresError{{Slug: "gza", ID: "x", Err: errors.New("spotify timeout")}},
	}
	fx.runner.err = errors.New("save artist catalog: rename /secret/artists.yaml: no space left")

	_, out, _, err := runAction(t, NewRefreshMetadata(fx.deps), Params{})
	var se *SafeError
	if !errors.As(err, &se) || strings.Contains(se.Error(), "secret") {
		t.Fatalf("a fatal error must fail the job safely, got out=%+v err=%v", out, err)
	}
	if out.Status == jobs.StatusPartial {
		t.Fatalf("fatal error reported as partial: %+v", out)
	}
	if got := fx.log.get(); got[len(got)-1] != "release" {
		t.Fatalf("lock must be released: %v", got)
	}
}

func TestRefreshArtistErrorsEndPartial(t *testing.T) {
	fx := newRefreshFixture(true)
	fx.runner.report = artists.RefreshGenresReport{
		ArtistsWithIDs: 2, Updated: 1,
		Errors: []artists.RefreshGenresError{{Slug: "gza", ID: "x", Err: errors.New("spotify timeout")}},
	}
	fx.runner.err = fmt.Errorf("refresh genres completed with 1 errors: %w", artists.ErrRefreshCompletedWithErrors)

	_, out, r, err := runAction(t, NewRefreshMetadata(fx.deps), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != jobs.StatusPartial || out.Resumable || r.counts["failed"] != 1 {
		t.Fatalf("outcome %+v counts %v", out, r.counts)
	}
}
