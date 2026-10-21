package httpadmin

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Maintenance is the set of background-job use cases the adapter consumes. A
// Service that also implements it gets the /jobs pages and API.
type Maintenance interface {
	// Jobs lists every known job, newest first.
	Jobs() []jobs.Job
	Job(id string) (jobs.Job, bool)
	// CancelJob returns jobs.ErrNotFound or maintenance.ErrJobFinished for the
	// expected refusals.
	CancelJob(id string) error
	// JobReportCSV and JobReportJSON return jobs.ErrNotFound or
	// maintenance.ErrNoReport for the expected refusals.
	JobReportCSV(w io.Writer, id string) error
	JobReportJSON(w io.Writer, id string) error
	// ResumeEligible reports whether job id exists, was run by action and may
	// be resumed or retried. The adapter never decides that on its own.
	ResumeEligible(id, action string) bool
}

var (
	// jobIDPattern is the job ID charset; anything else is rejected before lookup.
	jobIDPattern = regexp.MustCompile(`^[0-9A-Fa-f][0-9A-Fa-f-]{0,63}$`)
	// actionPattern is an action name safe for a URL path segment and a filename.
	actionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

const shortIDLen = 8

func (s *Server) registerMaintenance() {
	s.routes["/jobs"] = route{methods: []string{http.MethodGet}, handler: s.handleJobs}
	s.routes["/api/v1/jobs"] = route{methods: []string{http.MethodGet}, handler: s.apiJobs}
	if s.dynamic == nil {
		s.dynamic = http.NewServeMux()
	}
	s.dynamic.HandleFunc("GET /jobs/{id}", s.handleJob)
	s.dynamic.HandleFunc("GET /api/v1/jobs/{id}", s.apiJob)
	s.dynamic.HandleFunc("POST /jobs/{id}/cancel", s.handleJobCancel)
	s.dynamic.HandleFunc("POST /jobs/{id}/retry", s.handleJobRetry)
	s.dynamic.HandleFunc("GET /jobs/{id}/report.csv", func(w http.ResponseWriter, r *http.Request) {
		s.handleJobReport(w, r, "csv", "text/csv; charset=utf-8", s.maint.JobReportCSV)
	})
	s.dynamic.HandleFunc("GET /jobs/{id}/report.json", func(w http.ResponseWriter, r *http.Request) {
		s.handleJobReport(w, r, "json", "application/json", s.maint.JobReportJSON)
	})
}

// lookupJob validates the path ID and loads the job, answering 404 otherwise.
func (s *Server) lookupJob(w http.ResponseWriter, r *http.Request) (jobs.Job, bool) {
	id := r.PathValue("id")
	if !jobIDPattern.MatchString(id) {
		s.fail(w, r, http.StatusNotFound, "not_found", "job not found")
		return jobs.Job{}, false
	}
	j, ok := s.maint.Job(id)
	if !ok {
		s.fail(w, r, http.StatusNotFound, "not_found", "job not found")
		return jobs.Job{}, false
	}
	return j, true
}

// --- view model ---

var statusLabels = map[jobs.Status]string{
	jobs.StatusQueued: "Queued", jobs.StatusRunning: "Running", jobs.StatusCancelling: "Cancelling",
	jobs.StatusSucceeded: "Succeeded", jobs.StatusPartial: "Partially completed", jobs.StatusFailed: "Failed",
	jobs.StatusCancelled: "Cancelled", jobs.StatusInterrupted: "Interrupted",
}

func statusClass(st jobs.Status) string {
	switch st {
	case jobs.StatusSucceeded:
		return "ok"
	case jobs.StatusFailed, jobs.StatusInterrupted:
		return "bad"
	case jobs.StatusPartial, jobs.StatusCancelled:
		return "warn"
	}
	return "active"
}

func isActive(st jobs.Status) bool {
	return st == jobs.StatusQueued || st == jobs.StatusRunning || st == jobs.StatusCancelling
}

func canCancel(st jobs.Status) bool { return st == jobs.StatusQueued || st == jobs.StatusRunning }

// canRetry asks the service whether the job may be resumed or retried; the
// kind must also be safe to put in a URL path.
func (s *Server) canRetry(j jobs.Job) bool {
	return actionPattern.MatchString(j.Kind) && s.maint.ResumeEligible(j.ID, j.Kind)
}

func shortID(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func hasReport(j jobs.Job) bool { return j.Result != nil && j.Result.Report != nil }

type countRow struct {
	Key   string
	Value int
}

type jobRow struct {
	ID, ShortID, Kind, Step, Message, Summary string
	Status, StatusLabel, StatusClass          string
	Done, Total, Left                         int
	Active, Resumable, ReportAvailable        bool
	CanCancel, CanRetry                       bool
	Interrupted, Partial, Failed, Cancelled   bool
	Created, Started, Finished, Duration      string
	Counts                                    []countRow
	Resources                                 []string
	ResumeOf                                  string
	Page, CSVURL, JSONURL                     string
}

func (s *Server) jobRow(j jobs.Job, now time.Time) jobRow {
	page := "/jobs/" + j.ID
	row := jobRow{
		ID: j.ID, ShortID: shortID(j.ID), Kind: j.Kind, Step: j.Step, Message: j.Message,
		Status: string(j.Status), StatusLabel: statusLabels[j.Status], StatusClass: statusClass(j.Status),
		Done: j.Done, Total: j.Total, Active: isActive(j.Status), Resumable: j.Resumable,
		CanCancel: canCancel(j.Status), CanRetry: s.canRetry(j),
		Interrupted: j.Status == jobs.StatusInterrupted, Partial: j.Status == jobs.StatusPartial,
		Failed: j.Status == jobs.StatusFailed, Cancelled: j.Status == jobs.StatusCancelled,
		Created: rfc3339(j.CreatedAt), Started: rfc3339(j.StartedAt), Finished: rfc3339(j.FinishedAt),
		Resources: j.Resources, Page: page, ReportAvailable: hasReport(j),
	}
	if row.StatusLabel == "" {
		row.StatusLabel = string(j.Status)
	}
	if j.Total > j.Done {
		row.Left = j.Total - j.Done
	}
	if jobIDPattern.MatchString(j.ResumeOf) {
		row.ResumeOf = j.ResumeOf
	}
	if j.Result != nil {
		row.Summary = j.Result.Summary
	}
	if row.ReportAvailable {
		row.CSVURL, row.JSONURL = page+"/report.csv", page+"/report.json"
	}
	if !j.StartedAt.IsZero() {
		end := j.FinishedAt
		if end.IsZero() {
			end = now
		}
		if d := end.Sub(j.StartedAt).Round(time.Second); d >= 0 {
			row.Duration = d.String()
		}
	}
	for k, v := range j.Counts {
		row.Counts = append(row.Counts, countRow{k, v})
	}
	sort.Slice(row.Counts, func(a, b int) bool { return row.Counts[a].Key < row.Counts[b].Key })
	return row
}

type jobPage struct {
	Job jobRow
}

// autoRefresh asks the layout for a no-JS refresh while the job is active.
func (p jobPage) autoRefresh() bool { return p.Job.Active }

// --- pages ---

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	list := s.maint.Jobs()
	rows := make([]jobRow, 0, len(list))
	for _, j := range list {
		rows = append(rows, s.jobRow(j, now))
	}
	s.render(w, r, "jobs", "Jobs", map[string]any{"Jobs": rows})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	row := s.jobRow(j, time.Now())
	s.render(w, r, "job", "Job "+row.ShortID, jobPage{Job: row})
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	err := s.maint.CancelJob(j.ID)
	switch {
	case err == nil:
		s.done(w, r, "Cancellation requested. The job stops at its next safe point.", "/jobs/"+j.ID)
	case errors.Is(err, maintenance.ErrJobFinished):
		s.refused(w, r, "already_finished", "This job had already finished; nothing was cancelled.", "/jobs/"+j.ID)
	case errors.Is(err, jobs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "job not found")
	default:
		s.internal(w, r, err)
	}
}

// handleJobRetry never starts anything: it sends the operator to the
// operation's preview, where the resumed run must be confirmed.
func (s *Server) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	if !s.canRetry(j) {
		s.refused(w, r, "not_retryable", "This job cannot be resumed or retried.", "/jobs/"+j.ID)
		return
	}
	s.done(w, r, "Review the preview to resume or retry this job.",
		"/operations/"+j.Kind+"?resume_of="+url.QueryEscape(j.ID))
}

