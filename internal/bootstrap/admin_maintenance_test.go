package bootstrap

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	e2eHost  = "127.0.0.1:8787"
	e2eToken = "0123456789abcdef0123456789abcdef"
)

// threeArtistCatalog has three syncable artists, in editorial order.
const threeArtistCatalog = `
version: 1
profile_sync_policy: allow_unreviewed
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
  - slug: gza
    name: GZA
    spotify_id: 1AAAAAAAAAAAAAAAAAAAAA
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 2
    notes: ""
  - slug: rza
    name: RZA
    spotify_id: 1BBBBBBBBBBBBBBBBBBBBB
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 3
    notes: ""
`

// e2eRoot is a temporary repository root with only data/artists.yaml.
func e2eRoot(t *testing.T, catalogYAML string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "data", "artists.yaml"), []byte(catalogYAML), 0o644))
	return root
}

func e2eConfig(root string, env EnvLookup, r maintenance.Releaser, opts ...MaintenanceOption) AdminConfig {
	return AdminConfig{
		CatalogPath: filepath.Join(root, "data", "artists.yaml"),
		DBPath:      filepath.Join(root, "data", "catalog.db"),
		RepoRoot:    root,
		Env:         env,
		releaser:    r,
		// Never reach Wikipedia from a test.
		options: append([]MaintenanceOption{WithWikipedia("http://127.0.0.1:1/w/api.php", "")}, opts...),
	}
}

// stack is the real admin composition behind the real HTTP adapter.
type stack struct {
	t      *testing.T
	a      *Admin
	close  func()
	h      http.Handler
	cookie *http.Cookie
	csrf   string
}

func startStack(t *testing.T, cfg AdminConfig) *stack {
	t.Helper()
	a, closeFn, err := NewAdmin(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(closeFn)
	srv, err := httpadmin.New(a, httpadmin.Options{Addr: e2eHost, Token: e2eToken})
	require.NoError(t, err)
	s := &stack{t: t, a: a, close: closeFn, h: srv.Handler()}

	rec := s.request(http.MethodGet, "/auth?token="+e2eToken, nil, "")
	require.Equal(t, http.StatusSeeOther, rec.Code)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "swf_admin_session" {
			s.cookie = c
		}
	}
	require.NotNil(t, s.cookie)
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(s.get("/").Body.String())
	require.Len(t, m, 2)
	s.csrf = m[1]
	return s
}

func (s *stack) request(method, target string, form url.Values, origin string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.Host = e2eHost
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if s.cookie != nil {
		req.AddCookie(s.cookie)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func (s *stack) get(target string) *httptest.ResponseRecorder {
	return s.request(http.MethodGet, target, nil, "")
}

func (s *stack) post(target string, form url.Values) *httptest.ResponseRecorder {
	form.Set("csrf_token", s.csrf)
	return s.request(http.MethodPost, target, form, "http://"+e2eHost)
}

// confirm previews action with p and posts the confirm form through HTTP.
func (s *stack) confirm(action string, p maintenance.Params, typed string, extra url.Values) *httptest.ResponseRecorder {
	s.t.Helper()
	_, tok, err := s.a.PreviewAction(context.Background(), action, p)
	require.NoError(s.t, err)
	return s.confirmWith(action, p, tok, typed, extra)
}

func (s *stack) confirmWith(action string, p maintenance.Params, tok maintenance.Token, typed string, extra url.Values) *httptest.ResponseRecorder {
	form := url.Values{"token": {string(tok)}, "typed": {typed}}
	for k, v := range p {
		form.Set(k, v)
	}
	for k, v := range extra {
		form[k] = v
	}
	return s.post("/operations/"+action+"/run", form)
}

var jobLocation = regexp.MustCompile(`^/jobs/([0-9a-f-]+)$`)

// startedJob returns the job a successful confirm redirected to.
func (s *stack) startedJob(rec *httptest.ResponseRecorder) string {
	s.t.Helper()
	require.Equal(s.t, http.StatusSeeOther, rec.Code, rec.Body.String())
	m := jobLocation.FindStringSubmatch(rec.Header().Get("Location"))
	require.Len(s.t, m, 2, "redirects to the job page: %q", rec.Header().Get("Location"))
	return m[1]
}

func (s *stack) wait(id string) jobs.Job {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := s.a.Job(id); ok && j.Status.Terminal() {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Fatalf("job %s did not finish", id)
	return jobs.Job{}
}

// limitedFetcher answers every artist except abortOn, which hits a Spotify
// 429 abort until allow() is called. It records the artists it was asked for.
type limitedFetcher struct {
	mu      sync.Mutex
	abortOn string
	allowed bool
	seen    []string
}

func (f *limitedFetcher) allow() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowed, f.seen = true, nil
}

func (f *limitedFetcher) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *limitedFetcher) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, id)
	if id == f.abortOn && !f.allowed {
		return catalog.ArtistCandidate{}, rateLimitAbort{}
	}
	return catalog.ArtistCandidate{SpotifyID: id, Name: id}, nil
}

