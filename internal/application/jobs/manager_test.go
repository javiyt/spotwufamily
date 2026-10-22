package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

type fakeStore struct {
	mu    sync.Mutex
	saved []jobs.Job
	load  []jobs.Job
}

func (s *fakeStore) Save(_ context.Context, j jobs.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, j)
	return nil
}

func (s *fakeStore) Load(context.Context) ([]jobs.Job, error) { return s.load, nil }

func (s *fakeStore) statuses(id string) []jobs.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []jobs.Status
	for _, j := range s.saved {
		if j.ID == id {
			out = append(out, j.Status)
		}
	}
	return out
}

func newManager(store jobs.Store, opts ...jobs.Option) *jobs.Manager {
	var n int
	var mu sync.Mutex
	return jobs.NewManager(store, time.Now, func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("job-%d", n)
	}, opts...)
}

func blocking(started chan<- struct{}, release <-chan struct{}) jobs.Func {
	return func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
		r.Step("working")
		r.Progress(1, 2)
		r.Count("items", 3)
		close(started)
		select {
		case <-release:
			return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: "ok"}}, nil
		case <-ctx.Done():
			return jobs.Outcome{}, ctx.Err()
		}
	}
}

func TestSubmitRunsAndRecordsProgress(t *testing.T) {
	store := &fakeStore{}
	m := newManager(store)
	started, release := make(chan struct{}), make(chan struct{})

	j, err := m.Submit(context.Background(), jobs.Spec{Kind: "sync", Resources: []string{"catalog"}, Run: blocking(started, release)})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	got, ok := m.Get(j.ID)
	if !ok || got.Status != jobs.StatusRunning || got.Step != "working" || got.Done != 1 || got.Total != 2 || got.Counts["items"] != 3 {
		t.Fatalf("unexpected running job: %+v", got)
	}
	close(release)
	m.Wait(j.ID)

	got, _ = m.Get(j.ID)
	if got.Status != jobs.StatusSucceeded || got.Result == nil || got.Result.Summary != "ok" || got.FinishedAt.IsZero() {
		t.Fatalf("unexpected final job: %+v", got)
	}
	want := []jobs.Status{jobs.StatusQueued, jobs.StatusRunning, jobs.StatusSucceeded}
	if s := store.statuses(j.ID); fmt.Sprint(s) != fmt.Sprint(want) {
		t.Fatalf("persisted statuses = %v, want %v", s, want)
	}
}

func TestConflictingResourceRejected(t *testing.T) {
	m := newManager(&fakeStore{})
	started, release := make(chan struct{}), make(chan struct{})
	first, err := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x", "y"}, Run: blocking(started, release)})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	_, err = m.Submit(context.Background(), jobs.Spec{Kind: "b", Resources: []string{"z", "y"}, Run: blocking(make(chan struct{}), release)})
	var ce *jobs.ConflictError
	if !errors.As(err, &ce) || ce.HolderID != first.ID || !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("err = %v, want conflict held by %s", err, first.ID)
	}
	close(release)
	m.Wait(first.ID)
}

func TestDisjointResourcesRunConcurrently(t *testing.T) {
	m := newManager(&fakeStore{})
	release := make(chan struct{})
	s1, s2 := make(chan struct{}), make(chan struct{})
	a, err := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: blocking(s1, release)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Submit(context.Background(), jobs.Spec{Kind: "b", Resources: []string{"y"}, Run: blocking(s2, release)})
	if err != nil {
		t.Fatal(err)
	}
	<-s1
	<-s2
	close(release)
	m.Wait(a.ID)
	m.Wait(b.ID)
	if l := m.List(); len(l) != 2 || l[0].ID != b.ID {
		t.Fatalf("list should be newest first: %+v", l)
	}
}

func TestCancelMarksCancelledWithPartialCounts(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: blocking(started, make(chan struct{}))})
	<-started
	if err := m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusCancelled || got.Counts["items"] != 3 || got.Done != 1 {
		t.Fatalf("unexpected: %+v", got)
	}
	if err := m.Cancel("missing"); err == nil {
		t.Fatal("expected error for unknown job")
	}
}

type safeErr struct{}

