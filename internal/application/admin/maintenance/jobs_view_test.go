package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

func runToEnd(t *testing.T, s *Service, m *jobs.Manager, name string) jobs.Job {
	t.Helper()
	_, tok, err := s.Preview(context.Background(), name, Params{"scope": "all"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Run(context.Background(), name, Params{"scope": "all"}, tok, "")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	return j
}

func TestServiceJobsListsNewestFirstAndGets(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	s, m, _ := newTestService(t, a, b)
	first := runToEnd(t, s, m, "a")
	second := runToEnd(t, s, m, "b")

	list := s.Jobs()
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("want newest first, got %+v", list)
	}
	got, ok := s.Job(first.ID)
	if !ok || got.Kind != "a" || got.Status != jobs.StatusSucceeded {
		t.Fatalf("Job(%s) = %+v, %v", first.ID, got, ok)
	}
	if _, ok := s.Job("missing"); ok {
		t.Fatal("unknown job must not be found")
	}
}

func TestServiceCancelJob(t *testing.T) {
	a := newFake("a")
	release := make(chan struct{})
	a.run = func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
		select {
		case <-ctx.Done():
			return jobs.Outcome{}, ctx.Err()
		case <-release:
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}
	}
	s, m, _ := newTestService(t, a)
	_, tok, err := s.Preview(context.Background(), "a", Params{"scope": "all"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Run(context.Background(), "a", Params{"scope": "all"}, tok, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelJob(j.ID); err != nil {
		t.Fatalf("cancel active job: %v", err)
	}
	m.Wait(j.ID)
	close(release)
	if got, _ := s.Job(j.ID); got.Status != jobs.StatusCancelled {
		t.Fatalf("want cancelled, got %s", got.Status)
	}
	if err := s.CancelJob(j.ID); !errors.Is(err, ErrJobFinished) {
		t.Fatalf("cancel finished job: want ErrJobFinished, got %v", err)
	}
	if err := s.CancelJob("missing"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("cancel unknown job: want jobs.ErrNotFound, got %v", err)
	}
}

func TestServiceJobReports(t *testing.T) {
	withReport, without := newFake("with"), newFake("without")
	report := jobs.Report{Name: "r", Columns: []string{"slug", "note"}, Rows: [][]string{{"zeta", "=1"}, {"alpha", "ok"}}}
	withReport.run = func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		r := report
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: "done", Report: &r}}, nil
	}
	s, m, _ := newTestService(t, withReport, without)
	j1 := runToEnd(t, s, m, "with")
	j2 := runToEnd(t, s, m, "without")

	var got, want bytes.Buffer
	if err := s.JobReportCSV(&got, j1.ID); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSV(&want, report); err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Fatalf("csv mismatch:\n%s\nwant\n%s", got.String(), want.String())
	}
	got.Reset()
	want.Reset()
	if err := s.JobReportJSON(&got, j1.ID); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&want, report); err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Fatalf("json mismatch:\n%s\nwant\n%s", got.String(), want.String())
	}

	if err := s.JobReportCSV(&got, j2.ID); !errors.Is(err, ErrNoReport) {
		t.Fatalf("want ErrNoReport, got %v", err)
	}
	if err := s.JobReportJSON(&got, "missing"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("want jobs.ErrNotFound, got %v", err)
	}
}

func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		status    jobs.Status
		resumable bool
		want      bool
	}{
		{jobs.StatusFailed, false, true},
		{jobs.StatusFailed, true, true},
		{jobs.StatusPartial, true, true},
		{jobs.StatusPartial, false, false},
		{jobs.StatusInterrupted, true, true},
		{jobs.StatusInterrupted, false, false},
		{jobs.StatusCancelled, true, true},
		{jobs.StatusCancelled, false, false},
		{jobs.StatusSucceeded, true, false},
		{jobs.StatusQueued, true, false},
		{jobs.StatusRunning, true, false},
		{jobs.StatusCancelling, true, false},
	} {
		j := jobs.Job{ID: "job-1", Kind: "sync", Status: tc.status, Resumable: tc.resumable}
		if got := Retryable(j); got != tc.want {
			t.Errorf("Retryable(%s, resumable=%v) = %v, want %v", tc.status, tc.resumable, got, tc.want)
		}
	}
}

func TestResumeEligible(t *testing.T) {
	failing, ok := newFake("a"), newFake("b")
	failing.run = func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{}, errors.New("boom")
	}
	s, m, _ := newTestService(t, failing, ok)
	failed := runToEnd(t, s, m, "a")
	succeeded := runToEnd(t, s, m, "b")
	if got, _ := s.Job(failed.ID); got.Status != jobs.StatusFailed {
		t.Fatalf("setup: want failed job, got %s", got.Status)
	}

	if !s.ResumeEligible(failed.ID, "a") {
		t.Fatal("a failed job of the same action must be eligible")
	}
	for _, tc := range []struct{ id, action, why string }{
		{failed.ID, "b", "kind mismatch"},
		{failed.ID, "", "empty action"},
		{succeeded.ID, "b", "succeeded job"},
		{"missing", "a", "unknown job"},
		{"", "a", "empty id"},
	} {
		if s.ResumeEligible(tc.id, tc.action) {
			t.Errorf("%s: must not be eligible", tc.why)
		}
	}
}

func TestResumeEligibleRequiresRegisteredAction(t *testing.T) {
	failing := newFake("a")
	failing.run = func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
		return jobs.Outcome{}, errors.New("boom")
	}
	var n int
	m := jobs.NewManager(&memStore{}, time.Now, func() string { n++; return fmt.Sprintf("job-%d", n) })
	full := NewService(m, []Action{failing}, []byte("k"), time.Now)
	j := runToEnd(t, full, m, "a")
	// A service sharing the manager but not registering "a" never resumes it.
	other := NewService(m, nil, []byte("k"), time.Now)
	if other.ResumeEligible(j.ID, "a") {
		t.Fatal("an action the service does not register is never eligible")
	}
}
