package maintenance

import (
	"errors"
	"io"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

var (
	// ErrNoReport is returned when a job exists but has no report attached.
	ErrNoReport = errors.New("job has no report")
	// ErrJobFinished is returned when cancelling a job that already finished.
	ErrJobFinished = errors.New("job already finished")
)

// Jobs lists every known job, newest first.
func (s *Service) Jobs() []jobs.Job { return s.jobs.List() }

// Job returns a snapshot of one job.
func (s *Service) Job(id string) (jobs.Job, bool) { return s.jobs.Get(id) }

// CancelJob requests cancellation of an active job. It returns
// jobs.ErrNotFound for an unknown job and ErrJobFinished for a finished one.
func (s *Service) CancelJob(id string) error {
	j, ok := s.jobs.Get(id)
	if !ok {
		return jobs.ErrNotFound
	}
	if j.Status.Terminal() {
		return ErrJobFinished
	}
	if err := s.jobs.Cancel(id); err != nil {
		// The job may have finished between the check and the request.
		if j, ok := s.jobs.Get(id); ok && j.Status.Terminal() {
			return ErrJobFinished
		}
		return err
	}
	return nil
}

// JobReportCSV writes the job's report as deterministic CSV (see WriteCSV).
func (s *Service) JobReportCSV(w io.Writer, id string) error {
	r, err := s.report(id)
	if err != nil {
		return err
	}
	return WriteCSV(w, r)
}

// JobReportJSON writes the job's report as deterministic JSON (see WriteJSON).
func (s *Service) JobReportJSON(w io.Writer, id string) error {
	r, err := s.report(id)
	if err != nil {
		return err
	}
	return WriteJSON(w, r)
}

func (s *Service) report(id string) (jobs.Report, error) {
	j, ok := s.jobs.Get(id)
	if !ok {
		return jobs.Report{}, jobs.ErrNotFound
	}
	if j.Result == nil || j.Result.Report == nil {
		return jobs.Report{}, ErrNoReport
	}
	return *j.Result.Report, nil
}