func (*limitedFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return nil, nil
}

func (*limitedFetcher) GetAlbum(context.Context, string) (catalog.Release, error) {
	return catalog.Release{}, nil
}

func (*limitedFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return nil, nil
}

func TestNewAdminWiresJournalBackupsAndLogUnderTheWorkDir(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	cfg := e2eConfig(root, envMap(nil), readyReleaser())
	paths, err := cfg.Paths()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "build", "admin"), paths.WorkDir)
	require.Equal(t, filepath.Join(root, "build", "admin", "jobs.jsonl"), paths.JournalPath)
	require.Equal(t, filepath.Join(root, "build", "admin", "backups"), paths.BackupDir)
	require.Equal(t, cfg.CatalogPath+".operations.jsonl", paths.OperationLog)
	require.Equal(t, filepath.Join(root, "data", "catalog.snapshot.sql.gz"), paths.SnapshotPath)
	require.Equal(t, filepath.Join(root, "data", "wu-candidates.yaml"), paths.LedgerPath)
	require.Equal(t, filepath.Join(root, "site", "data", "generated"), paths.ExportOutputDir)
	require.Equal(t, filepath.Join(root, "site", "static"), paths.ExportStaticDir)
	require.Equal(t, filepath.Join(root, "site", "content", "generated"), paths.ExportContentDir)

	s := startStack(t, cfg)
	names := map[string]bool{}
	for _, a := range s.a.Actions() {
		names[a.Name] = true
	}
	for _, want := range []string{
		"discover-preview", "discover-import", "audit-profiles", "refresh-metadata-preview", "refresh-metadata",
		"sync-dry-run", "sync", "db-verify", "db-snapshot", "export", "audit", "refresh-export",
		"reconcile-dry-run", "reconcile-apply", "release-prepare", "release-publish",
	} {
		require.True(t, names[want], "action %s is registered", want)
	}
	require.True(t, s.a.ReleaseConfigured())

	// The release gate reads exactly the journal the manager writes.
	id := s.startedJob(s.confirm("release-prepare", nil, "", url.Values{"from": {"publish"}}))
	require.Equal(t, jobs.StatusSucceeded, s.wait(id).Status)
	sha, ok := journalPrepared{path: paths.JournalPath}.LastPrepared()
	require.True(t, ok, "release-prepare recorded in %s", paths.JournalPath)
	require.Equal(t, localSHA, sha)
}

func TestNewJobIDIsUUIDShaped(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		id := newJobID()
		require.Regexp(t, pattern, id)
		require.False(t, seen[id])
		seen[id] = true
	}
}

func TestAdminRegistersMaintenanceRoutes(t *testing.T) {
	s := startStack(t, e2eConfig(e2eRoot(t, maintenanceCatalogYAML), envMap(nil), readyReleaser()))
	for _, path := range []string{"/", "/artists", "/operations", "/jobs", "/publish", "/api/v1/jobs", "/api/v1/operations", "/operations/sync"} {
		rec := s.get(path)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
	}
}

