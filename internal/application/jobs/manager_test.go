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
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
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
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
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
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
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

func TestNonFinalOutcomeStillCancelled(t *testing.T) {
	m := newManager(&fakeStore{})
	started := make(chan struct{})
	out := jobs.Outcome{Status: jobs.StatusSucceeded}
	j, _ := m.Submit(context.Background(), jobs.Spec{Kind: "a", Resources: []string{"x"}, Run: finalAfterCancel(started, out, nil)})
	<-started
	_ = m.Cancel(j.ID)
	m.Wait(j.ID)
	if got, _ := m.Get(j.ID); got.Status != jobs.StatusCancelled {
		t.Fatalf("got %+v", got)
	}
}
