package maintenance

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// statusEventually polls ReleaseStatus until it has a result.
func statusAfterCheck(t *testing.T, s *Service) ReleaseStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := s.ReleaseStatus()
		if !ok {
			t.Fatal("release status must be available with release actions registered")
		}
		if !st.Checked.IsZero() && !st.Checking {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the background release check never finished")
	return ReleaseStatus{}
}

func TestReleaseStatusNeverWaitsForThePreflight(t *testing.T) {
	fx := newReleaseFixture()
	br := &blockingReleaser{fakeReleaser: fx.releaser, entered: make(chan struct{}, 1), release: make(chan struct{})}
	fx.deps.Releaser = br
	s, _, _ := newTestService(t, NewReleasePrepare(fx.deps), NewReleasePublish(fx.deps))

	done := make(chan ReleaseStatus, 1)
	go func() { st, _ := s.ReleaseStatus(); done <- st }()
	var st ReleaseStatus
	select {
	case st = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReleaseStatus blocked on the preflight")
	}
	if !st.Checked.IsZero() || !st.Checking {
		t.Fatalf("before the first check: %+v", st)
	}
	if !slices.Equal(st.PublishBlockers, []string{releaseCheckingReason}) ||
		!slices.Equal(st.PrepareBlockers, []string{releaseCheckingReason}) {
		t.Fatalf("nothing is ready while checking: %+v", st)
	}
	<-br.entered
	// Asking again while the check hangs neither blocks nor starts another.
	go func() { st, _ := s.ReleaseStatus(); done <- st }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second ReleaseStatus blocked")
	}
	if n := br.calls.Load(); n != 1 {
		t.Fatalf("single-flight background check, got %d", n)
	}
	close(br.release)
	st = statusAfterCheck(t, s)
	if st.Prereq.LocalContentSHA256 != shaLocal || st.Prereq.LastReleaseTag != "catalog-old" {
		t.Fatalf("cached preflight not reported: %+v", st.Prereq)
	}
}

func TestReleaseStatusReadiness(t *testing.T) {
	fx := newReleaseFixture()
	s, _, _ := newTestService(t, NewReleasePrepare(fx.deps), NewReleasePublish(fx.deps))
	st := statusAfterCheck(t, s)
	if len(st.PrepareBlockers) != 0 {
		t.Fatalf("prepare is ready: %v", st.PrepareBlockers)
	}
	if !slices.Equal(st.PublishBlockers, []string{nothingPreparedWarning}) || st.PreparedMatchesLocal {
		t.Fatalf("publish needs a prepare first: %+v", st)
	}
	if st.PackageName != "build/catalog-release" || st.ManifestName != "data/catalog.release.json" || st.Repo != "javiyt/spotwufamily" {
		t.Fatalf("display names: %+v", st)
	}

	fx2 := newReleaseFixture()
	fx2.prepared()
	s2, _, _ := newTestService(t, NewReleasePrepare(fx2.deps), NewReleasePublish(fx2.deps))
	st = statusAfterCheck(t, s2)
	if len(st.PublishBlockers) != 0 || !st.PreparedMatchesLocal || st.LastPrepared != shaLocal {
		t.Fatalf("a matching prepared package is publishable: %+v", st)
	}
	if len(st.PrepareBlockers) != 1 || st.PrepareBlockers[0] == "" {
		t.Fatalf("the full package directory blocks another prepare: %v", st.PrepareBlockers)
	}

	// A package prepared for other content is stale: publishing is blocked.
	fx3 := newReleaseFixture()
	fx3.prepared()
	fx3.releaser.set(func(p *Prereq) { p.PreparedContentSHA256 = shaPinned })
	fx3.releaser.set(func(p *Prereq) { p.GhAuthenticated = false })
	s3, _, _ := newTestService(t, NewReleasePrepare(fx3.deps), NewReleasePublish(fx3.deps))
	st = statusAfterCheck(t, s3)
	if !slices.Equal(st.PublishBlockers, []string{ghNotAuthenticatedReason, nothingPreparedWarning}) || st.PreparedMatchesLocal {
		t.Fatalf("stale package and missing gh login both block: %+v", st.PublishBlockers)
	}

	fx4 := newReleaseFixture()
	fx4.releaser.preflightErr = context.DeadlineExceeded
	s4, _, _ := newTestService(t, NewReleasePrepare(fx4.deps), NewReleasePublish(fx4.deps))
	st = statusAfterCheck(t, s4)
	if !st.CheckFailed || !slices.Equal(st.PublishBlockers, []string{releaseScriptReason}) ||
		!slices.Equal(st.PrepareBlockers, []string{releaseScriptReason}) {
		t.Fatalf("a failed check blocks both: %+v", st)
	}
}

func TestReleaseStatusWithoutReleaseActions(t *testing.T) {
	s, _, _ := newTestService(t, newFake("a"))
	if _, ok := s.ReleaseStatus(); ok {
		t.Fatal("no release actions, no status")
	}
	s.RecheckRelease() // must not panic
}

func TestRecheckReleaseForcesABackgroundCheck(t *testing.T) {
	fx := newReleaseFixture()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fx.deps.Now = func() time.Time { return now }
	pub := NewReleasePublish(fx.deps)
	s, _, _ := newTestService(t, pub)
	statusAfterCheck(t, s)
	statusAfterCheck(t, s)
	if n := len(fx.log.get()); n != 1 {
		t.Fatalf("a recent check is reused: %v", fx.log.get())
	}
	fx.releaser.set(func(p *Prereq) { p.GhAuthenticated = false })
	s.RecheckRelease()
	st := statusAfterCheck(t, s)
	if n := len(fx.log.get()); n != 2 {
		t.Fatalf("re-check runs a new preflight within the TTL: %v", fx.log.get())
	}
	if st.Prereq.GhAuthenticated {
		t.Fatal("the re-check result is reported")
	}
}

// panickingReleaser panics in Preflight.
type panickingReleaser struct{ fakeReleaser }

func (p *panickingReleaser) Preflight(context.Context) (Prereq, error) { panic("boom") }

func TestReleaseBackgroundCheckPanicIsAFailedCheck(t *testing.T) {
	fx := newReleaseFixture()
	fx.deps.Releaser = &panickingReleaser{}
	a := NewReleasePublish(fx.deps).(*releaseAction)
	a.AvailableNow()
	a.waitRefresh()
	if ok, reason := a.AvailableNow(); ok || reason != releaseScriptReason {
		t.Fatalf("a panicking check is a failed check: %v %q", ok, reason)
	}
	// The synchronous path recovers too.
	b := NewReleasePrepare(fx.deps)
	if ok, reason := b.Available(); ok || reason != releaseScriptReason {
		t.Fatalf("sync check: %v %q", ok, reason)
	}
	// The single-flight lock was released: another check can run.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); a.refresh() }()
	wg.Wait()
}
