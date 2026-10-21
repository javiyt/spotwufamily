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

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

var (
	shaLocal  = strings.Repeat("a", 64)
	shaPinned = strings.Repeat("b", 64)
)

// fakeReleaser records calls; prereq can be changed between calls.
type fakeReleaser struct {
	mu           sync.Mutex
	log          *callLog
	prereq       Prereq
	preflightErr error
	pkg          PackageInfo
	pub          PublishInfo
	pubErr       error
	// onPreflight runs before each preflight returns (to simulate changes).
	onPreflight func(*Prereq)
	// publishFn, when set, replaces the canned publish result.
	publishFn func(context.Context) (PublishInfo, error)
}

func (f *fakeReleaser) Preflight(context.Context) (Prereq, error) {
	f.log.add("preflight")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onPreflight != nil {
		f.onPreflight(&f.prereq)
	}
	return f.prereq, f.preflightErr
}

func (f *fakeReleaser) Prepare(context.Context) (PackageInfo, error) {
	f.log.add("prepare")
	return f.pkg, nil
}

func (f *fakeReleaser) Publish(ctx context.Context) (PublishInfo, error) {
	f.log.add("publish")
	if f.publishFn != nil {
		return f.publishFn(ctx)
	}
	return f.pub, f.pubErr
}

func (f *fakeReleaser) set(fn func(*Prereq)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.prereq)
}

type fakePrepared struct {
	sha string
	ok  bool
}

func (p fakePrepared) LastPrepared() (string, bool) { return p.sha, p.ok }

type releaseFixture struct {
	log      *callLog
	releaser *fakeReleaser
	locker   *fakeLocker
	deps     ReleaseDeps
}

func newReleaseFixture() *releaseFixture {
	log := &callLog{}
	r := &fakeReleaser{
		log: log,
		prereq: Prereq{
			GhAuthenticated: true, GoAvailable: true, PackageDirClean: true,
			LocalContentSHA256: shaLocal, PinnedContentSHA256: shaPinned, LastReleaseTag: "catalog-old",
		},
		pkg: PackageInfo{Tag: "catalog-new", ContentSHA256: shaLocal,
			Assets: []string{"catalog.db.gz", "catalog.release.json", "catalog.snapshot.sql.gz"}},
		pub: PublishInfo{Tag: "catalog-new", Published: true},
	}
	locker := &fakeLocker{log: log}
	return &releaseFixture{log: log, releaser: r, locker: locker, deps: ReleaseDeps{
		Releaser:     r,
		Prepared:     fakePrepared{},
		Locker:       locker,
		Repo:         "javiyt/spotwufamily",
		PackageName:  "build/catalog-release",
		ManifestName: "data/catalog.release.json",
	}}
}

// prepared makes the fixture look like a successful prepare happened.
func (fx *releaseFixture) prepared() {
	fx.releaser.set(func(p *Prereq) {
		p.PackageDirClean = false
		p.PreparedTag = "catalog-new"
		p.PreparedContentSHA256 = shaLocal
		p.PreparedAssets = []string{"catalog.db.gz", "catalog.release.json", "catalog.snapshot.sql.gz"}
	})
	fx.deps.Prepared = fakePrepared{sha: shaLocal, ok: true}
}

func (fx *releaseFixture) published() int {
	n := 0
	for _, c := range fx.log.get() {
		if c == "publish" {
			n++
		}
	}
	return n
}

func TestReleasePrepareAvailability(t *testing.T) {
	cases := map[string]struct {
		mut    func(*Prereq)
		err    error
		ok     bool
		reason string
	}{
		"ready":             {mut: func(*Prereq) {}, ok: true},
		"gh not needed":     {mut: func(p *Prereq) { p.GhAuthenticated = false }, ok: true},
		"script missing":    {mut: func(*Prereq) {}, err: ErrReleasePrerequisite, reason: "python3"},
		"go missing":        {mut: func(p *Prereq) { p.GoAvailable = false }, reason: "Go"},
		"package not clean": {mut: func(p *Prereq) { p.PackageDirClean = false }, reason: "build/catalog-release"},
		"no database":       {mut: func(p *Prereq) { p.LocalContentSHA256 = "" }, reason: "database"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newReleaseFixture()
			fx.releaser.set(tc.mut)
			fx.releaser.preflightErr = tc.err
			ok, reason := NewReleasePrepare(fx.deps).Available()
			if ok != tc.ok || !strings.Contains(reason, tc.reason) {
				t.Fatalf("Available() = %v, %q", ok, reason)
			}
		})
	}
}

