package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// fakeSync records each sync call and replays scripted results per slug
// ("" for a whole-catalog run).
type fakeSync struct {
	log     *callLog
	calls   []catalogsync.Options
	results map[string]SyncRunResult
	errs    map[string]error
	// onRun runs inside the call, e.g. to cancel the job context.
	onRun func(slug string)
}

func (f *fakeSync) Run(_ context.Context, o catalogsync.Options) (SyncRunResult, error) {
	f.log.add("sync:" + o.ArtistSlug)
	f.calls = append(f.calls, o)
	if f.onRun != nil {
		f.onRun(o.ArtistSlug)
	}
	res, ok := f.results[o.ArtistSlug]
	if !ok {
		res = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsProcessed: 1}}
		if o.DryRun {
			res = SyncRunResult{Report: catalogsync.Report{DryRun: true, ArtistsPlanned: 1}}
		}
	}
	if o.Progress != nil && !o.DryRun && res.Report.ArtistsProcessed > 0 {
		slug := o.ArtistSlug
		if slug == "" {
			slug = "all"
		}
		o.Progress(catalogsync.ProgressEvent{
			Stage: catalogsync.ProgressArtistFinished, ArtistSlug: slug, ArtistIndex: 1, ArtistTotal: 1,
			Stats: catalogsync.SyncStats{AlbumsUpserted: 3, TracksUpserted: 30},
		})
	}
	return res, f.errs[o.ArtistSlug]
}

type syncFixture struct {
	log       *callLog
	runner    *fakeSync
	locker    *fakeLocker
	backuper  *fakeBackuper
	digest    string
	syncable  []string
	resumable *ResumableRun
	deps      SyncDeps
}

func newSyncFixture(creds bool) *syncFixture {
	log := &callLog{}
	fx := &syncFixture{
		log:      log,
		runner:   &fakeSync{log: log, results: map[string]SyncRunResult{}, errs: map[string]error{}},
		locker:   &fakeLocker{log: log},
		backuper: &fakeBackuper{log: log},
		digest:   "d1",
		syncable: []string{"gza", "rza", "odb", "raekwon", "ghostface", "method-man", "inspectah-deck"},
	}
	fx.deps = SyncDeps{
		Runner:      fx.runner,
		Digest:      func(context.Context) (string, error) { return fx.digest, nil },
		Syncable:    func(context.Context) ([]string, error) { return slices.Clone(fx.syncable), nil },
		Credentials: credentials(creds),
		Resumable: func(context.Context) (ResumableRun, bool, error) {
			if fx.resumable == nil {
				return ResumableRun{}, false, nil
			}
			return *fx.resumable, true, nil
		},
		Locker:   fx.locker,
		Backuper: fx.backuper,
	}
	return fx
}

func previewSync(t *testing.T, a Action, p Params) Preview {
	t.Helper()
	if err := a.Validate(p); err != nil {
		t.Fatalf("Validate(%v): %v", p, err)
	}
	pv, err := a.Preview(context.Background(), p)
	if err != nil {
		t.Fatalf("Preview(%v): %v", p, err)
	}
	return pv
}

