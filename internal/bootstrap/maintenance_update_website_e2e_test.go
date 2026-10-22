package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// The whole update-website run through NewAdmin and the HTTP adapter, over a
// temporary git checkout whose origin is a temp bare repository, a fake gh
// first on PATH, a real SQLite database and a fake release script. No test
// here reaches Spotify, GitHub or the real checkout.

const websiteE2ECatalog = `version: 1
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
    spotify_id: ` + gzaID + `
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 2
    notes: ""
`

// websiteE2EGh is a fake gh that keeps one pull request per head branch.
const websiteE2EGh = `#!/bin/sh
printf '%s\n' "$*" >> "$E2E_GH_LOG"
head=""; prev=""
for a in "$@"; do
  if [ "$prev" = "--head" ]; then head="$a"; fi
  prev="$a"
done
case "$1 $2" in
  "pr list")
    if [ -f "$E2E_GH_STATE/$head" ]; then printf '[{"url":"%s"}]\n' "$(cat "$E2E_GH_STATE/$head")"; else echo '[]'; fi ;;
  "pr create")
    n=$(ls "$E2E_GH_STATE" | wc -l | tr -d ' '); n=$((n+1))
    url="https://github.com/owner/repo/pull/$n"
    echo "$url" > "$E2E_GH_STATE/$head"; echo "$url" ;;
  *) exit 1 ;;
esac
`

// countingSyncFetcher is the fake Spotify port; it counts every call.
type countingSyncFetcher struct {
	mu    sync.Mutex
	calls int
}

func (f *countingSyncFetcher) hit() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
}

func (f *countingSyncFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *countingSyncFetcher) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	f.hit()
	return catalog.ArtistCandidate{SpotifyID: id, Name: id}, nil
}

func (f *countingSyncFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	f.hit()
	return nil, nil
}

func (f *countingSyncFetcher) GetAlbum(context.Context, string) (catalog.Release, error) {
	f.hit()
	return catalog.Release{}, nil
}

func (f *countingSyncFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	f.hit()
	return nil, nil
}

// dbReleaser is a fake release script: the local content hash is the real
// database digest, prepare packages it and publish pins it in the manifest
// file of the checkout.
type dbReleaser struct {
	mu                    sync.Mutex
	root, dbPath, catalog string
	prepared, pinned, tag string
	prepares, publishes   int
	onPrepare             func()
}

