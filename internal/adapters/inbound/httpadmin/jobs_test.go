package httpadmin_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

const (
	jobID1 = "a1b2c3d4-0000-4000-8000-000000000001"
	jobID2 = "b2c3d4e5-0000-4000-8000-000000000002"
)

// fakeMaint is a Service that also implements httpadmin.Maintenance.
type fakeMaint struct {
	fakeService
	mu        sync.Mutex
	list      []jobs.Job
	cancelErr error
	reportErr error
	cancelled []string
	// ineligible makes the service refuse every resume, whatever the job says.
	ineligible bool
}

// ResumeEligible mirrors the service: the job must exist, match the action
// and be retryable.
func (f *fakeMaint) ResumeEligible(id, action string) bool {
	j, ok := f.Job(id)
	return ok && !f.ineligible && j.Kind == action && maintenance.Retryable(j)
}

func (f *fakeMaint) Jobs() []jobs.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jobs.Job(nil), f.list...)
}

func (f *fakeMaint) Job(id string) (jobs.Job, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.list {
		if j.ID == id {
			return j, true
		}
	}
	return jobs.Job{}, false
}

func (f *fakeMaint) CancelJob(id string) error {
	j, ok := f.Job(id)
	if !ok {
		return jobs.ErrNotFound
	}
	if f.cancelErr != nil {
		return f.cancelErr
	}
	if j.Status.Terminal() {
		return maintenance.ErrJobFinished
	}
	f.mu.Lock()
	f.cancelled = append(f.cancelled, id)
	f.mu.Unlock()
	return nil
}

func (f *fakeMaint) report(id string) (jobs.Report, error) {
	j, ok := f.Job(id)
	if !ok {
		return jobs.Report{}, jobs.ErrNotFound
	}
	if f.reportErr != nil {
		return jobs.Report{}, f.reportErr
	}
	if j.Result == nil || j.Result.Report == nil {
		return jobs.Report{}, maintenance.ErrNoReport
	}
	return *j.Result.Report, nil
}

func (f *fakeMaint) JobReportCSV(w io.Writer, id string) error {
	r, err := f.report(id)
	if err != nil {
		return err
	}
	return maintenance.WriteCSV(w, r)
}

func (f *fakeMaint) JobReportJSON(w io.Writer, id string) error {
	r, err := f.report(id)
	if err != nil {
		return err
	}
	return maintenance.WriteJSON(w, r)
}

var _ httpadmin.Maintenance = (*fakeMaint)(nil)

var jobT0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func runningJob() jobs.Job {
	return jobs.Job{
		ID: jobID1, Kind: "sync-all", Status: jobs.StatusRunning, Step: "fetching albums",
		Done: 3, Total: 10, Counts: map[string]int{"updated": 2, "skipped": 1},
		Resources: []string{"database"}, CreatedAt: jobT0, StartedAt: jobT0.Add(time.Second),
	}
}

type jobsEnv struct {
	svc    *fakeMaint
	h      http.Handler
	cookie *http.Cookie
	csrf   string
	log    *bytes.Buffer
}

func newJobsEnv(t *testing.T, list ...jobs.Job) *jobsEnv {
	t.Helper()
	svc := &fakeMaint{list: list}
	var logs bytes.Buffer
	srv, err := httpadmin.New(svc, httpadmin.Options{Addr: host, Token: token, Log: &logs})
	require.NoError(t, err)
	h := srv.Handler()
	cookie := login(t, h)
	return &jobsEnv{svc: svc, h: h, cookie: cookie, csrf: csrfFor(t, h, cookie), log: &logs}
}

func (e *jobsEnv) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, e.h, http.MethodGet, target, withCookie(e.cookie))
}

func (e *jobsEnv) post(target string, form url.Values, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *jobsEnv) form() url.Values { return url.Values{"csrf_token": {e.csrf}} }

const refreshMeta = `<meta http-equiv="refresh" content="3">`

