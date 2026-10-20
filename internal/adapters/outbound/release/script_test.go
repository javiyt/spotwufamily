package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

// These tests never run the real gh, never touch the network and never run
// the release script against the repository: a stub python3 (or, for the
// end-to-end tests, the real script copied into a temp repository with stub
// gh and go) is put first on a temp PATH.

const testRepo = "javiyt/spotwufamily"

const fakeToken = "ghp_FAKEtokenFAKEtokenFAKEtoken1234"

const okPreflight = `{"gh_authenticated": true, "go_available": true, "package_dir_clean": false,
"pinned_matches_local": false, "local_content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "pinned_content_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
"last_release_tag": "catalog-old", "prepared_tag": "catalog-new", "prepared_content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
"prepared_assets": ["catalog.db.gz", "catalog.release.json", "catalog.snapshot.sql.gz"]}`

// stub is a fake python3 that records its argv and environment per call and
// answers each subcommand from files in dir.
type stub struct {
	dir  string // records and canned answers
	root string // fake repository root
}

func newStub(t *testing.T) *stub {
	t.Helper()
	base := t.TempDir()
	s := &stub{dir: filepath.Join(base, "stub"), root: filepath.Join(base, "repo")}
	bin := filepath.Join(base, "bin")
	for _, d := range []string{s.dir, bin, filepath.Join(s.root, "scripts", "automation")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(s.root, "scripts", "automation", "catalog-release.py"), []byte("# never run\n"), 0o644))
	script := fmt.Sprintf(`#!/bin/sh
d='%s'
sub="$2"
printf '%%s\n' "$@" > "$d/argv-$sub"
printf '%%s\n' "$(pwd)" > "$d/cwd-$sub"
env > "$d/env-$sub"
echo "$sub" >> "$d/calls"
if [ -f "$d/sleep-$sub" ]; then
  sleep 30 &
  echo $! > "$d/child.pid"
  wait
fi
[ -f "$d/stdout-$sub" ] && cat "$d/stdout-$sub"
[ -f "$d/stderr-$sub" ] && cat "$d/stderr-$sub" >&2
if [ -f "$d/exit-$sub" ]; then exit "$(cat "$d/exit-$sub")"; fi
exit 0
`, s.dir)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "python3"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return s
}

func (s *stub) answer(t *testing.T, sub, stdout, stderr string, exit int) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "stdout-"+sub), []byte(stdout), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "stderr-"+sub), []byte(stderr), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "exit-"+sub), []byte(strconv.Itoa(exit)), 0o644))
}

func (s *stub) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, "calls"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	return strings.Fields(string(data))
}

func (s *stub) argv(t *testing.T, sub string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, "argv-"+sub))
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (s *stub) env(t *testing.T, sub string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, "env-"+sub))
	require.NoError(t, err)
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		out[k] = v
	}
	return out
}

func (s *stub) script(t *testing.T, logger *slog.Logger) *Script {
	t.Helper()
	sc, err := New(Config{RepoRoot: s.root, Repo: testRepo, Logger: logger, Timeout: 10 * time.Second})
	require.NoError(t, err)
	return sc
}

func TestArgvIsTheFixedListForEachSubcommand(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", okPreflight, "", 0)
	s.answer(t, "prepare", "Prepared catalog-new in /somewhere/build/catalog-release\n", "", 0)
	s.answer(t, "publish", "Published https://github.com/javiyt/spotwufamily/releases/tag/catalog-new\n", "", 0)
	sc := s.script(t, nil)
	ctx := context.Background()

	_, err := sc.Preflight(ctx)
	require.NoError(t, err)
	_, err = sc.Prepare(ctx)
	require.NoError(t, err)
	_, err = sc.Publish(ctx)
	require.NoError(t, err)

	script := filepath.Join(s.root, "scripts", "automation", "catalog-release.py")
	for _, sub := range []string{"preflight", "prepare", "publish"} {
		require.Equal(t, []string{script, sub, "--repo", testRepo}, s.argv(t, sub), sub)
		cwd, err := os.ReadFile(filepath.Join(s.dir, "cwd-"+sub))
		require.NoError(t, err)
		wantRoot, _ := filepath.EvalSymlinks(s.root)
		gotRoot, _ := filepath.EvalSymlinks(strings.TrimSpace(string(cwd)))
		require.Equal(t, wantRoot, gotRoot, "working directory is the repository root")
	}
}