func safeCode(err error) string {
	var se *SafeError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestSyncAllRequiresTypedConfirmation(t *testing.T) {
	fx := newSyncFixture(true)
	pv := previewSync(t, NewSync(fx.deps), Params{"scope": "all"})
	if !pv.Destructive || pv.RequiresTyped != "sync 7 artists" {
		t.Fatalf("scope=all must need typed confirmation: %+v", pv)
	}
	if !pv.UsesNetwork || len(pv.Warnings) == 0 || !strings.Contains(strings.Join(pv.Warnings, " "), "estimate") {
		t.Fatalf("want network use and a quota caveat: %+v", pv)
	}
	want := fmt.Sprint(7 * (1 + albumPagesEstimate))
	if !scopeHas(pv, want) || !scopeHas(pv, "7") {
		t.Fatalf("scope should show 7 artists and %s estimated requests: %+v", want, pv.Scope)
	}

	selected := previewSync(t, NewSync(fx.deps), Params{"scope": "selected", "artists": "gza,rza,odb,raekwon,ghostface,method-man"})
	if selected.RequiresTyped != "sync 6 artists" || !selected.Destructive {
		t.Fatalf("more than 5 selected artists need typed confirmation: %+v", selected)
	}

	dry := previewSync(t, NewSyncDryRun(fx.deps), Params{"scope": "all"})
	if dry.Destructive || dry.RequiresTyped != "" {
		t.Fatalf("a dry run never needs typed confirmation: %+v", dry)
	}
}

func TestSyncSingleArtistNoTypedPhrase(t *testing.T) {
	fx := newSyncFixture(true)
	pv := previewSync(t, NewSync(fx.deps), Params{"scope": "artist", "artists": "gza"})
	if pv.Destructive || pv.RequiresTyped != "" {
		t.Fatalf("one artist needs no typed phrase: %+v", pv)
	}
	five := previewSync(t, NewSync(fx.deps), Params{"scope": "selected", "artists": "gza,rza,odb,raekwon,ghostface"})
	if five.RequiresTyped != "" {
		t.Fatalf("five selected artists need no typed phrase: %+v", five)
	}

	_, out, r, err := runAction(t, NewSync(fx.deps), Params{"scope": "artist", "artists": "gza"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	if len(fx.runner.calls) != 1 || fx.runner.calls[0].ArtistSlug != "gza" {
		t.Fatalf("want a single-artist sync of gza: %+v", fx.runner.calls)
	}
	if r.counts["synced"] != 1 || r.counts["albums"] != 3 || r.counts["tracks"] != 30 {
		t.Fatalf("counts: %v", r.counts)
	}
}

func TestSyncSelectedRunsEachSlugSequentially(t *testing.T) {
	fx := newSyncFixture(true)
	_, out, r, err := runAction(t, NewSync(fx.deps), Params{"scope": "selected", "artists": "rza,gza"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	want := []string{"lock", "backup", "sync:rza", "sync:gza", "release"}
	if got := fx.log.get(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if r.done != 2 || r.total != 2 || !slices.Contains(r.steps, "rza") || !slices.Contains(r.steps, "gza") {
		t.Fatalf("progress %d/%d steps %v", r.done, r.total, r.steps)
	}
}

func TestSyncAllRunsWholeCatalogOnce(t *testing.T) {
	fx := newSyncFixture(true)
	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	if len(fx.runner.calls) != 1 || fx.runner.calls[0].ArtistSlug != "" || fx.runner.calls[0].DryRun {
		t.Fatalf("want one whole-catalog sync: %+v", fx.runner.calls)
	}
}

func TestSyncRateLimitEndsPartialResumable(t *testing.T) {
	fx := newSyncFixture(true)
	fx.runner.results["rza"] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsFailed: 1, RunID: 4}}
	fx.runner.errs["rza"] = &catalogsync.PartialSyncError{Err: retryAfterErr{90 * time.Second}}

	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "selected", "artists": "gza,rza,odb"})
	if err != nil {
		t.Fatalf("a rate limit is a partial outcome, not an error: %v", err)
	}
	if out.Status != jobs.StatusPartial || !out.Resumable {
		t.Fatalf("want partial resumable: %+v", out)
	}
	s := out.Result.Summary
	if !strings.HasPrefix(s, "Spotify rate limit reached; retry after 90s") {
		t.Fatalf("summary %q", s)
	}
	if !strings.Contains(s, "1 artists synced") || !strings.Contains(s, "2 remaining") {
		t.Fatalf("summary should count completed and remaining artists: %q", s)
	}
	if strings.Contains(s, "token") || strings.Contains(s, "api.spotify.com") {
		t.Fatalf("summary leaks raw error: %q", s)
	}
	if want := []string{"lock", "backup", "sync:gza", "sync:rza", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("remaining artists must not run after the rate limit: %v", fx.log.get())
	}
}

func TestSyncRateLimitOnWholeCatalogCountsRemaining(t *testing.T) {
	fx := newSyncFixture(true)
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 7, ArtistsProcessed: 3, ArtistsFailed: 1, RunID: 2}}
	fx.runner.errs[""] = &catalogsync.PartialSyncError{Err: retryAfterErr{1500 * time.Millisecond}}

	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"})
	if err != nil || out.Status != jobs.StatusPartial || !out.Resumable {
		t.Fatalf("want partial resumable: %+v %v", out, err)
	}
	if s := out.Result.Summary; !strings.HasPrefix(s, "Spotify rate limit reached; retry after 2s") ||
		!strings.Contains(s, "3 artists synced") || !strings.Contains(s, "4 remaining") {
		t.Fatalf("summary %q", s)
	}
}