func TestReleasePreparePreviewIsAChecklist(t *testing.T) {
	fx := newReleaseFixture()
	fx.releaser.set(func(p *Prereq) { p.GhAuthenticated = false })
	a := NewReleasePrepare(fx.deps)
	if a.Name() != "release-prepare" {
		t.Fatalf("name %q", a.Name())
	}
	pv := previewSync(t, a, Params{})
	if pv.Destructive || pv.UsesNetwork || pv.RequiresTyped != "" || pv.Fingerprint == "" {
		t.Fatalf("preview %+v", pv)
	}
	for _, want := range []string{"Go", "build/catalog-release", "GitHub CLI", shaLocal, shaPinned, "catalog-old"} {
		if !scopeMentions(pv, want) {
			t.Fatalf("scope should mention %q: %+v", want, pv.Scope)
		}
	}
	if len(pv.Warnings) == 0 || !strings.Contains(strings.Join(pv.Warnings, " "), "gh auth login") {
		t.Fatalf("unmet gh prerequisite should warn: %v", pv.Warnings)
	}
	if !slices.Equal(fx.log.get(), []string{"preflight"}) {
		t.Fatalf("preview only runs preflight: %v", fx.log.get())
	}
	if err := a.Validate(Params{"x": "y"}); err == nil {
		t.Fatal("unknown params must be rejected")
	}
}