func TestEnvironmentIsTheAllowlistOnly(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", okPreflight, "", 0)
	t.Setenv("GH_TOKEN", fakeToken)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "spotify-secret")
	t.Setenv("PYTHONPATH", "/evil")
	t.Setenv("GOFLAGS", "-mod=mod")

	_, err := s.script(t, nil).Preflight(context.Background())
	require.NoError(t, err)

	env := s.env(t, "preflight")
	// The stub is a shell script; sh itself exports these.
	for _, shellAdded := range []string{"PWD", "SHLVL", "_", "OLDPWD"} {
		delete(env, shellAdded)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"GH_TOKEN", "LANG", "PATH"}
	if os.Getenv("HOME") != "" {
		want = []string{"GH_TOKEN", "HOME", "LANG", "PATH"}
	}
	require.Equal(t, want, keys)
	require.Equal(t, fakeToken, env["GH_TOKEN"], "an already-set token is passed through")
}

func TestPreflightReportsMissingGhAuth(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", `{"gh_authenticated": false, "go_available": true, "package_dir_clean": true,
"pinned_matches_local": true, "local_content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "pinned_content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
"last_release_tag": "catalog-old", "prepared_tag": null, "prepared_content_sha256": null, "prepared_assets": []}`, "", 0)
	p, err := s.script(t, nil).Preflight(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.Prereq{
		GoAvailable: true, PackageDirClean: true, PinnedMatchesLocal: true,
		LocalContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PinnedContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LastReleaseTag: "catalog-old",
	}, p)
}

func TestPreflightParsesFullReport(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", okPreflight, "", 0)
	p, err := s.script(t, nil).Preflight(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.Prereq{
		GhAuthenticated: true, GoAvailable: true,
		LocalContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PinnedContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", LastReleaseTag: "catalog-old",
		PreparedTag: "catalog-new", PreparedContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PreparedAssets: []string{"catalog.db.gz", "catalog.release.json", "catalog.snapshot.sql.gz"},
	}, p)
}

func TestPreflightRejectsMalformedOutput(t *testing.T) {
	for name, out := range map[string]string{
		"not json":       "hello\n",
		"missing key":    `{"gh_authenticated": true}`,
		"two objects":    okPreflight + "\n" + okPreflight,
		"asset with dir": strings.Replace(okPreflight, `"catalog.db.gz"`, `"../catalog.db.gz"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t)
			s.answer(t, "preflight", out, "", 0)
			_, err := s.script(t, nil).Preflight(context.Background())
			require.ErrorIs(t, err, maintenance.ErrReleasePrerequisite)
		})
	}
}

func TestMissingPython3IsAPrerequisiteError(t *testing.T) {
	s := newStub(t)
	t.Setenv("PATH", t.TempDir())
	_, err := s.script(t, nil).Preflight(context.Background())
	require.ErrorIs(t, err, maintenance.ErrReleasePrerequisite)
}

func TestPublishRefusedWhenGhUnauthenticated(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", strings.Replace(okPreflight, `"gh_authenticated": true`, `"gh_authenticated": false`, 1), "", 0)
	s.answer(t, "publish", "Published https://github.com/javiyt/spotwufamily/releases/tag/catalog-new\n", "", 0)
	info, err := s.script(t, nil).Publish(context.Background())
	require.ErrorIs(t, err, maintenance.ErrGhNotAuthenticated)
	require.Equal(t, maintenance.PublishInfo{}, info)
	require.Equal(t, []string{"preflight"}, s.calls(t), "publish never runs")
}

func TestPublishParsesPublishedTag(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", okPreflight, "", 0)
	s.answer(t, "publish", "Published https://github.com/javiyt/spotwufamily/releases/tag/catalog-new\n", "", 0)
	info, err := s.script(t, nil).Publish(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.PublishInfo{Tag: "catalog-new", Published: true}, info)
}

func TestPublishRejectsUnknownOutputAndFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		exit int
	}{
		"unknown line":     {"Something else\n", 0},
		"other repository": {"Published https://github.com/evil/repo/releases/tag/catalog-new\n", 0},
		"exit status":      {"", 1},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t)
			s.answer(t, "preflight", okPreflight, "", 0)
			s.answer(t, "publish", tc.out, "", tc.exit)
			info, err := s.script(t, nil).Publish(context.Background())
			require.ErrorIs(t, err, maintenance.ErrReleasePrerequisite)
			require.False(t, info.Published)
		})
	}
}