func TestJobPageShowsStatusStepCounts(t *testing.T) {
	e := newJobsEnv(t, runningJob())
	rec := e.get(t, "/jobs/"+jobID1)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "sync-all")
	require.Contains(t, body, `data-job-id="`+jobID1+`"`)
	require.Contains(t, body, `data-job-status="running"`)
	require.Contains(t, body, "Running")
	require.Contains(t, body, "fetching albums")
	require.Contains(t, body, `<progress data-job-progress max="10" value="3"`)
	require.Regexp(t, `<span data-job-field="done">3</span> of <span data-job-field="total">10</span>`, body)
	require.Regexp(t, `(?s)<th scope="row">updated</th>\s*<td>2</td>`, body)
	require.Regexp(t, `(?s)<th scope="row">skipped</th>\s*<td>1</td>`, body)
	require.Contains(t, body, "database")
	require.Contains(t, body, "2026-10-20T12:00:00Z")
	require.Contains(t, body, `action="/jobs/`+jobID1+`/cancel"`)
	require.NotContains(t, body, "/retry")
	require.Contains(t, body, refreshMeta, "active jobs refresh without JS")
}

func TestJobPageRefreshesOnlyWhileActive(t *testing.T) {
	for _, st := range []jobs.Status{jobs.StatusQueued, jobs.StatusRunning, jobs.StatusCancelling} {
		j := runningJob()
		j.Status = st
		e := newJobsEnv(t, j)
		require.Contains(t, e.get(t, "/jobs/"+jobID1).Body.String(), refreshMeta, st)
	}
	for _, st := range []jobs.Status{jobs.StatusSucceeded, jobs.StatusPartial, jobs.StatusFailed, jobs.StatusCancelled, jobs.StatusInterrupted} {
		j := runningJob()
		j.Status, j.FinishedAt = st, jobT0.Add(time.Minute)
		e := newJobsEnv(t, j)
		body := e.get(t, "/jobs/"+jobID1).Body.String()
		require.NotContains(t, body, refreshMeta, st)
		require.NotContains(t, body, "/cancel", "no cancel button for %s", st)
	}
	// Other pages never refresh.
	e := newJobsEnv(t, runningJob())
	require.NotContains(t, e.get(t, "/jobs").Body.String(), refreshMeta)
	require.NotContains(t, e.get(t, "/").Body.String(), refreshMeta)
}

func TestInterruptedJobShowsResumeButtonNotSuccess(t *testing.T) {
	j := runningJob()
	j.Status, j.Resumable, j.FinishedAt = jobs.StatusInterrupted, true, jobT0.Add(time.Minute)
	j.Message = "The admin restarted while this job was running."
	e := newJobsEnv(t, j)
	rec := e.get(t, "/jobs/"+jobID1)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "The admin restarted while this job was running; it did not complete.")
	require.Contains(t, body, "Resumable: yes")
	require.Contains(t, body, `action="/jobs/`+jobID1+`/retry"`)
	require.Contains(t, body, "Resume/retry…")
	require.NotContains(t, strings.ToLower(body), "succeeded")
	require.NotContains(t, strings.ToLower(body), "success")
	require.NotContains(t, body, `class="badge ok"`)

	j.Resumable = false
	e = newJobsEnv(t, j)
	body = e.get(t, "/jobs/"+jobID1).Body.String()
	require.Contains(t, body, "Resumable: no")
	require.NotContains(t, body, "/retry")

	// The list never shows an interrupted job as a success either.
	list := e.get(t, "/jobs").Body.String()
	require.Contains(t, list, "Interrupted")
	require.NotContains(t, strings.ToLower(list), "succeeded")
}