func (safeErr) Error() string { return "/Users/x/secret" }
func (safeErr) Safe() string  { return "catalog is locked" }

func TestFailedJobSanitisesMessage(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("open /Users/x/secret: denied"), "operation failed"},
		{safeErr{}, "catalog is locked"},
	} {
		m := newManager(&fakeStore{})
		j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{}, tc.err
		}})
		m.Wait(j.ID)
		got, _ := m.Get(j.ID)
		if got.Status != jobs.StatusFailed || got.Message != tc.want {
			t.Fatalf("got %+v, want message %q", got, tc.want)
		}
	}
}

func TestCustomSanitizerAndOnFinish(t *testing.T) {
	var finished []jobs.Job
	m := newManager(&fakeStore{},
		jobs.WithSanitizer(func(error) string { return "custom" }),
		jobs.WithOnFinish(func(j jobs.Job) { finished = append(finished, j) }))
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{}, errors.New("boom")
	}})
	m.Wait(j.ID)
	if len(finished) != 1 || finished[0].Message != "custom" || finished[0].Status != jobs.StatusFailed {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestPanicBecomesFailed(t *testing.T) {
	m := newManager(&fakeStore{})
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		panic("secret /Users/x")
	}})
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusFailed || got.Message != "operation failed" {
		t.Fatalf("got %+v", got)
	}
}

func TestResourceReleasedAfterFinish(t *testing.T) {
	m := newManager(&fakeStore{})
	run := func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{Status: jobs.StatusPartial, Resumable: true}, nil
	}
	a, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: run})
	m.Wait(a.ID)
	b, err := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, ResumeOf: a.ID, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(b.ID)
	got, _ := m.Get(b.ID)
	if got.Status != jobs.StatusPartial || !got.Resumable || got.ResumeOf != a.ID {
		t.Fatalf("got %+v", got)
	}
}

func TestRecoverMarksActiveJobsInterrupted(t *testing.T) {
	store := &fakeStore{load: []jobs.Job{
		{ID: "old-1", Status: jobs.StatusRunning, Resumable: true},
		{ID: "old-2", Status: jobs.StatusSucceeded},
	}}
	m := newManager(store)
	interrupted, err := m.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != "old-1" || interrupted[0].Status != jobs.StatusInterrupted || interrupted[0].Message == "" {
		t.Fatalf("Recover must return the jobs it interrupted: %+v", interrupted)
	}
	a, _ := m.Get("old-1")
	b, _ := m.Get("old-2")
	if a.Status != jobs.StatusInterrupted || a.FinishedAt.IsZero() || b.Status != jobs.StatusSucceeded {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if s := store.statuses("old-1"); len(s) != 1 || s[0] != jobs.StatusInterrupted {
		t.Fatalf("not persisted: %v", s)
	}
}

func TestRecoverMarksRunningAndQueuedInterrupted(t *testing.T) {
	store := &fakeStore{load: []jobs.Job{
		{ID: "r", Status: jobs.StatusRunning, Resumable: true, Resources: []string{"x"}},
		{ID: "q", Status: jobs.StatusQueued},
	}}
	m := newManager(store)
	if interrupted, err := m.Recover(context.Background()); err != nil || len(interrupted) != 2 {
		t.Fatalf("interrupted %+v err %v", interrupted, err)
	}
	for _, id := range []string{"r", "q"} {
		j, _ := m.Get(id)
		if j.Status != jobs.StatusInterrupted || j.FinishedAt.IsZero() || j.Message == "" {
			t.Fatalf("%s: %+v", id, j)
		}
	}
	r, _ := m.Get("r")
	if !r.Resumable {
		t.Fatalf("resumable lost: %+v", r)
	}
	// resources must not be held after recovery
	started := make(chan struct{})
	release := make(chan struct{})
	close(release)
	if _, err := m.Submit(context.Background(), jobs.Spec{Kind: "k", Resources: []string{"x"}, Run: blocking(started, release)}); err != nil {
		t.Fatalf("resource still held: %v", err)
	}
}

func TestRecoverKeepsTerminalJobs(t *testing.T) {
	fin := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &fakeStore{load: []jobs.Job{
		{ID: "a", Status: jobs.StatusFailed, Message: "boom", FinishedAt: fin},
		{ID: "b", Status: jobs.StatusPartial, Resumable: true, FinishedAt: fin},
	}}
	m := newManager(store)
	if interrupted, err := m.Recover(context.Background()); err != nil || len(interrupted) != 0 {
		t.Fatalf("interrupted %+v err %v", interrupted, err)
	}
	a, _ := m.Get("a")
	b, _ := m.Get("b")
	if a.Status != jobs.StatusFailed || a.Message != "boom" || !a.FinishedAt.Equal(fin) || b.Status != jobs.StatusPartial || !b.Resumable {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if len(store.saved) != 0 {
		t.Fatalf("terminal jobs rewritten: %v", store.saved)
	}
}

// finalAfterCancel waits for a cancel request, then returns out/err: work
// that could not be stopped once started.
func finalAfterCancel(started chan<- struct{}, out jobs.Outcome, err error) jobs.Func {
	return func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
		close(started)
		<-ctx.Done()
		return out, err
	}
}

func TestFinalOutcomeIsRecordedDespiteCancel(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	out := jobs.Outcome{Status: jobs.StatusSucceeded, Final: true, Result: jobs.Result{Summary: "done anyway"}}
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: finalAfterCancel(started, out, nil)})
	<-started
	if err := m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusSucceeded || got.Result == nil || got.Result.Summary != "done anyway" {
		t.Fatalf("a final outcome must survive the cancel: %+v", got)
	}
}