func TestPrepareReturnsPreparedPackage(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", okPreflight, "", 0)
	s.answer(t, "prepare", "Prepared catalog-new in /somewhere/build/catalog-release\n", "", 0)
	info, err := s.script(t, nil).Prepare(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.PackageInfo{
		Tag: "catalog-new", ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Assets: []string{"catalog.db.gz", "catalog.release.json", "catalog.snapshot.sql.gz"},
	}, info)
	require.Equal(t, []string{"prepare", "preflight"}, s.calls(t))
}

func TestPrepareRefusesPackageThatDoesNotMatchItsOutput(t *testing.T) {
	s := newStub(t)
	s.answer(t, "preflight", okPreflight, "", 0)
	s.answer(t, "prepare", "Prepared catalog-other in /somewhere\n", "", 0)
	_, err := s.script(t, nil).Prepare(context.Background())
	require.ErrorIs(t, err, maintenance.ErrReleasePrerequisite)
}

func TestRepoArgValidated(t *testing.T) {
	s := newStub(t)
	for _, repo := range []string{"", "javiyt", "javiyt/spotwufamily/extra", "javi yt/repo", "javiyt/repo;rm -rf",
		"-javiyt/repo", "javiyt/-repo", "../x", "./x", "javiyt/..", "$(id)/x", "javiyt/repo\n", "javiyt/répo"} {
		_, err := New(Config{RepoRoot: s.root, Repo: repo})
		require.ErrorIs(t, err, ErrInvalidRepo, "%q", repo)
	}
	for _, repo := range []string{"javiyt/spotwufamily", "Some_Org.x/repo-name.go"} {
		_, err := New(Config{RepoRoot: s.root, Repo: repo})
		require.NoError(t, err, repo)
	}
	_, err := New(Config{RepoRoot: "relative/root", Repo: testRepo})
	require.Error(t, err, "the repository root must be absolute")
	require.Empty(t, s.calls(t), "validation never runs anything")
}

func TestScriptOutputNeverLeaksToken(t *testing.T) {
	t.Setenv("GH_TOKEN", fakeToken)
	leak := "token " + fakeToken + " github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz at /Users/someone/secret\n"
	for _, sub := range []string{"preflight", "prepare", "publish"} {
		t.Run(sub, func(t *testing.T) {
			s := newStub(t)
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			s.answer(t, "preflight", okPreflight, "", 0)
			s.answer(t, sub, leak, leak, 1)
			sc := s.script(t, logger)
			var (
				res any
				err error
			)
			switch sub {
			case "preflight":
				res, err = sc.Preflight(context.Background())
			case "prepare":
				res, err = sc.Prepare(context.Background())
			case "publish":
				res, err = sc.Publish(context.Background())
			}
			require.Error(t, err)
			got, _ := json.Marshal(res)
			for _, out := range []string{err.Error(), string(got), logs.String()} {
				require.NotContains(t, out, fakeToken)
				require.NotContains(t, out, "github_pat_")
				require.NotContains(t, out, "FAKEtoken")
			}
			require.NotContains(t, err.Error(), "/Users/someone", "errors never carry script output")
			require.Contains(t, logs.String(), "[redacted]", "stderr is logged only sanitized")
			require.LessOrEqual(t, len(logs.String()), 1024, "the logged stderr is truncated")
		})
	}
}

func TestStderrIsTruncatedInLogs(t *testing.T) {
	s := newStub(t)
	var logs bytes.Buffer
	s.answer(t, "preflight", "", strings.Repeat("x", 10000), 1)
	_, err := s.script(t, slog.New(slog.NewTextHandler(&logs, nil))).Preflight(context.Background())
	require.Error(t, err)
	require.Less(t, len(logs.String()), 1024)
}

func TestTimeoutKillsTheSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix only")
	}
	s := newStub(t)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "sleep-preflight"), nil, 0o644))
	sc, err := New(Config{RepoRoot: s.root, Repo: testRepo, Timeout: 1500 * time.Millisecond})
	require.NoError(t, err)
	start := time.Now()
	_, err = sc.Preflight(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 10*time.Second)
	requireChildKilled(t, s)
}

func TestContextCancelKillsTheSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix only")
	}
	s := newStub(t)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "sleep-preflight"), nil, 0o644))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(1500*time.Millisecond, cancel)
	start := time.Now()
	_, err := s.script(t, nil).Preflight(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 10*time.Second)
	requireChildKilled(t, s)
}