func TestSyncFatalErrorFails(t *testing.T) {
	fx := newSyncFixture(true)
	fx.runner.errs["gza"] = errors.New("begin sync run: disk I/O error at /secret/catalog.db")

	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "selected", "artists": "gza,rza"})
	if safeCode(err) != "internal" || out.Status == jobs.StatusPartial {
		t.Fatalf("a fatal error must fail safely, got %+v %v", out, err)
	}
	if strings.Contains(err.Error(), "/secret") {
		t.Fatalf("leaked: %v", err)
	}
	if want := []string{"lock", "backup", "sync:gza", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
}

func TestSyncResumePassesResumeOption(t *testing.T) {
	fx := newSyncFixture(true)
	fx.resumable = &ResumableRun{ID: 12, CompletedSlugs: []string{"gza"}}
	a := NewSync(fx.deps)
	pv := previewSync(t, a, Params{"scope": "all", "resume": "true"})
	if !scopeHas(pv, "12") {
		t.Fatalf("preview should name the resumable run: %+v", pv.Scope)
	}
	if _, _, _, err := runAction(t, a, Params{"scope": "all", "resume": "true"}); err != nil {
		t.Fatal(err)
	}
	if !fx.runner.calls[0].Resume {
		t.Fatalf("resume option not passed: %+v", fx.runner.calls[0])
	}

	fresh := newSyncFixture(true)
	if _, _, _, err := runAction(t, NewSync(fresh.deps), Params{"scope": "all"}); err != nil {
		t.Fatal(err)
	}
	if fresh.runner.calls[0].Resume {
		t.Fatal("resume defaults to false")
	}
}

func TestSyncFingerprintTracksScopeResumeAndRun(t *testing.T) {
	fx := newSyncFixture(true)
	a := NewSync(fx.deps)
	fp := func(p Params) string { return previewSync(t, a, p).Fingerprint }

	base := fp(Params{"scope": "all", "resume": "true"})
	if base == fp(Params{"scope": "all"}) {
		t.Fatal("resume flag must change the fingerprint")
	}
	fx.resumable = &ResumableRun{ID: 3, CompletedSlugs: []string{"gza"}}
	withRun := fp(Params{"scope": "all", "resume": "true"})
	if withRun == base {
		t.Fatal("the resumable run must change the fingerprint")
	}
	fx.resumable.CompletedSlugs = []string{"gza", "rza"}
	if fp(Params{"scope": "all", "resume": "true"}) == withRun {
		t.Fatal("progress of the resumable run must change the fingerprint")
	}
	before := fp(Params{"scope": "all"})
	fx.syncable = fx.syncable[1:]
	if fp(Params{"scope": "all"}) == before {
		t.Fatal("in-scope slugs must change the fingerprint")
	}
	before = fp(Params{"scope": "all"})
	fx.digest = "d2"
	if fp(Params{"scope": "all"}) == before {
		t.Fatal("catalog digest must change the fingerprint")
	}
}

func TestSyncBacksUpDatabaseFirst(t *testing.T) {
	fx := newSyncFixture(true)
	if _, _, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "artist", "artists": "gza"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lock", "backup", "sync:gza", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v, want %v", fx.log.get(), want)
	}

	failing := newSyncFixture(true)
	failing.backuper.err = errors.New("vacuum into /secret/backups: disk full")
	_, _, _, err := runAction(t, NewSync(failing.deps), Params{"scope": "artist", "artists": "gza"})
	if safeCode(err) != "internal" || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("backup failure: %v", err)
	}
	if want := []string{"lock", "backup", "release"}; !slices.Equal(failing.log.get(), want) {
		t.Fatalf("no sync after a failed backup: %v", failing.log.get())
	}
}