func TestFailedAndPartialJobsOfferRetryWithBanners(t *testing.T) {
	failed := runningJob()
	failed.Status, failed.Message, failed.FinishedAt = jobs.StatusFailed, "The source is temporarily unavailable.", jobT0.Add(time.Minute)
	e := newJobsEnv(t, failed)
	body := e.get(t, "/jobs/"+jobID1).Body.String()
	require.Contains(t, body, "This job failed")
	require.Contains(t, body, "The source is temporarily unavailable.")
	require.Contains(t, body, "/retry", "failed jobs can always be retried")

	partial := runningJob()
	partial.Status, partial.Done, partial.Total = jobs.StatusPartial, 4, 10
	partial.Message, partial.Resumable, partial.FinishedAt = "The source is rate limited; retry after 30 seconds.", true, jobT0.Add(time.Minute)
	e = newJobsEnv(t, partial)
	body = e.get(t, "/jobs/"+jobID1).Body.String()
	require.Contains(t, body, "only partly completed")
	require.Contains(t, body, "Completed 4 of 10; 6 left.")
	require.Contains(t, body, "retry after 30 seconds")
	require.Contains(t, body, "/retry")

	cancelled := runningJob()
	cancelled.Status, cancelled.FinishedAt = jobs.StatusCancelled, jobT0.Add(time.Minute)
	e = newJobsEnv(t, cancelled)
	body = e.get(t, "/jobs/"+jobID1).Body.String()
	require.Contains(t, body, "cancelled before it finished")
	require.NotContains(t, body, "/retry", "non-resumable cancelled jobs are not retried from here")

	ok := runningJob()
	ok.Status, ok.FinishedAt, ok.Done = jobs.StatusSucceeded, jobT0.Add(time.Minute), 10
	ok.Result = &jobs.Result{Summary: "Synced 10 artists."}
	e = newJobsEnv(t, ok)
	body = e.get(t, "/jobs/"+jobID1).Body.String()
	require.Contains(t, body, "Synced 10 artists.")
	require.Contains(t, body, "59s")
	require.NotContains(t, body, "/retry")
	require.NotContains(t, body, "report.csv", "no report links without a report")
}

func TestCancelPostRequiresSameOrigin(t *testing.T) {
	e := newJobsEnv(t, runningJob())
	target := "/jobs/" + jobID1 + "/cancel"

	rec := e.post(target, e.form(), "https://evil.example")
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = e.post(target, url.Values{}, "http://"+host)
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = e.post(target, url.Values{"csrf_token": {"nope"}}, "http://"+host)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, e.svc.cancelled)

	// GET never cancels.
	require.Equal(t, http.StatusNotFound, e.get(t, target).Code)
	require.Empty(t, e.svc.cancelled)

	rec = e.post(target, e.form(), "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID1, rec.Header().Get("Location"))
	require.Equal(t, []string{jobID1}, e.svc.cancelled)
	require.Contains(t, e.get(t, "/jobs/"+jobID1).Body.String(), "Cancellation requested")
}