func TestFinalErrorIsRecordedAsFailedDespiteCancel(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"},
		Run: finalAfterCancel(started, jobs.Outcome{}, &jobs.FinalError{Err: safeErr{}})})
	<-started
	if err := m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusFailed || got.Message != "catalog is locked" {
		t.Fatalf("a final error must be recorded as failed with its safe message: %+v", got)
	}
}

func TestOutcomeFinishedAfterCancelRequestIsKept(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	out := jobs.Outcome{Status: jobs.StatusSucceeded, Resumable: false, Result: jobs.Result{Summary: "committed"}}
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: finalAfterCancel(started, out, nil)})
	<-started
	_ = m.Cancel(j.ID)
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusSucceeded || got.Result == nil || got.Result.Summary != "committed" {
		t.Fatalf("work that finished after a cancel request must keep its outcome: %+v", got)
	}
}

func TestPartialOutcomeAfterCancelRequestIsKept(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	out := jobs.Outcome{Status: jobs.StatusPartial, Resumable: true, Result: jobs.Result{Summary: "some"}}
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: finalAfterCancel(started, out, nil)})
	<-started
	_ = m.Cancel(j.ID)
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusPartial || !got.Resumable || got.Result == nil || got.Result.Summary != "some" {
		t.Fatalf("got %+v", got)
	}
}

func TestContextErrorAfterCancelRequestIsCancelledWithCounts(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"},
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			r.Count("done", 2)
			close(started)
			<-ctx.Done()
			return jobs.Outcome{Status: jobs.StatusPartial, Resumable: true}, ctx.Err()
		}})
	<-started
	_ = m.Cancel(j.ID)
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusCancelled || got.Counts["done"] != 2 || !got.Resumable || got.Result != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestAcquireConflictsWithARunningJob(t *testing.T) {
	m := newManager(&fakeStore{})
	started, release := make(chan struct{}), make(chan struct{})
	j, err := m.Submit(context.Background(), jobs.Spec{Kind: "refresh", Resources: []string{"catalog-yaml"}, Run: blocking(started, release)})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := m.Acquire("catalog-yaml"); err == nil {
		t.Fatal("an edit must not take the catalog while a job holds it")
	} else {
		var ce *jobs.ConflictError
		if !errors.As(err, &ce) || ce.HolderID != j.ID || !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("got %v", err)
		}
	}
	close(release)
	m.Wait(j.ID)
	done, err := m.Acquire("catalog-yaml")
	if err != nil {
		t.Fatalf("free after the job: %v", err)
	}
	done()
}

