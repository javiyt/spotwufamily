package maintenance

import (
	"bytes"
	"context"
	"errors"
	"testing"

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