func TestCancelFinishedJobIsFriendly(t *testing.T) {
	j := runningJob()
	j.Status, j.FinishedAt = jobs.StatusSucceeded, jobT0.Add(time.Minute)
	e := newJobsEnv(t, j)
	rec := e.post("/jobs/"+jobID1+"/cancel", e.form(), "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID1, rec.Header().Get("Location"))
	require.Contains(t, e.get(t, "/jobs/"+jobID1).Body.String(), "already finished; nothing was cancelled")
	require.Empty(t, e.svc.cancelled)

	rec = e.post("/jobs/"+jobID2+"/cancel", e.form(), "http://"+host)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRetryRedirectsToPreviewAndStartsNothing(t *testing.T) {
	j := runningJob()
	j.Status, j.FinishedAt = jobs.StatusFailed, jobT0.Add(time.Minute)
	e := newJobsEnv(t, j)
	before := e.svc.Jobs()

	rec := e.post("/jobs/"+jobID1+"/retry", url.Values{}, "http://"+host)
	require.Equal(t, http.StatusForbidden, rec.Code, "retry needs a CSRF token")

	rec = e.post("/jobs/"+jobID1+"/retry", e.form(), "https://evil.example")
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = e.post("/jobs/"+jobID1+"/retry", e.form(), "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/operations/sync-all?resume_of="+jobID1, rec.Header().Get("Location"))
	require.Equal(t, before, e.svc.Jobs(), "retry must not start or change anything")
	require.Empty(t, e.svc.cancelled)

	// An active job cannot be retried.
	e = newJobsEnv(t, runningJob())
	rec = e.post("/jobs/"+jobID1+"/retry", e.form(), "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID1, rec.Header().Get("Location"))

	// A job whose kind is not a valid action name is never redirected with it.
	bad := runningJob()
	bad.Kind, bad.Status = "../evil?x=1", jobs.StatusFailed
	e = newJobsEnv(t, bad)
	rec = e.post("/jobs/"+jobID1+"/retry", e.form(), "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID1, rec.Header().Get("Location"))
}

func TestReportDownloadDeterministicHeaders(t *testing.T) {
	report := jobs.Report{Name: "sync", Columns: []string{"slug", "status"}, Rows: [][]string{{"zeta", "=cmd"}, {"alpha", "ok"}}}
	j := runningJob()
	j.Status, j.FinishedAt = jobs.StatusSucceeded, jobT0.Add(time.Minute)
	j.Result = &jobs.Result{Summary: "done", Report: &report}
	noReport := runningJob()
	noReport.ID, noReport.Status = jobID2, jobs.StatusSucceeded
	e := newJobsEnv(t, j, noReport)

	page := e.get(t, "/jobs/"+jobID1).Body.String()
	require.Contains(t, page, `href="/jobs/`+jobID1+`/report.csv"`)
	require.Contains(t, page, `href="/jobs/`+jobID1+`/report.json"`)

	var want bytes.Buffer
	require.NoError(t, maintenance.WriteCSV(&want, report))
	rec := e.get(t, "/jobs/"+jobID1+"/report.csv")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="sync-all-a1b2c3d4.csv"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, want.String(), rec.Body.String())
	require.Equal(t, rec.Body.String(), e.get(t, "/jobs/"+jobID1+"/report.csv").Body.String(), "byte-identical on repeat")

	want.Reset()
	require.NoError(t, maintenance.WriteJSON(&want, report))
	rec = e.get(t, "/jobs/"+jobID1+"/report.json")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="sync-all-a1b2c3d4.json"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, want.String(), rec.Body.String())

	// A kind that is not a plain slug never reaches the filename.
	evil := j
	evil.Kind = `../../etc/"passwd`
	e = newJobsEnv(t, evil)
	rec = e.get(t, "/jobs/"+jobID1+"/report.csv")
	require.Equal(t, http.StatusOK, rec.Code)
	cd := rec.Header().Get("Content-Disposition")
	require.Equal(t, `attachment; filename="job-a1b2c3d4.csv"`, cd)
	require.NotContains(t, cd, "/")
	require.NotContains(t, cd, "..")

	e = newJobsEnv(t, j, noReport)
	require.Equal(t, http.StatusNotFound, e.get(t, "/jobs/"+jobID2+"/report.csv").Code)
	require.Equal(t, http.StatusNotFound, e.get(t, "/jobs/"+jobID2+"/report.json").Code)
	require.Equal(t, http.StatusNotFound, e.get(t, "/jobs/ffffffff/report.csv").Code)
}

func TestJobPageNeverShowsPathOrStack(t *testing.T) {
	leak := errors.New("open /Users/javi/private/artists.yaml: permission denied\ngoroutine 1 [running]:\nmain.main()")
	report := jobs.Report{Name: "r", Columns: []string{"a"}}
	j := runningJob()
	j.Result = &jobs.Result{Report: &report}
	e := newJobsEnv(t, j)
	e.svc.cancelErr, e.svc.reportErr = leak, leak

	for _, rec := range []*httptest.ResponseRecorder{
		e.post("/jobs/"+jobID1+"/cancel", e.form(), "http://"+host),
		e.get(t, "/jobs/"+jobID1+"/report.csv"),
		e.get(t, "/jobs/"+jobID1+"/report.json"),
	} {
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		body := rec.Body.String()
		require.NotContains(t, body, "/Users")
		require.NotContains(t, body, "artists.yaml")
		require.NotContains(t, body, "goroutine")
		require.Empty(t, rec.Header().Get("Content-Disposition"), "a failed report is not offered as a download")
	}
	require.Contains(t, e.log.String(), "permission denied", "details are logged server-side")

	// The page and API never inline report rows or internal fields.
	report.Rows = [][]string{{"secret-row-value"}}
	e = newJobsEnv(t, j)
	require.NotContains(t, e.get(t, "/jobs/"+jobID1).Body.String(), "secret-row-value")
	require.NotContains(t, e.get(t, "/api/v1/jobs/"+jobID1).Body.String(), "secret-row-value")
}

func TestJobsRoutesAbsentWithoutMaintenance(t *testing.T) {
	h := newServer(t, &fakeService{}).Handler()
	cookie := login(t, h)
	for _, target := range []string{"/jobs", "/jobs/" + jobID1, "/api/v1/jobs", "/api/v1/jobs/" + jobID1, "/jobs/" + jobID1 + "/report.csv"} {
		require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, target, withCookie(cookie)).Code, target)
	}
	require.NotContains(t, do(t, h, http.MethodGet, "/", withCookie(cookie)).Body.String(), `href="/jobs"`)

	e := newJobsEnv(t)
	require.Contains(t, e.get(t, "/").Body.String(), `href="/jobs"`)
}