func TestSubmitConflictsWithAnAcquiredResource(t *testing.T) {
	m := newManager(&fakeStore{})
	release, err := m.Acquire("catalog-yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Submit(context.Background(), jobs.Spec{Kind: "discover-import", Resources: []string{"catalog-yaml"},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}})
	var ce *jobs.ConflictError
	if !errors.As(err, &ce) || ce.HolderID != jobs.EditHolder {
		t.Fatalf("a job must not start while an edit holds the catalog: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatal("nothing was started")
	}
	// Edits share the resource with each other.
	second, err := m.Acquire("catalog-yaml")
	if err != nil {
		t.Fatalf("concurrent edits share the resource: %v", err)
	}
	release()
	release() // idempotent
	if _, err := m.Submit(context.Background(), jobs.Spec{Kind: "x", Resources: []string{"catalog-yaml"},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}}); err == nil {
		t.Fatal("still held by the second edit")
	}
	second()
	j, err := m.Submit(context.Background(), jobs.Spec{Kind: "x", Resources: []string{"catalog-yaml"},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}})
	if err != nil {
		t.Fatalf("free once every edit released: %v", err)
	}
	m.Wait(j.ID)
}

// ctxStore mimics the filesystem journal: it refuses a canceled context.
type ctxStore struct {
	fakeStore
	failFirst error
}

func (s *ctxStore) Save(ctx context.Context, j jobs.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failFirst != nil {
		err := s.failFirst
		s.failFirst = nil
		return err
	}
	return s.fakeStore.Save(ctx, j)
}

func TestSubmitSurvivesCanceledRequestContext(t *testing.T) {
	store := &ctxStore{}
	m := newManager(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client disconnected before the queued state was journaled

	j, err := m.Submit(ctx, jobs.Spec{Kind: "k", Run: func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: "ok"}}, ctx.Err()
	}})
	if err != nil {
		t.Fatalf("accepted submit must not depend on the request context: %v", err)
	}
	m.Wait(j.ID)
	got, ok := m.Get(j.ID)
	if !ok || got.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v, %v", got, ok)
	}
	st := store.statuses(j.ID)
	if len(st) < 2 || st[0] != jobs.StatusQueued {
		t.Fatalf("journal states = %v", st)
	}
}

func TestSubmitJournalFailureIsHonest(t *testing.T) {
	store := &ctxStore{failFirst: errors.New("disk full")}
	m := newManager(store)
	ran := make(chan struct{}, 1)
	spec := jobs.Spec{Kind: "k", Resources: []string{"db"}, Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		ran <- struct{}{}
		return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
	}}
	if _, err := m.Submit(context.Background(), spec); err == nil {
		t.Fatal("want an error when the queued state cannot be journaled")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("no phantom job expected, got %+v", got)
	}
	select {
	case <-ran:
		t.Fatal("job ran although it was never accepted")
	default:
	}
	// The resource was not leaked: a retry is accepted.
	j, err := m.Submit(context.Background(), spec)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	m.Wait(j.ID)
}

// A job that fails or is cancelled may still return a result (an
// all-or-nothing operation listing what it rolled back); it is kept.
func TestFailedOrCancelledJobKeepsItsResult(t *testing.T) {
	rep := &jobs.Report{Name: "r", Columns: []string{"artist", "outcome"}, Rows: [][]string{{"a", "rolled_back"}}}
	m := newManager(&fakeStore{})
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{Result: jobs.Result{Summary: "rolled back", Report: rep}}, safeErr{}
	}})
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	if got.Status != jobs.StatusFailed || got.Result == nil || got.Result.Summary != "rolled back" || got.Result.Report == nil {
		t.Fatalf("got %+v", got)
	}

	started := make(chan struct{})
	j, _ = m.Submit(context.Background(), jobs.Spec{Kind: "b", Run: func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
		close(started)
		<-ctx.Done()
		return jobs.Outcome{Result: jobs.Result{Summary: "cancelled, rolled back", Report: rep}}, ctx.Err()
	}})
	<-started
	_ = m.Cancel(j.ID)
	m.Wait(j.ID)
	got, _ = m.Get(j.ID)
	if got.Status != jobs.StatusCancelled || got.Result == nil || got.Result.Summary != "cancelled, rolled back" {
		t.Fatalf("got %+v", got)
	}
}