func TestReleasePrepareRunsUnderLockAndRecordsTheContent(t *testing.T) {
	fx := newReleaseFixture()
	spec, out, _, err := runAction(t, NewReleasePrepare(fx.deps), Params{})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("prepare: %+v %v", out, err)
	}
	if !slices.Equal(spec.Resources, []string{ResourceRelease, ResourceDatabase}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	want := []string{"preflight", "preflight", "lock", "preflight", "prepare", "release"}
	if got := fx.log.get(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	if !strings.Contains(out.Result.Summary, "catalog-new") || strings.Contains(out.Result.Summary, " /") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
	rep := out.Result.Report
	if rep == nil || !slices.Equal(rep.Columns, []string{"tag", "content_sha256", "assets"}) ||
		len(rep.Rows) != 1 || rep.Rows[0][0] != "catalog-new" || rep.Rows[0][1] != shaLocal {
		t.Fatalf("report %+v", rep)
	}
	if fx.published() != 0 {
		t.Fatal("prepare must never publish")
	}
}

func TestReleasePrepareRefusesDirtyPackageInsideLock(t *testing.T) {
	fx := newReleaseFixture()
	a := NewReleasePrepare(fx.deps)
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	fx.releaser.set(func(p *Prereq) { p.PackageDirClean = false })
	_, err = spec.Run(context.Background(), &recReporter{})
	if !errors.Is(err, ErrStalePreview) && !errors.Is(err, ErrReleasePackageNotClean) {
		assertSafe(t, err, "stale_preview")
	}
	if slices.Contains(fx.log.get(), "prepare") {
		t.Fatal("prepare must not run")
	}
	if got := fx.log.get(); got[len(got)-1] != "release" {
		t.Fatalf("lock must be released: %v", got)
	}
}

func TestReleaseSpecsFailClosedWithoutVerifiedFingerprint(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	for _, a := range []Action{NewReleasePrepare(fx.deps), NewReleasePublish(fx.deps)} {
		if _, err := a.Spec(context.Background(), Params{}); !errors.Is(err, ErrConfirmationRequired) {
			t.Fatalf("%s: want ErrConfirmationRequired, got %v", a.Name(), err)
		}
		if _, err := a.Spec(WithVerifiedFingerprint(context.Background(), "other"), Params{}); !errors.Is(err, ErrStalePreview) {
			t.Fatalf("%s: want ErrStalePreview for another fingerprint, got %v", a.Name(), err)
		}
	}
	if slices.Contains(fx.log.get(), "prepare") || fx.published() != 0 {
		t.Fatalf("nothing may run: %v", fx.log.get())
	}
}

func TestReleasePublishPreviewAndTypedPhrase(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	a := NewReleasePublish(fx.deps)
	if a.Name() != "release-publish" {
		t.Fatalf("name %q", a.Name())
	}
	d := a.(Describer).Describe()
	if !d.Destructive || !d.UsesNetwork {
		t.Fatalf("describe %+v", d)
	}
	pv := previewSync(t, a, Params{})
	if !pv.Destructive || !pv.UsesNetwork || pv.RequiresTyped != "publish release" {
		t.Fatalf("preview %+v", pv)
	}
	if pv.Fingerprint != fingerprint("release-publish", shaLocal, shaPinned, "catalog-new") {
		t.Fatalf("fingerprint must bind local, pinned and prepared tag: %s", pv.Fingerprint)
	}
	for _, want := range []string{"catalog-new", "javiyt/spotwufamily", "data/catalog.release.json"} {
		if !scopeMentions(pv, want) {
			t.Fatalf("scope should mention %q: %+v", want, pv.Scope)
		}
	}

	s, m, _ := newTestService(t, a)
	_, tok, err := s.Preview(context.Background(), "release-publish", Params{})
	if err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"", "publish", "PUBLISH RELEASE"} {
		if _, err := s.Run(context.Background(), "release-publish", Params{}, tok, typed); !errors.Is(err, ErrConfirmationRequired) {
			t.Fatalf("typed %q: want ErrConfirmationRequired, got %v", typed, err)
		}
	}
	if fx.published() != 0 {
		t.Fatal("nothing may publish without the phrase")
	}
	j, err := s.Run(context.Background(), "release-publish", Params{}, tok, "publish release")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusSucceeded || got.Result == nil || got.Result.Summary != "Published catalog release catalog-new." {
		t.Fatalf("job %+v %+v", got, got.Result)
	}
	if !slices.Equal(got.Resources, []string{ResourceRelease, ResourceDatabase}) {
		t.Fatalf("resources %v", got.Resources)
	}
	if fx.published() != 1 {
		t.Fatalf("calls %v", fx.log.get())
	}
}

func TestPublishRequiresPriorPrepare(t *testing.T) {
	cases := map[string]func(*releaseFixture){
		"never prepared": func(fx *releaseFixture) {},
		"prepared other content": func(fx *releaseFixture) {
			fx.prepared()
			fx.deps.Prepared = fakePrepared{sha: shaPinned, ok: true}
		},
		"package missing": func(fx *releaseFixture) {
			fx.prepared()
			fx.releaser.set(func(p *Prereq) { p.PreparedTag, p.PreparedContentSHA256 = "", "" })
		},
		"package of other content": func(fx *releaseFixture) {
			fx.prepared()
			fx.releaser.set(func(p *Prereq) { p.PreparedContentSHA256 = shaPinned })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newReleaseFixture()
			setup(fx)
			a := NewReleasePublish(fx.deps)
			pv := previewSync(t, a, Params{})
			if !strings.Contains(strings.Join(pv.Warnings, " "), "release-prepare") {
				t.Fatalf("preview should warn: %v", pv.Warnings)
			}
			_, err := a.Spec(WithVerifiedFingerprint(context.Background(), pv.Fingerprint), Params{})
			if !errors.Is(err, ErrNothingPrepared) {
				t.Fatalf("want ErrNothingPrepared, got %v", err)
			}
			assertSafe(t, err, "validation")
			if fx.published() != 0 {
				t.Fatal("must not publish")
			}
		})
	}
	fx := newReleaseFixture()
	fx.prepared()
	if _, out, _, err := runAction(t, NewReleasePublish(fx.deps), Params{}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("prepared content publishes: %+v %v", out, err)
	}
}