func TestJobsRequireAuthentication(t *testing.T) {
	e := newJobsEnv(t, runningJob())
	for _, target := range []string{"/jobs", "/jobs/" + jobID1, "/jobs/" + jobID1 + "/report.csv"} {
		rec := do(t, e.h, http.MethodGet, target, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target)
		require.NotContains(t, rec.Body.String(), "fetching albums")
	}
	rec := do(t, e.h, http.MethodGet, "/api/v1/jobs", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), `"unauthorized"`)

	req := httptest.NewRequest(http.MethodPost, "/jobs/"+jobID1+"/cancel", strings.NewReader(e.form().Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out := httptest.NewRecorder()
	e.h.ServeHTTP(out, req)
	require.Equal(t, http.StatusUnauthorized, out.Code)
	require.Empty(t, e.svc.cancelled)
}

func TestInvalidOrUnknownJobIDIs404(t *testing.T) {
	e := newJobsEnv(t, runningJob())
	for _, id := range []string{"ffffffff", "not-hex-zz", "%3Cscript%3E", "a1b2c3d4.csv", strings.Repeat("a", 65)} {
		require.Equal(t, http.StatusNotFound, e.get(t, "/jobs/"+id).Code, id)
		rec := e.get(t, "/api/v1/jobs/"+id)
		require.Equal(t, http.StatusNotFound, rec.Code, id)
		var body map[string]map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "not_found", body["error"]["code"])
		require.Equal(t, http.StatusNotFound, e.post("/jobs/"+id+"/cancel", e.form(), "http://"+host).Code, id)
		require.Equal(t, http.StatusNotFound, e.post("/jobs/"+id+"/retry", e.form(), "http://"+host).Code, id)
	}
}

func TestJobFieldsAreEscaped(t *testing.T) {
	j := runningJob()
	j.Kind, j.Step, j.Message = "<script>alert(1)</script>", "<script>alert(2)</script>", "<script>alert(3)</script>"
	j.Counts = map[string]int{"<b>x</b>": 1}
	e := newJobsEnv(t, j)
	for _, target := range []string{"/jobs/" + jobID1, "/jobs"} {
		body := e.get(t, target).Body.String()
		require.NotContains(t, body, "<script>alert", target)
		require.NotContains(t, body, "<b>x</b>", target)
		require.Contains(t, body, "&lt;script&gt;alert(2)", target)
	}
	for _, target := range []string{"/api/v1/jobs/" + jobID1, "/api/v1/jobs"} {
		body := e.get(t, target).Body.String()
		require.NotContains(t, body, "<script>", target)
		require.NotContains(t, body, "<b>", target)
	}
}

func TestJobsListNewestFirstAndEmptyState(t *testing.T) {
	e := newJobsEnv(t)
	body := e.get(t, "/jobs").Body.String()
	require.Contains(t, body, "No jobs yet")

	older := runningJob()
	older.ID, older.Kind, older.Status, older.FinishedAt = jobID2, "audit-profiles", jobs.StatusSucceeded, jobT0.Add(90*time.Second)
	e = newJobsEnv(t, runningJob(), older) // the service lists newest first
	body = e.get(t, "/jobs").Body.String()
	require.NotContains(t, body, "No jobs yet")
	i1, i2 := strings.Index(body, `href="/jobs/`+jobID1+`"`), strings.Index(body, `href="/jobs/`+jobID2+`"`)
	require.True(t, i1 > 0 && i2 > i1, "newest first")
	require.Contains(t, body, "a1b2c3d4")
	require.Contains(t, body, "audit-profiles")
	require.Contains(t, body, "1m29s")
	require.Contains(t, body, "3 / 10")
}