// requireChildKilled checks the grandchild (the stub's sleep) died too.
func requireChildKilled(t *testing.T, s *stub) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, "child.pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) != nil
	}, 5*time.Second, 50*time.Millisecond, "child process %d still alive", pid)
}

// realScriptRepo copies the real release script into a temp repository with
// a small database, stub gh and go first on PATH, and the real python3.
type realScriptRepo struct {
	root, ghLog string
}

func newRealScriptRepo(t *testing.T) realScriptRepo {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	base := t.TempDir()
	r := realScriptRepo{root: filepath.Join(base, "repo"), ghLog: filepath.Join(base, "gh.log")}
	bin := filepath.Join(base, "bin")
	for _, d := range []string{bin, filepath.Join(r.root, "scripts", "automation"), filepath.Join(r.root, "data")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "automation", "catalog-release.py"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.root, "scripts", "automation", "catalog-release.py"), src, 0o644))
	require.NoError(t, os.Symlink(python, filepath.Join(bin, "python3")))
	// Stub gh: authenticated, records every call, uploads nothing.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
exit 0
`, r.ghLog)), 0o755))
	// Stub go: writes the snapshot file the script asks for.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
prev=""
for a in "$@"; do
  if [ "$prev" = "--snapshot" ] && [ "$3" = "db" ] && [ "$4" = "snapshot" ]; then printf 'fixture snapshot' > "$a"; fi
  prev="$a"
done
exit 0
`), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	cmd := exec.Command(python, "-c", "import sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nc.execute('create table t(x)')\nc.execute(\"insert into t values('a')\")\nc.commit()\nc.close()",
		filepath.Join(r.root, "data", "catalog.db"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return r
}

func (r realScriptRepo) ghCalls(t *testing.T) string {
	data, err := os.ReadFile(r.ghLog)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

func TestPublishNoChangesDoesNotCopyManifest(t *testing.T) {
	r := newRealScriptRepo(t)
	sc, err := New(Config{RepoRoot: r.root, Repo: testRepo})
	require.NoError(t, err)
	ctx := context.Background()

	pre, err := sc.Preflight(ctx)
	require.NoError(t, err)
	require.True(t, pre.GhAuthenticated && pre.GoAvailable && pre.PackageDirClean)
	require.NotEmpty(t, pre.LocalContentSHA256)
	require.Empty(t, pre.PinnedContentSHA256)

	info, err := sc.Prepare(ctx)
	require.NoError(t, err)
	require.Regexp(t, `^catalog-[A-Za-z0-9._-]+$`, info.Tag)
	require.Equal(t, pre.LocalContentSHA256, info.ContentSHA256)
	require.NotContains(t, r.ghCalls(t), "release", "prepare never talks to GitHub")

	// Pin a manifest with the same content: publish must change nothing.
	pointer := filepath.Join(r.root, "data", "catalog.release.json")
	pinned, err := os.ReadFile(filepath.Join(r.root, "build", "catalog-release", "catalog.release.json"))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(pinned, &m))
	m["tag"] = "catalog-previous"
	pinned, _ = json.MarshalIndent(m, "", "  ")
	require.NoError(t, os.WriteFile(pointer, pinned, 0o644))

	res, err := sc.Publish(ctx)
	require.NoError(t, err)
	require.Equal(t, maintenance.PublishInfo{Tag: info.Tag, NoChanges: true}, res)
	after, err := os.ReadFile(pointer)
	require.NoError(t, err)
	require.Equal(t, pinned, after, "the manifest is not copied")
	require.NotContains(t, r.ghCalls(t), "release create")
}

func TestPublishUploadsThroughGhAndPinsManifest(t *testing.T) {
	r := newRealScriptRepo(t)
	sc, err := New(Config{RepoRoot: r.root, Repo: testRepo})
	require.NoError(t, err)
	ctx := context.Background()
	info, err := sc.Prepare(ctx)
	require.NoError(t, err)

	res, err := sc.Publish(ctx)
	require.NoError(t, err)
	require.Equal(t, maintenance.PublishInfo{Tag: info.Tag, Published: true}, res)
	require.Contains(t, r.ghCalls(t), "release create "+info.Tag+" --repo "+testRepo)
	pre, err := sc.Preflight(ctx)
	require.NoError(t, err)
	require.True(t, pre.PinnedMatchesLocal)
	require.Equal(t, info.Tag, pre.LastReleaseTag)
}