func TestEndToEndSyncPartialThenResume(t *testing.T) {
	root := e2eRoot(t, threeArtistCatalog)
	fetcher := &limitedFetcher{abortOn: "1BBBBBBBBBBBBBBBBBBBBB"}
	s := startStack(t, e2eConfig(root, credentials, readyReleaser(), withSyncFetcher(fetcher)))
	all := maintenance.Params{"scope": "all"}

	// The preview asks for the typed phrase; a confirm without it starts nothing.
	pv, tok, err := s.a.PreviewAction(context.Background(), "sync", all)
	require.NoError(t, err)
	require.Equal(t, "sync 3 artists", pv.RequiresTyped)
	rec := s.confirmWith("sync", all, tok, "sync 2 artists", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Empty(t, s.a.Jobs())

	id := s.startedJob(s.confirmWith("sync", all, tok, "sync 3 artists", nil))
	first := s.wait(id)
	require.Equal(t, jobs.StatusPartial, first.Status, first.Message)
	require.True(t, first.Resumable)
	require.NotNil(t, first.Result)
	require.Contains(t, first.Result.Summary, "Spotify rate limit reached")
	require.NotContains(t, first.Result.Summary, root, "no paths in summaries")
	require.True(t, s.a.ResumeEligible(id, "sync"))

	backups, err := os.ReadDir(filepath.Join(root, "build", "admin", "backups"))
	require.NoError(t, err)
	require.NotEmpty(t, backups, "the database is backed up before the sync writes")

	// The report downloads, identically twice.
	csv1 := s.get("/jobs/" + id + "/report.csv")
	require.Equal(t, http.StatusOK, csv1.Code)
	require.NotEmpty(t, csv1.Body.String())
	csv2 := s.get("/jobs/" + id + "/report.csv")
	require.Equal(t, csv1.Body.String(), csv2.Body.String(), "reports are deterministic")

	// Resume after the rate limit: only the remaining artist is fetched.
	fetcher.allow()
	resume := maintenance.Params{"scope": "all", "resume": "true"}
	pv, tok, err = s.a.PreviewAction(context.Background(), "sync", resume)
	require.NoError(t, err)
	second := s.wait(s.startedJob(s.confirmWith("sync", resume, tok, pv.RequiresTyped, nil)))
	require.Equal(t, jobs.StatusSucceeded, second.Status, second.Message)
	require.Equal(t, []string{"1BBBBBBBBBBBBBBBBBBBBB"}, fetcher.requested(), "a resume skips completed artists")

	entries, err := s.a.RecentOperations(context.Background(), 10)
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, e := range entries {
		if e.JobID == id {
			kinds[e.Kind+":"+e.Outcome]++
		}
	}
	require.Equal(t, 1, kinds["confirmed:submitted"], "%+v", entries)
	require.Equal(t, 1, kinds["finished:partial"], "%+v", entries)
	require.FileExists(t, filepath.Join(root, "data", "artists.yaml.operations.jsonl"))
}

func TestRestartMarksRunningJobInterrupted(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	cfg := e2eConfig(root, envMap(nil), readyReleaser())
	first, closeFirst, err := NewAdmin(context.Background(), cfg)
	require.NoError(t, err)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	job, err := first.manager.Submit(context.Background(), jobs.Spec{
		Kind: "sync", Resources: []string{"database"},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			close(started)
			<-release // the process "dies" before this job ever finishes
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		},
	})
	require.NoError(t, err)
	<-started
	// Simulate a resumable job (a sync that already completed artists).
	running, _ := first.Job(job.ID)
	running.Resumable = true
	require.NoError(t, filesystem.NewJobJournal(first.Paths().JournalPath).Save(context.Background(), running))
	_ = closeFirst // the old process is killed: nothing is closed or finished

	s := startStack(t, cfg)
	got, ok := s.a.Job(job.ID)
	require.True(t, ok)
	require.Equal(t, jobs.StatusInterrupted, got.Status)
	require.True(t, got.Resumable, "the resumable flag survives the restart")
	require.True(t, s.a.ResumeEligible(job.ID, "sync"))

	rec := s.get("/jobs/" + job.ID)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, strings.ToLower(rec.Body.String()), "interrupted")

	// Resources are not held: a conflicting job starts in the new process.
	conflicting, err := s.a.manager.Submit(context.Background(), jobs.Spec{
		Kind: "db-verify", Resources: []string{"database"},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, s.wait(conflicting.ID).Status)
}

// blockingFetcher blocks every artist fetch until released.
type blockingFetcher struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (f *blockingFetcher) GetArtist(ctx context.Context, id string) (catalog.ArtistCandidate, error) {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
	case <-ctx.Done():
		return catalog.ArtistCandidate{}, ctx.Err()
	}
	return catalog.ArtistCandidate{SpotifyID: id, Name: id}, nil
}

func (*blockingFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return nil, nil
}

func (*blockingFetcher) GetAlbum(context.Context, string) (catalog.Release, error) {
	return catalog.Release{}, nil
}

func (*blockingFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return nil, nil
}

