package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/release"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// These tests run the real release script copied into a temp repository,
// with stub go and gh first on PATH that print chatter on stdout like the
// real tools. They never run the real gh or touch the network.

const (
	e2eChatterPath = "/Users/someone/private/catalog.snapshot.sql.gz"
	e2eReleaseURL  = "https://github.com/javiyt/spotwufamily/releases/tag/"
)

type releaseRepo struct {
	root, ghLog, ghSlow, ghPid string
}

func newReleaseRepo(t *testing.T) releaseRepo {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	base := t.TempDir()
	r := releaseRepo{root: filepath.Join(base, "repo"), ghLog: filepath.Join(base, "gh.log"),
		ghSlow: filepath.Join(base, "gh-slow"), ghPid: filepath.Join(base, "gh.pid")}
	bin := filepath.Join(base, "bin")
	for _, d := range []string{bin, filepath.Join(r.root, "scripts", "automation"), filepath.Join(r.root, "data")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "automation", "catalog-release.py"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.root, "scripts", "automation", "catalog-release.py"), src, 0o644))
	require.NoError(t, os.Symlink(python, filepath.Join(bin, "python3")))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
if [ "$1 $2" = "auth status" ]; then echo "Logged in to github.com account someone"; fi
if [ "$1 $2" = "release create" ]; then
  if [ -f '%s' ]; then sleep "$(cat '%s')" & echo $! > '%s'; wait; fi
  echo "%s$3"
fi
exit 0
`, r.ghLog, r.ghSlow, r.ghSlow, r.ghPid, e2eReleaseURL)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
prev=""
for a in "$@"; do
  if [ "$prev" = "--snapshot" ] && [ "$4" = "snapshot" ]; then printf 'fixture snapshot' > "$a"; fi
  prev="$a"
done
echo "database snapshot written: `+e2eChatterPath+`"
echo "database verified: `+e2eChatterPath+`"
exit 0
`), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	migrateDB(t, filepath.Join(r.root, "data", "catalog.db"))
	return r
}

func (r releaseRepo) service(t *testing.T, timeout time.Duration) (*maintenance.Service, *jobs.Manager) {
	t.Helper()
	script, err := release.New(release.Config{RepoRoot: r.root, Repo: "javiyt/spotwufamily", Timeout: timeout})
	require.NoError(t, err)
	journal := filepath.Join(r.root, "build", "admin", "jobs.jsonl")
	actions, err := NewReleaseActions(ReleaseActionsConfig{
		RepoRoot: r.root, Repo: "javiyt/spotwufamily", DBPath: filepath.Join(r.root, "data", "catalog.db"),
		JournalPath: journal, Releaser: script,
	})
	require.NoError(t, err)
	n := 0
	m := jobs.NewManager(filesystem.NewJobJournal(journal), time.Now, func() string { n++; return strconv.Itoa(n) })
	return maintenance.NewService(m, actions, []byte("secret"), time.Now), m
}

// start previews and runs action, returning the job id.
func start(t *testing.T, svc *maintenance.Service, action, typed string) string {
	t.Helper()
	_, tok, err := svc.Preview(context.Background(), action, nil)
	require.NoError(t, err)
	j, err := svc.Run(context.Background(), action, nil, tok, typed)
	require.NoError(t, err)
	return j.ID
}

func finished(t *testing.T, m *jobs.Manager, id string) jobs.Job {
	t.Helper()
	m.Wait(id)
	j, ok := m.Get(id)
	require.True(t, ok)
	return j
}

func requireNoChatterIn(t *testing.T, j jobs.Job) {
	t.Helper()
	data, err := json.Marshal(j)
	require.NoError(t, err)
	for _, s := range []string{"/Users/someone", "database snapshot", "database verified", "https://", "account someone"} {
		require.NotContains(t, string(data), s)
	}
}

func (r releaseRepo) prepare(t *testing.T, svc *maintenance.Service, m *jobs.Manager) string {
	t.Helper()
	j := finished(t, m, start(t, svc, "release-prepare", ""))
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%+v", j)
	requireNoChatterIn(t, j)
	require.Len(t, j.Result.Report.Rows, 1)
	return j.Result.Report.Rows[0][0]
}

func TestReleaseEndToEndDespiteToolChatter(t *testing.T) {
	r := newReleaseRepo(t)
	svc, m := r.service(t, 0)
	tag := r.prepare(t, svc, m)

	j := finished(t, m, start(t, svc, "release-publish", "publish release"))
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%+v", j)
	require.Equal(t, "Published catalog release "+tag+".", j.Result.Summary)
	requireNoChatterIn(t, j)
	gh, err := os.ReadFile(r.ghLog)
	require.NoError(t, err)
	require.Contains(t, string(gh), "release create "+tag+" --repo javiyt/spotwufamily")
	pinned, err := os.ReadFile(filepath.Join(r.root, "data", "catalog.release.json"))
	require.NoError(t, err)
	require.Contains(t, string(pinned), tag)
}

func TestReleasePublishCancelledMidUploadStillFinishes(t *testing.T) {
	r := newReleaseRepo(t)
	svc, m := r.service(t, 0)
	tag := r.prepare(t, svc, m)
	require.NoError(t, os.WriteFile(r.ghSlow, []byte("3"), 0o644))

	id := start(t, svc, "release-publish", "publish release")
	require.Eventually(t, func() bool { _, err := os.Stat(r.ghPid); return err == nil }, 20*time.Second, 50*time.Millisecond)
	require.NoError(t, m.Cancel(id))
	j := finished(t, m, id)
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%+v", j)
	require.Equal(t, "Published catalog release "+tag+". A cancel was requested, but publishing cannot be stopped once it has started.", j.Result.Summary)
	pinned, err := os.ReadFile(filepath.Join(r.root, "data", "catalog.release.json"))
	require.NoError(t, err)
	require.Contains(t, string(pinned), tag, "the upload finished and the manifest is pinned")
}

func TestReleasePublishTimeoutMidUploadReportsPartialRelease(t *testing.T) {
	r := newReleaseRepo(t)
	svc, m := r.service(t, 5*time.Second)
	tag := r.prepare(t, svc, m)
	require.NoError(t, os.WriteFile(r.ghSlow, []byte("30"), 0o644))

	j := finished(t, m, start(t, svc, "release-publish", "publish release"))
	require.Equal(t, jobs.StatusFailed, j.Status, "%+v", j)
	require.Equal(t, "Publishing was interrupted; a partial release "+tag+
		" may exist on GitHub — check the Releases page before retrying.", j.Message)
	require.NotContains(t, j.Message, "https://")
	_, err := os.Stat(filepath.Join(r.root, "data", "catalog.release.json"))
	require.True(t, os.IsNotExist(err), "an interrupted publish never pins the manifest")
	require.True(t, strings.HasPrefix(tag, "catalog-"))
}