// refused reports an expected no-op: API and fetch clients get a 409 JSON
// error (never a success), plain forms go back to the job page with the
// explanation.
func (s *Server) refused(w http.ResponseWriter, r *http.Request, code, message, location string) {
	if wantsJSON(r) || isFetch(r) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": code, "message": message}})
		return
	}
	s.done(w, r, message, location)
}

func (s *Server) handleJobReport(w http.ResponseWriter, r *http.Request, ext, contentType string, write func(io.Writer, string) error) {
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	// Rendered in full first, so a failure is a clean error page and never a
	// truncated download.
	var buf bytes.Buffer
	if err := write(&buf, j.ID); err != nil {
		switch {
		case errors.Is(err, maintenance.ErrNoReport):
			s.fail(w, r, http.StatusNotFound, "no_report", "this job has no report")
		case errors.Is(err, jobs.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "job not found")
		default:
			s.internal(w, r, err)
		}
		return
	}
	name := "job"
	if actionPattern.MatchString(j.Kind) {
		name = j.Kind
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", `attachment; filename="`+name+"-"+shortID(j.ID)+"."+ext+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// --- API ---

type apiJob struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Status          string            `json:"status"`
	Active          bool              `json:"active"`
	Step            string            `json:"step"`
	Done            int               `json:"done"`
	Total           int               `json:"total"`
	Counts          map[string]int    `json:"counts"`
	Message         string            `json:"message"`
	Resources       []string          `json:"resources"`
	ResumeOf        string            `json:"resume_of,omitempty"`
	Resumable       bool              `json:"resumable"`
	CreatedAt       string            `json:"created_at"`
	StartedAt       string            `json:"started_at,omitempty"`
	FinishedAt      string            `json:"finished_at,omitempty"`
	Summary         string            `json:"summary,omitempty"`
	ReportAvailable bool              `json:"report_available"`
	Links           map[string]string `json:"links"`
}

func toAPIJob(j jobs.Job) apiJob {
	counts := make(map[string]int, len(j.Counts))
	for k, v := range j.Counts {
		counts[k] = v
	}
	out := apiJob{
		ID: j.ID, Kind: j.Kind, Status: string(j.Status), Active: isActive(j.Status), Step: j.Step,
		Done: j.Done, Total: j.Total, Counts: counts, Message: j.Message, Resources: nonNilStrings(append([]string(nil), j.Resources...)),
		ResumeOf: j.ResumeOf, Resumable: j.Resumable,
		CreatedAt: rfc3339(j.CreatedAt), StartedAt: rfc3339(j.StartedAt), FinishedAt: rfc3339(j.FinishedAt),
		ReportAvailable: hasReport(j),
		Links:           map[string]string{"self": "/api/v1/jobs/" + j.ID, "page": "/jobs/" + j.ID},
	}
	if j.Result != nil {
		out.Summary = j.Result.Summary
	}
	if out.ReportAvailable {
		out.Links["report_csv"] = "/jobs/" + j.ID + "/report.csv"
		out.Links["report_json"] = "/jobs/" + j.ID + "/report.json"
	}
	return out
}

func (s *Server) apiJobs(w http.ResponseWriter, _ *http.Request) {
	list := s.maint.Jobs()
	items := make([]apiJob, 0, len(list))
	for _, j := range list {
		items = append(items, toAPIJob(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) apiJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAPIJob(j))
}