func TestConflictingJobsCannotRunConcurrently(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	fetcher := &blockingFetcher{entered: make(chan struct{}), release: make(chan struct{})}
	s := startStack(t, e2eConfig(root, credentials, readyReleaser(), withSyncFetcher(fetcher)))
	p := maintenance.Params{"scope": "artist", "artists": "wu-tang-clan"}

	_, tok1, err := s.a.PreviewAction(context.Background(), "sync", p)
	require.NoError(t, err)
	_, tok2, err := s.a.PreviewAction(context.Background(), "sync", p)
	require.NoError(t, err)

	holder := s.startedJob(s.confirmWith("sync", p, tok1, "", nil))
	<-fetcher.entered

	rec := s.confirmWith("sync", p, tok2, "", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), holder[:8], "the refusal names the holder")
	require.Len(t, s.a.Jobs(), 1, "the double submit started nothing")

	close(fetcher.release)
	require.Equal(t, jobs.StatusSucceeded, s.wait(holder).Status)
}

func TestReconcileDryRunThenApplyThroughHTTP(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	cfg := e2eConfig(root, envMap(nil), readyReleaser())
	seedHomonymAlbum(t, cfg.DBPath)
	s := startStack(t, cfg)
	p := maintenance.Params{"artist": "wu-tang-clan"}
	before := databaseSnapshot(t, cfg.DBPath)

	dry := s.wait(s.startedJob(s.confirm("reconcile-dry-run", p, "", nil)))
	require.Equal(t, jobs.StatusSucceeded, dry.Status, dry.Message)
	require.Equal(t, before, databaseSnapshot(t, cfg.DBPath), "a dry run changes nothing")
	report := s.get("/jobs/" + dry.ID + "/report.csv")
	require.Equal(t, http.StatusOK, report.Code)
	require.Contains(t, report.Body.String(), "al-bad")

	// Without the typed phrase nothing is applied.
	rec := s.confirm("reconcile-apply", p, "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, []string{"al-bad", "al-ok"}, activeAlbums(t, cfg.DBPath))

	applied := s.wait(s.startedJob(s.confirm("reconcile-apply", p, "reconcile wu-tang-clan", nil)))
	require.Equal(t, jobs.StatusSucceeded, applied.Status, applied.Message)
	require.Equal(t, []string{"al-ok"}, activeAlbums(t, cfg.DBPath))
	backups, err := os.ReadDir(filepath.Join(root, "build", "admin", "backups"))
	require.NoError(t, err)
	require.NotEmpty(t, backups)
}

func TestOfflineStartNoCredentialsNoDatabase(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	// The real release adapter: the release script does not exist here, so
	// its preflight fails and the release actions report unavailable.
	cfg := e2eConfig(root, envMap(nil), nil)
	s := startStack(t, cfg)

	for _, path := range []string{"/", "/artists", "/operations", "/jobs", "/publish", "/api/v1/jobs", "/api/v1/operations"} {
		require.Equal(t, http.StatusOK, s.get(path).Code, path)
	}
	infos := map[string]maintenance.ActionInfo{}
	for _, a := range s.a.Actions() {
		infos[a.Name] = a
	}
	for _, name := range []string{"sync", "refresh-metadata", "reconcile-apply", "db-verify"} {
		require.False(t, infos[name].Available, name)
		require.NotEmpty(t, infos[name].UnavailableReason, name)
		require.NotContains(t, infos[name].UnavailableReason, root, "reasons never carry paths")
	}
	require.True(t, infos["sync-dry-run"].Available, "a dry sync needs neither Spotify nor the database")

	audit := s.wait(s.startedJob(s.confirm("audit-profiles", maintenance.Params{"offline": "true"}, "", nil)))
	require.NotEqual(t, jobs.StatusFailed, audit.Status, audit.Message)
	require.NoFileExists(t, cfg.DBPath, "nothing creates the database")
}

func TestNoAutomaticPublish(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	r := readyReleaser()
	cfg := e2eConfig(root, credentials, r, withSyncFetcher(okSyncFetcher{}))
	seedHomonymAlbum(t, cfg.DBPath)
	s := startStack(t, cfg)

	paramSets := []maintenance.Params{
		{}, {"offline": "true"}, {"artist": "wu-tang-clan"}, {"scope": "artist", "artists": "wu-tang-clan"},
		{"scope": "all"}, {"dry_run": "true"},
	}
	ran := 0
	for _, info := range s.a.Actions() {
		if strings.HasPrefix(info.Name, "release-") || info.Name == "discover-preview" || info.Name == "discover-import" {
			continue // discovery would need Wikipedia
		}
		for _, p := range paramSets {
			pv, tok, err := s.a.PreviewAction(context.Background(), info.Name, p)
			if err != nil {
				continue
			}
			rec := s.confirmWith(info.Name, p, tok, pv.RequiresTyped, nil)
			if rec.Code == http.StatusSeeOther {
				s.wait(s.startedJob(rec))
				ran++
			}
			break
		}
	}
	require.GreaterOrEqual(t, ran, 8, "most non-release flows ran end to end")
	_, _, published := r.counts()
	require.Zero(t, published, "no flow publishes on its own")

	// The generic flow redirects release actions to the guided page.
	rec := s.post("/operations/release-publish/run", url.Values{})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/publish", rec.Header().Get("Location"))

	// Prepare through the guided page; the package is then on disk.
	guided := url.Values{"from": {"publish"}}
	require.Equal(t, jobs.StatusSucceeded, s.wait(s.startedJob(s.confirm("release-prepare", nil, "", guided))).Status)
	r.mu.Lock()
	r.prereq.PackageDirClean = false
	r.prereq.PreparedTag, r.prereq.PreparedContentSHA256 = "catalog-new", localSHA
	r.mu.Unlock()

	// No token, wrong phrase or no from=publish: nothing is published.
	rec = s.post("/operations/release-publish/run", url.Values{"from": {"publish"}, "typed": {"publish release"}})
	require.Equal(t, http.StatusConflict, rec.Code)
	rec = s.confirm("release-publish", nil, "publish", guided)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = s.confirm("release-publish", nil, "publish release", nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/publish", rec.Header().Get("Location"))
	_, _, published = r.counts()
	require.Zero(t, published)

	// Token + typed phrase + from=publish publishes, once.
	id := s.startedJob(s.confirm("release-publish", nil, "publish release", guided))
	require.Equal(t, jobs.StatusSucceeded, s.wait(id).Status)
	_, _, published = r.counts()
	require.Equal(t, 1, published)
}

func TestStaticSiteDoesNotReferenceAdmin(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	site := filepath.Join(filepath.Dir(file), "..", "..", "site")
	if _, err := os.Stat(site); err != nil {
		t.Skip("site/ not present")
	}
	forbidden := regexp.MustCompile(`httpadmin|["'(=]/(operations|jobs|publish)\b`)
	skip := map[string]bool{"public": true, "resources": true, "generated": true, "node_modules": true}
	err := filepath.WalkDir(site, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".html", ".toml", ".yaml", ".yml", ".json", ".js", ".md", ".css":
		default:
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2<<20 {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loc := forbidden.FindIndex(data); loc != nil {
			t.Errorf("%s references the admin: %q", path, data[loc[0]:loc[1]])
		}
		return nil
	})
	require.NoError(t, err)
}

func TestCloseCancelsJobsAndStopsWaitingAfterTheBound(t *testing.T) {
	cfg := e2eConfig(e2eRoot(t, maintenanceCatalogYAML), envMap(nil), readyReleaser())
	cfg.ShutdownWait = 100 * time.Millisecond
	a, closeFn, err := NewAdmin(context.Background(), cfg)
	require.NoError(t, err)

	cancelled := make(chan struct{})
	cooperative, err := a.manager.Submit(context.Background(), jobs.Spec{Kind: "sync", Resources: []string{"database"},
		Run: func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
			<-ctx.Done()
			close(cancelled)
			return jobs.Outcome{}, ctx.Err()
		}})
	require.NoError(t, err)
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	_, err = a.manager.Submit(context.Background(), jobs.Spec{Kind: "release-publish", Resources: []string{"release"},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			<-stuck // like a started publish: ignores cancellation
			return jobs.Outcome{Status: jobs.StatusSucceeded, Final: true}, nil
		}})
	require.NoError(t, err)

	start := time.Now()
	closeFn()
	require.Less(t, time.Since(start), 5*time.Second, "shutdown never hangs on uncancellable work")
	<-cancelled
	got, _ := a.Job(cooperative.ID)
	require.Equal(t, jobs.StatusCancelled, got.Status)
}

func TestInvalidReleaseRepoDisablesReleaseButStarts(t *testing.T) {
	cfg := e2eConfig(e2eRoot(t, maintenanceCatalogYAML), envMap(map[string]string{ReleaseRepoEnv: "evil/repo; rm -rf /"}), readyReleaser())
	s := startStack(t, cfg)
	require.False(t, s.a.ReleaseConfigured())
	for _, a := range s.a.Actions() {
		require.False(t, strings.HasPrefix(a.Name, "release-"), a.Name)
	}
	require.Equal(t, http.StatusOK, s.get("/publish").Code)

	off := startStack(t, e2eConfig(e2eRoot(t, maintenanceCatalogYAML), envMap(map[string]string{ReleaseRepoEnv: ReleaseDisabled}), nil))
	require.False(t, off.a.ReleaseConfigured())
}