func TestJobsAPIShape(t *testing.T) {
	report := jobs.Report{Name: "r", Columns: []string{"a"}, Rows: [][]string{{"row"}}}
	done := runningJob()
	done.ID, done.Status, done.ResumeOf, done.Resumable = jobID2, jobs.StatusPartial, jobID1, true
	done.CreatedAt = time.Date(2026, 10, 20, 14, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	done.FinishedAt = done.CreatedAt.Add(time.Minute)
	done.Message = "The source is rate limited; retry after 30 seconds."
	done.Result = &jobs.Result{Summary: "4 of 10 synced", Report: &report}
	e := newJobsEnv(t, done, runningJob())

	rec := e.get(t, "/api/v1/jobs")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	var list struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 2)
	require.Equal(t, jobID2, list.Items[0]["id"])

	rec = e.get(t, "/api/v1/jobs/"+jobID2)
	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, map[string]any{
		"id": jobID2, "kind": "sync-all", "status": "partial", "active": false, "step": "fetching albums",
		"done": 3.0, "total": 10.0, "counts": map[string]any{"updated": 2.0, "skipped": 1.0},
		"message": "The source is rate limited; retry after 30 seconds.", "resources": []any{"database"},
		"resume_of": jobID1, "resumable": true,
		"created_at": "2026-10-20T12:00:00Z", "started_at": "2026-10-20T12:00:01Z", "finished_at": "2026-10-20T12:01:00Z",
		"summary": "4 of 10 synced", "report_available": true,
		"links": map[string]any{
			"self": "/api/v1/jobs/" + jobID2, "page": "/jobs/" + jobID2,
			"report_csv": "/jobs/" + jobID2 + "/report.csv", "report_json": "/jobs/" + jobID2 + "/report.json",
		},
	}, got)

	rec = e.get(t, "/api/v1/jobs/"+jobID1)
	got = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, true, got["active"])
	require.Equal(t, false, got["report_available"])
	require.NotContains(t, got, "finished_at")
	require.Equal(t, map[string]any{"self": "/api/v1/jobs/" + jobID1, "page": "/jobs/" + jobID1}, got["links"])
}

func TestRetryEligibilityComesFromTheService(t *testing.T) {
	j := runningJob()
	j.Status, j.FinishedAt = jobs.StatusFailed, jobT0.Add(time.Minute)
	e := newJobsEnv(t, j)
	e.svc.ineligible = true
	require.NotContains(t, e.get(t, "/jobs/"+jobID1).Body.String(), "/retry", "no button when the service refuses")
	rec := e.post("/jobs/"+jobID1+"/retry", e.form(), "http://"+host)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/jobs/"+jobID1, rec.Header().Get("Location"), "refused, back to the job")
	require.Contains(t, e.get(t, "/jobs/"+jobID1).Body.String(), "cannot be resumed or retried")
}

func TestFetchRefusalIsAnErrorNotASuccess(t *testing.T) {
	j := runningJob()
	j.Status, j.FinishedAt = jobs.StatusSucceeded, jobT0.Add(time.Minute)
	e := newJobsEnv(t, j)
	for _, target := range []string{"/jobs/" + jobID1 + "/cancel", "/jobs/" + jobID1 + "/retry"} {
		for _, header := range []string{"X-Admin-Fetch", "Accept"} {
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(e.form().Encode()))
			req.Host = host
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://"+host)
			if header == "Accept" {
				req.Header.Set("Accept", "application/json")
			} else {
				req.Header.Set("X-Admin-Fetch", "1")
			}
			req.AddCookie(e.cookie)
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			require.Equal(t, http.StatusConflict, rec.Code, target+" "+header)
			var body map[string]map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), target)
			require.NotEmpty(t, body["error"]["code"])
			require.NotEmpty(t, body["error"]["message"])
		}
	}
	require.Empty(t, e.svc.cancelled)
}