func TestReleasePublishUnavailableWithoutGhAuth(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	fx.releaser.set(func(p *Prereq) { p.GhAuthenticated = false })
	a := NewReleasePublish(fx.deps)
	ok, reason := a.Available()
	if ok || reason != "GitHub CLI is not authenticated. Run `gh auth login` in a terminal." {
		t.Fatalf("Available() = %v, %q", ok, reason)
	}
	if _, err := a.Preview(context.Background(), Params{}); !errors.Is(err, ErrGhNotAuthenticated) {
		t.Fatalf("preview: %v", err)
	}
	var se *SafeError
	if !errors.As(Safe(ErrGhNotAuthenticated), &se) || se.Message != "GitHub CLI is not authenticated. Run `gh auth login` in a terminal." {
		t.Fatalf("safe message %+v", se)
	}
	if fx.published() != 0 {
		t.Fatal("must not publish")
	}
}

func TestReleasePublishRefusedWhenGhLogsOutBeforeLock(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	a := NewReleasePublish(fx.deps)
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	fx.releaser.set(func(p *Prereq) { p.GhAuthenticated = false })
	_, err = spec.Run(context.Background(), &recReporter{})
	assertSafe(t, err, "credentials_missing")
	if fx.published() != 0 {
		t.Fatal("must not publish")
	}
	if got := fx.log.get(); got[len(got)-1] != "release" {
		t.Fatalf("lock must be released: %v", got)
	}
}

func TestReleasePublishStaleAfterLockIsRefused(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	a := NewReleasePublish(fx.deps)
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	// The pinned manifest changes (e.g. a merged catalog PR) after Spec.
	fx.releaser.set(func(p *Prereq) { p.PinnedContentSHA256 = strings.Repeat("c", 64) })
	_, err = spec.Run(context.Background(), &recReporter{})
	if !errors.Is(err, ErrStalePreview) {
		assertSafe(t, err, "stale_preview")
	}
	if fx.published() != 0 {
		t.Fatal("must not publish a stale preview")
	}
	if got := fx.log.get(); got[len(got)-1] != "release" {
		t.Fatalf("lock must be released: %v", got)
	}
}