func TestSyncDryRunTakesNoLockOrBackup(t *testing.T) {
	fx := newSyncFixture(false)
	a := NewSyncDryRun(fx.deps)
	if ok, _ := a.Available(); !ok {
		t.Fatal("a dry run needs no Spotify credentials")
	}
	_, out, _, err := runAction(t, a, Params{"scope": "selected", "artists": "gza,rza"})
	if err != nil || out.Status != jobs.StatusSucceeded || out.Resumable {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	if want := []string{"sync:gza", "sync:rza"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
	for _, c := range fx.runner.calls {
		if !c.DryRun {
			t.Fatalf("dry run option not set: %+v", c)
		}
	}
	if !strings.Contains(out.Result.Summary, "Dry run") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestSyncUnavailableWithoutCredentials(t *testing.T) {
	fx := newSyncFixture(false)
	a := NewSync(fx.deps)
	if ok, reason := a.Available(); ok || reason != "Spotify credentials are not configured" {
		t.Fatalf("available=%v reason=%q", ok, reason)
	}
	if _, err := a.Spec(context.Background(), Params{"scope": "all"}); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("spec: %v", err)
	}
}

func TestSyncStaleFingerprintAfterLockIsRefused(t *testing.T) {
	fx := newSyncFixture(true)
	a := NewSync(fx.deps)
	spec, err := a.Spec(context.Background(), Params{"scope": "all"})
	if err != nil {
		t.Fatal(err)
	}
	fx.digest = "changed while queued"

	_, err = spec.Run(context.Background(), &recReporter{})
	if safeCode(err) != "stale_preview" {
		t.Fatalf("want stale preview, got %v", err)
	}
	if want := []string{"lock", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
}

func TestSyncLockedFailsWithConflict(t *testing.T) {
	fx := newSyncFixture(true)
	fx.locker.err = fmt.Errorf("lock /secret/catalog.db.lock: %w", ErrLocked)
	_, _, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"})
	if safeCode(err) != "conflict" || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("want safe conflict, got %v", err)
	}
	if want := []string{"lock"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("calls = %v", fx.log.get())
	}
}

func TestSyncCancelledKeepsCheckpoints(t *testing.T) {
	fx := newSyncFixture(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.runner.onRun = func(string) { cancel() }

	spec, err := NewSync(fx.deps).Spec(context.Background(), Params{"scope": "selected", "artists": "gza,rza,odb"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := spec.Run(ctx, &recReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled so the manager marks the job cancelled, got %v", err)
	}
	if !out.Resumable {
		t.Fatal("a cancelled sync keeps its checkpoints and can be resumed")
	}
	if want := []string{"lock", "backup", "sync:gza", "release"}; !slices.Equal(fx.log.get(), want) {
		t.Fatalf("remaining artists must be skipped after the current one: %v", fx.log.get())
	}
}

func TestSyncSelectedRejectsUnknownSlug(t *testing.T) {
	fx := newSyncFixture(true)
	a := NewSync(fx.deps)
	p := Params{"scope": "selected", "artists": "gza,cappadonna"}
	if err := a.Validate(p); err != nil {
		t.Fatalf("well-formed slugs pass static validation: %v", err)
	}
	_, err := a.Preview(context.Background(), p)
	var se *SafeError
	if !errors.As(err, &se) || se.Code != "validation" || !strings.Contains(se.Message, `"cappadonna"`) {
		t.Fatalf("want validation error naming the slug, got %v", err)
	}
	if _, err := a.Spec(context.Background(), p); safeCode(err) != "validation" {
		t.Fatalf("spec must refuse the unknown slug too: %v", err)
	}
	if len(fx.log.get()) != 0 {
		t.Fatalf("nothing should run: %v", fx.log.get())
	}
}

func TestSyncValidateParams(t *testing.T) {
	a := NewSync(newSyncFixture(true).deps)
	bad := []Params{
		{},
		{"scope": "everything"},
		{"scope": "all", "artists": "gza"},
		{"scope": "artist"},
		{"scope": "artist", "artists": "gza,rza"},
		{"scope": "selected"},
		{"scope": "selected", "artists": " , "},
		{"scope": "selected", "artists": "Not A Slug"},
		{"scope": "selected", "artists": "gza,gza"},
		{"scope": "all", "resume": "yes"},
		{"scope": "all", "full": "true"},
	}
	for _, p := range bad {
		if err := a.Validate(p); safeCode(err) != "validation" {
			t.Errorf("Validate(%v) = %v, want validation error", p, err)
		}
	}
	for _, p := range []Params{{"scope": "all"}, {"scope": "artist", "artists": "gza"}, {"scope": "selected", "artists": "gza, rza"}, {"scope": "all", "resume": "false"}} {
		if err := a.Validate(p); err != nil {
			t.Errorf("Validate(%v) = %v", p, err)
		}
	}
}