func (r *dbReleaser) Preflight(ctx context.Context) (maintenance.Prereq, error) {
	local, err := newProvenance(r.dbPath, r.catalog).DatabaseDigest(ctx)
	if err != nil {
		return maintenance.Prereq{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := maintenance.Prereq{GhAuthenticated: true, GoAvailable: true, PackageDirClean: r.prepared == "",
		LocalContentSHA256: local, PinnedContentSHA256: r.pinned, PinnedMatchesLocal: r.pinned == local,
		LastReleaseTag: r.tag}
	if r.prepared != "" {
		p.PreparedTag, p.PreparedContentSHA256 = fmt.Sprintf("catalog-%d", r.publishes+1), r.prepared
	}
	return p, nil
}

func (r *dbReleaser) Prepare(ctx context.Context) (maintenance.PackageInfo, error) {
	local, err := newProvenance(r.dbPath, r.catalog).DatabaseDigest(ctx)
	if err != nil {
		return maintenance.PackageInfo{}, err
	}
	r.mu.Lock()
	r.prepares++
	r.prepared = local
	tag := fmt.Sprintf("catalog-%d", r.publishes+1)
	hook := r.onPrepare
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return maintenance.PackageInfo{Tag: tag, ContentSHA256: local}, nil
}

func (r *dbReleaser) Publish(context.Context) (maintenance.PublishInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishes++
	r.tag = fmt.Sprintf("catalog-%d", r.publishes)
	r.pinned, r.prepared = r.prepared, ""
	manifest := `{"version":1,"repository":"owner/repo","tag":"` + r.tag + `","content_sha256":"` + r.pinned + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(r.root, "data", "catalog.release.json"), []byte(manifest), 0o644); err != nil {
		return maintenance.PublishInfo{}, err
	}
	return maintenance.PublishInfo{Tag: r.tag, Published: true}, nil
}

func (r *dbReleaser) counts() (prepares, publishes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prepares, r.publishes
}

type websiteE2E struct {
	t                      *testing.T
	root, bare, tmp, ghLog string
	main                   string
	cfg                    AdminConfig
	fetcher                *countingSyncFetcher
	releaser               *dbReleaser
}

// newWebsiteE2E builds the checkout (origin rewritten to a temp bare repo),
// the fake gh, an isolated HOME and git config, and a migrated database
// with a baseline full sync, so the database records the catalog it applied.
func newWebsiteE2E(t *testing.T) *websiteE2E {
	t.Helper()
	pr := newPRRepo(t) // git, HOME/config isolation, bare origin and clone
	base := filepath.Dir(pr.bare)
	e := &websiteE2E{t: t, root: pr.clone, bare: pr.bare, tmp: filepath.Join(base, "e2e-tmp"),
		ghLog: filepath.Join(base, "e2e-gh.log"), fetcher: &countingSyncFetcher{}}
	state := filepath.Join(base, "e2e-gh-state")
	require.NoError(t, os.MkdirAll(state, 0o755))
	require.NoError(t, os.MkdirAll(e.tmp, 0o755))
	writeFile(t, filepath.Join(base, "bin", "gh"), websiteE2EGh)
	require.NoError(t, os.Chmod(filepath.Join(base, "bin", "gh"), 0o755))
	t.Setenv("E2E_GH_LOG", e.ghLog)
	t.Setenv("E2E_GH_STATE", state)
	// release-pr's temporary worktree goes under the system temp dir.
	t.Setenv("TMPDIR", e.tmp)

	// Origin's main holds the catalog the site was last built from; the
	// operator's checkout starts from it with no local edits.
	gitRun(t, pr.other, "rm", "--quiet", "README.md")
	writeFile(t, filepath.Join(pr.other, "data", "artists.yaml"), websiteE2ECatalog)
	gitRun(t, pr.other, "add", "-A")
	gitRun(t, pr.other, "commit", "--quiet", "-m", "catalog")
	gitRun(t, pr.other, "push", "--quiet", "origin", "main")
	gitRun(t, e.root, "reset", "--quiet", "--hard")
	gitRun(t, e.root, "clean", "--quiet", "-fdx")
	gitRun(t, e.root, "pull", "--quiet", "--ff-only", "origin", "main")
	e.main = gitRun(t, e.bare, "rev-parse", "main")
	writeFile(t, filepath.Join(e.root, ".gitignore"), "/build/\n/site/\n/data/catalog.db*\n/data/*.jsonl.lock\n")

	e.releaser = &dbReleaser{root: e.root, dbPath: filepath.Join(e.root, "data", "catalog.db"),
		catalog: filepath.Join(e.root, "data", "artists.yaml")}
	env := envMap(map[string]string{"SPOTIFY_CLIENT_ID": "id", "SPOTIFY_CLIENT_SECRET": "secret", ReleaseRepoEnv: "owner/repo"})
	e.cfg = e2eConfig(e.root, env, e.releaser, withSyncFetcher(e.fetcher))
	migrateDB(t, e.cfg.DBPath)

	s := e.stack()
	sync := e.run(s, "sync", maintenance.Params{"scope": "all"})
	require.Equal(t, jobs.StatusSucceeded, sync.Status, sync.Message)
	return e
}

// stack is a fresh admin process (no cached pending view) over the fixture.
func (e *websiteE2E) stack() *stack { return startStack(e.t, e.cfg) }

func (e *websiteE2E) run(s *stack, action string, p maintenance.Params) jobs.Job {
	e.t.Helper()
	pv, tok, err := s.a.PreviewAction(context.Background(), action, p)
	require.NoError(e.t, err)
	j, err := s.a.RunAction(context.Background(), action, p, tok, pv.RequiresTyped)
	require.NoError(e.t, err)
	return s.wait(j.ID)
}

// editCatalog rewrites the catalog outside the admin, keeping its mtime, so
// only content digests can notice the edit.
func (e *websiteE2E) editCatalog(old, repl string) {
	e.t.Helper()
	path := e.cfg.CatalogPath
	info, err := os.Stat(path)
	require.NoError(e.t, err)
	body, err := os.ReadFile(path)
	require.NoError(e.t, err)
	edited := strings.Replace(string(body), old, repl, 1)
	require.NotEqual(e.t, string(body), edited)
	require.NoError(e.t, os.WriteFile(path, []byte(edited), 0o644))
	require.NoError(e.t, os.Chtimes(path, info.ModTime(), info.ModTime()))
}

func (e *websiteE2E) pending(s *stack) maintenance.PendingView {
	e.t.Helper()
	v, err := s.a.Pending(context.Background())
	require.NoError(e.t, err)
	return v
}

func (e *websiteE2E) ghCalls(prefix string) int {
	data, err := os.ReadFile(e.ghLog)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(e.t, err)
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (e *websiteE2E) branches() []string {
	out := strings.TrimSpace(gitRun(e.t, e.bare, "for-each-ref", "--format=%(refname:short)", "refs/heads/catalog-update-*"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func stageOf(v maintenance.PendingView, name string) maintenance.StageView {
	for _, st := range v.Stages {
		if st.Name == name {
			return st
		}
	}
	return maintenance.StageView{}
}

// pendingSection is the pending panel of the operations page.
func pendingSection(t *testing.T, s *stack) string {
	t.Helper()
	rec := s.get("/operations")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	start := strings.Index(body, `<section id="pending"`)
	require.GreaterOrEqual(t, start, 0, "the operations page shows the pending panel")
	end := strings.Index(body[start:], "</section>")
	require.Greater(t, end, 0)
	return body[start : start+end]
}

func outcomes(t *testing.T, j jobs.Job) map[string]string {
	t.Helper()
	require.NotNil(t, j.Result)
	require.NotNil(t, j.Result.Report)
	require.Equal(t, []string{"stage", "outcome", "detail"}, j.Result.Report.Columns)
	out := map[string]string{}
	for _, row := range j.Result.Report.Rows {
		out[row[0]] = row[1]
	}
	return out
}

func TestUpdateWebsiteEndToEnd(t *testing.T) {
	e := newWebsiteE2E(t)

	// (a) An external edit: a Spotify ID change and a new review line.
	e.editCatalog("spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU")
	reviews := filepath.Join(e.root, "data", "artists.yaml.reviews.jsonl")
	f, err := os.OpenFile(reviews, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("{\"slug\":\"wu-tang-clan\"}\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	s := e.stack()
	v := e.pending(s)
	require.Equal(t, maintenance.PendingChanges, v.State)
	require.Len(t, v.Changes, 1)
	require.Equal(t, "wu-tang-clan", v.Changes[0].Slug)
	require.True(t, v.Changes[0].NeedsSync)
	require.Equal(t, maintenance.NeedRequired, stageOf(v, maintenance.PlanSync).Need)

	rec := s.get("/api/v1/pending")
	require.Equal(t, http.StatusOK, rec.Code)
	var api struct {
		State   string `json:"state"`
		Count   int    `json:"pending_changes"`
		Changes []struct {
			Slug string `json:"slug"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &api))
	require.Equal(t, "pending", api.State)
	require.Equal(t, 1, api.Count)
	require.Equal(t, "wu-tang-clan", api.Changes[0].Slug)
	require.Contains(t, s.get("/").Body.String(), "1 artist modified since last database reconciliation")

	// (b) One confirmation runs every stage and opens one pull request.
	pv, _, err := s.a.PreviewAction(context.Background(), "update-website", nil)
	require.NoError(t, err)
	require.Equal(t, "update website", pv.RequiresTyped)
	j := e.run(s, "update-website", nil)
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%s %+v", j.Message, j.Result)
	got := outcomes(t, j)
	for _, name := range []string{maintenance.PlanReconcile, maintenance.PlanSeed, maintenance.PlanSync, maintenance.PlanSnapshot,
		maintenance.PlanExport, maintenance.PlanReleasePrepare, maintenance.PlanReleasePublish, maintenance.PlanPR} {
		require.Equal(t, maintenance.OutcomeDone, got[name], "stage %s: %v", name, got)
	}
	for _, name := range []string{maintenance.PlanBuild, maintenance.PlanDeploy} {
		require.Equal(t, maintenance.OutcomeSkipped, got[name], "manual stage %s: %v", name, got)
	}
	require.Contains(t, j.Result.Summary, "pull/1")

	branches := e.branches()
	require.Len(t, branches, 1)
	files := strings.Fields(gitRun(t, e.bare, "diff", "--name-only", "main", branches[0]))
	require.ElementsMatch(t, maintenance.ReleasePRFiles, files, "the branch carries exactly the three allowed files")
	require.Equal(t, 1, e.ghCalls("pr create"))
	_, published := e.releaser.counts()
	require.Equal(t, 1, published)
	require.Equal(t, e.main, gitRun(t, e.bare, "rev-parse", "main"), "main is never changed: merging happens on GitHub")
	entries, err := os.ReadDir(e.tmp)
	require.NoError(t, err)
	require.Empty(t, entries, "the temporary worktree is removed")

	s = e.stack()
	require.Equal(t, maintenance.PendingClean, e.pending(s).State)
	require.NotContains(t, s.get("/").Body.String(), "pending-banner")
	panel := pendingSection(t, s)
	require.Contains(t, panel, "Pull request open — waiting for merge and GitHub Pages deploy")
	require.Contains(t, panel, "not confirmed")
	lower := strings.ToLower(panel)
	for _, claim := range []string{"website published", "website is published", "website updated.", "site is live"} {
		require.NotContains(t, lower, claim)
	}

	// (c) A second run has nothing to do: no second release or pull request.
	info, ok := s.a.Action("update-website")
	require.True(t, ok)
	require.False(t, info.Available)
	require.Contains(t, info.UnavailableReason, "Nothing to update")
	_, _, err = s.a.PreviewAction(context.Background(), "update-website", nil)
	require.ErrorIs(t, err, maintenance.ErrUnavailable, "a second run cannot even be previewed")
	for _, st := range e.pending(s).Stages {
		require.NotEqual(t, maintenance.StageTodo, st.Status, "stage %s", st.Name)
	}
	require.Equal(t, 1, e.ghCalls("pr create"))
	_, published = e.releaser.counts()
	require.Equal(t, 1, published)
	require.Len(t, e.branches(), 1)

	// (d) A metadata-only edit skips the Spotify sync and still converges.
	e.editCatalog("name: GZA", "name: GZA (The Genius)")
	before := e.fetcher.count()
	s = e.stack()
	v = e.pending(s)
	require.Equal(t, maintenance.PendingChanges, v.State)
	require.Equal(t, maintenance.NeedNotNeeded, stageOf(v, maintenance.PlanSync).Need)
	require.Equal(t, maintenance.NeedRequired, stageOf(v, maintenance.PlanSeed).Need)
	j = e.run(s, "update-website", nil)
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%s %+v %v", j.Message, j.Result, j.Result.Report.Rows)
	got = outcomes(t, j)
	require.Equal(t, maintenance.OutcomeSkipped, got[maintenance.PlanSync])
	require.Equal(t, maintenance.OutcomeDone, got[maintenance.PlanSeed])
	require.Equal(t, before, e.fetcher.count(), "a metadata-only edit makes no Spotify call")
	require.Equal(t, maintenance.PendingClean, e.pending(e.stack()).State)
	require.Len(t, e.branches(), 2, "the new catalog goes on its own branch")
	require.Equal(t, 2, e.ghCalls("pr create"))
}

func TestUpdateWebsiteEndToEndStopsWhenTheCatalogChangesMidRun(t *testing.T) {
	e := newWebsiteE2E(t)
	e.editCatalog("spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU")
	// The fake release script edits the catalog while it packages.
	e.releaser.onPrepare = func() { e.editCatalog(`notes: ""`, `notes: "edited mid-run"`) }

	s := e.stack()
	j := e.run(s, "update-website", nil)
	require.Equal(t, jobs.StatusFailed, j.Status, "%+v", j.Result)
	require.Contains(t, j.Message, "The catalog changed outside the admin")
	got := outcomes(t, j)
	require.Equal(t, maintenance.OutcomeDone, got[maintenance.PlanReleasePrepare])
	require.NotEqual(t, maintenance.OutcomeDone, got[maintenance.PlanReleasePublish])
	require.NotEqual(t, maintenance.OutcomeDone, got[maintenance.PlanPR])

	require.Empty(t, e.branches(), "nothing is pushed")
	require.Zero(t, e.ghCalls("pr "), "gh is never asked for a pull request")
	_, published := e.releaser.counts()
	require.Zero(t, published)
	require.NotEqual(t, maintenance.PendingClean, e.pending(e.stack()).State)
}