func TestReleasePublishLockedFailsWithConflict(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	fx.locker.err = ErrLocked
	spec, err := specFor(t, NewReleasePublish(fx.deps), Params{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = spec.Run(context.Background(), &recReporter{})
	assertSafe(t, err, "conflict")
	if fx.published() != 0 {
		t.Fatal("must not publish")
	}
}

func TestReleasePublishNoChanges(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	fx.releaser.pub = PublishInfo{Tag: "catalog-new", NoChanges: true}
	_, out, _, err := runAction(t, NewReleasePublish(fx.deps), Params{})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("%+v %v", out, err)
	}
	if !strings.Contains(out.Result.Summary, "No catalog content changes") || !strings.Contains(out.Result.Summary, "catalog-new") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestReleasePublishFailureIsSafe(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	fx.releaser.pubErr = errors.New("gh: HTTP 401 token ghp_secret at /Users/x")
	_, _, _, err := runAction(t, NewReleasePublish(fx.deps), Params{})
	if err == nil || strings.Contains(err.Error(), "ghp_secret") || strings.Contains(err.Error(), "/Users") {
		t.Fatalf("unsafe error %v", err)
	}
}

func TestReleaseAvailabilityIsCachedBriefly(t *testing.T) {
	fx := newReleaseFixture()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fx.deps.Now = func() time.Time { return now }
	a := NewReleasePublish(fx.deps)
	a.Available()
	a.Available()
	if n := len(fx.log.get()); n != 1 {
		t.Fatalf("Available should reuse a recent preflight: %v", fx.log.get())
	}
	now = now.Add(time.Minute)
	a.Available()
	if n := len(fx.log.get()); n != 2 {
		t.Fatalf("an old preflight is refreshed: %v", fx.log.get())
	}
}

func TestReleaseSafeMessages(t *testing.T) {
	for err, code := range map[error]string{
		ErrGhNotAuthenticated:     "credentials_missing",
		ErrReleasePrerequisite:    "unavailable",
		ErrReleasePackageNotClean: "conflict",
		ErrNothingPrepared:        "validation",
	} {
		assertSafe(t, err, code)
	}
}

// scopeMentions reports whether a scope label or value contains substr.
func scopeMentions(pv Preview, substr string) bool {
	for _, l := range pv.Scope {
		if strings.Contains(l.Label, substr) || strings.Contains(l.Value, substr) {
			return true
		}
	}
	return false
}

const interruptedMessage = "Publishing was interrupted; a partial release catalog-new may exist on GitHub — check the Releases page before retrying."

// submitPublish runs release-publish through a jobs.Manager and returns it
// with the job id.
func submitPublish(t *testing.T, fx *releaseFixture) (*jobs.Manager, string) {
	t.Helper()
	s, m, _ := newTestService(t, NewReleasePublish(fx.deps))
	_, tok, err := s.Preview(context.Background(), "release-publish", Params{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Run(context.Background(), "release-publish", Params{}, tok, "publish release")
	if err != nil {
		t.Fatal(err)
	}
	return m, j.ID
}

func TestReleasePublishCannotBeCancelledOnceStarted(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	started, finish := make(chan struct{}), make(chan struct{})
	var publishCtxErr error
	fx.releaser.publishFn = func(ctx context.Context) (PublishInfo, error) {
		close(started)
		<-finish
		publishCtxErr = ctx.Err()
		return PublishInfo{Tag: "catalog-new", Published: true}, nil
	}
	m, id := submitPublish(t, fx)
	<-started
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(finish)
	m.Wait(id)
	got, _ := m.Get(id)
	if publishCtxErr != nil {
		t.Fatalf("the user cancel must not reach the publish subprocess: %v", publishCtxErr)
	}
	if got.Status != jobs.StatusSucceeded || got.Result == nil ||
		got.Result.Summary != "Published catalog release catalog-new. A cancel was requested, but publishing cannot be stopped once it has started." {
		t.Fatalf("a finished publish is reported truthfully: %+v %+v", got, got.Result)
	}
}

func TestReleasePublishInterruptedEndsFailedWithPartialReleaseMessage(t *testing.T) {
	for name, cancel := range map[string]bool{"timeout": false, "timeout after cancel request": true} {
		t.Run(name, func(t *testing.T) {
			fx := newReleaseFixture()
			fx.prepared()
			started, finish := make(chan struct{}), make(chan struct{})
			fx.releaser.publishFn = func(ctx context.Context) (PublishInfo, error) {
				close(started)
				<-finish
				return PublishInfo{}, fmt.Errorf("release publish: %w: %w", ErrPublishInterrupted, context.DeadlineExceeded)
			}
			m, id := submitPublish(t, fx)
			<-started
			if cancel {
				if err := m.Cancel(id); err != nil {
					t.Fatal(err)
				}
			}
			close(finish)
			m.Wait(id)
			got, _ := m.Get(id)
			if got.Status != jobs.StatusFailed || got.Message != interruptedMessage {
				t.Fatalf("job %+v", got)
			}
		})
	}
}

func TestReleasePublishFailureAfterCancelRequestIsFailed(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	started, finish := make(chan struct{}), make(chan struct{})
	fx.releaser.publishFn = func(context.Context) (PublishInfo, error) {
		close(started)
		<-finish
		return PublishInfo{}, fmt.Errorf("release publish: exit status 1: %w", ErrReleasePrerequisite)
	}
	m, id := submitPublish(t, fx)
	<-started
	_ = m.Cancel(id)
	close(finish)
	m.Wait(id)
	got, _ := m.Get(id)
	if got.Status != jobs.StatusFailed || got.Message == "" || strings.Contains(got.Message, "exit status") {
		t.Fatalf("job %+v", got)
	}
}

func TestReleasePublishCancelBeforePublishStartsCancels(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	inLock, proceed := make(chan struct{}), make(chan struct{})
	fx.locker.onLock = func() { close(inLock); <-proceed }
	m, id := submitPublish(t, fx)
	<-inLock
	_ = m.Cancel(id)
	close(proceed)
	m.Wait(id)
	got, _ := m.Get(id)
	if got.Status != jobs.StatusCancelled || fx.published() != 0 {
		t.Fatalf("a cancel before publishing starts stops the job: %+v %v", got, fx.log.get())
	}
}

func TestPublishInterruptedSafeMessageNamesNoTagWithoutContext(t *testing.T) {
	assertSafe(t, ErrPublishInterrupted, "interrupted")
}

// blockingReleaser blocks every Preflight until release is closed.
type blockingReleaser struct {
	*fakeReleaser
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingReleaser) Preflight(ctx context.Context) (Prereq, error) {
	b.calls.Add(1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.fakeReleaser.Preflight(ctx)
}

func TestServiceActionsNeverWaitForReleasePreflight(t *testing.T) {
	fx := newReleaseFixture()
	br := &blockingReleaser{fakeReleaser: fx.releaser, entered: make(chan struct{}, 1), release: make(chan struct{})}
	fx.deps.Releaser = br
	pub := NewReleasePublish(fx.deps)
	s, _, _ := newTestService(t, newFake("a"), pub)
	defer close(br.release)

	done := make(chan []ActionInfo, 1)
	go func() { done <- s.Actions() }()
	var got []ActionInfo
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Actions() blocked on the release preflight")
	}
	if got[1].Available || got[1].UnavailableReason != releaseCheckingReason {
		t.Fatalf("unchecked release action: %+v", got[1])
	}
	<-br.entered // a background check was started
	// A second listing while the check runs neither blocks nor starts another.
	go func() { done <- s.Actions() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Actions() blocked while a check is running")
	}
	if n := br.calls.Load(); n != 1 {
		t.Fatalf("checks must be single-flight, got %d", n)
	}
}

func TestReleaseQuickAvailabilityUsesTheBackgroundResult(t *testing.T) {
	fx := newReleaseFixture()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	fx.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	fx.releaser.set(func(p *Prereq) { p.GhAuthenticated = false })
	a := NewReleasePublish(fx.deps).(*releaseAction)
	if ok, reason := a.AvailableNow(); ok || reason != releaseCheckingReason {
		t.Fatalf("first listing: %v %q", ok, reason)
	}
	a.waitRefresh()
	if ok, reason := a.AvailableNow(); ok || reason != ghNotAuthenticatedReason {
		t.Fatalf("after the check: %v %q", ok, reason)
	}
	// An expired result is still shown while a new check runs.
	mu.Lock()
	now = now.Add(time.Minute)
	mu.Unlock()
	if ok, reason := a.AvailableNow(); ok || reason != ghNotAuthenticatedReason {
		t.Fatalf("stale result: %v %q", ok, reason)
	}
	a.waitRefresh()
	if n := len(fx.log.get()); n != 2 {
		t.Fatalf("an expired result is refreshed once in the background: %v", fx.log.get())
	}
}

func TestReleasePublishRunStillPreflightsFreshly(t *testing.T) {
	fx := newReleaseFixture()
	fx.prepared()
	pub := NewReleasePublish(fx.deps)
	s, m, _ := newTestService(t, pub)
	s.Actions()
	pub.(*releaseAction).waitRefresh()
	if ok, _ := pub.Available(); !ok {
		t.Fatal("setup: publish must be available")
	}
	before := len(fx.log.get())
	_, tok, err := s.Preview(context.Background(), "release-publish", Params{})
	if err != nil {
		t.Fatal(err)
	}
	afterPreview := len(fx.log.get())
	if afterPreview == before {
		t.Fatal("preview must run a fresh preflight, not the cached listing result")
	}
	j, err := s.Run(context.Background(), "release-publish", Params{}, tok, publishTypedPhrase)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	calls := fx.log.get()[afterPreview:]
	n := 0
	for _, c := range calls {
		if c == "preflight" {
			n++
		}
	}
	// Run re-previews (fresh), and the job re-checks inside the lock.
	if n < 2 {
		t.Fatalf("run must preflight freshly before and inside the lock: %v", calls)
	}
}
