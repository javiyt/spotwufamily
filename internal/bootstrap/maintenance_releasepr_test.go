package bootstrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// These tests never reach a real remote: origin is a bare repository in a
// temp dir and gh is a shell script placed first on PATH.

const prCatalogV1 = `version: 1
profile_sync_policy: allow_unreviewed
artists:
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

const prCatalogV2 = prCatalogV1 + `  - slug: rza
    name: RZA
    spotify_id: ` + rzaID + `
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 3
    notes: ""
`

const fakeGh = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
case "$1 $2" in
  "pr list")
    if [ -f "$FAKE_GH_STATE" ]; then echo '[{"url":"https://github.com/owner/repo/pull/7"}]'; else echo '[]'; fi ;;
  "pr create")
    if [ -n "$FAKE_GH_FAIL" ]; then echo "boom $HOME" >&2; exit 1; fi
    touch "$FAKE_GH_STATE"; echo "https://github.com/owner/repo/pull/7" ;;
  *) exit 1 ;;
esac
`

const prOriginURL = "https://github.com/owner/repo.git"

type prRepo struct {
	t                       *testing.T
	bare, clone, other, tmp string
	ghLog                   string
	manifestSHA             string
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func manifestJSON(sha string) string {
	return `{"version":1,"repository":"owner/repo","tag":"catalog-20261022T100000Z-abc","content_sha256":"` + sha + `"}` + "\n"
}

// newPRRepo builds origin (bare), a working clone with the operator's
// uncommitted catalog edits, another clone for concurrent changes and a
// fake gh. HOME and git's global/system config are isolated.
func newPRRepo(t *testing.T) *prRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_GLOBAL", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
	ghDir := filepath.Join(root, "bin")
	writeFile(t, filepath.Join(ghDir, "gh"), fakeGh)
	require.NoError(t, os.Chmod(filepath.Join(ghDir, "gh"), 0o755))
	t.Setenv("PATH", ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &prRepo{t: t, bare: filepath.Join(root, "origin.git"), clone: filepath.Join(root, "work"),
		other: filepath.Join(root, "other"), tmp: filepath.Join(root, "tmp"), ghLog: filepath.Join(root, "gh.log"),
		manifestSHA: strings.Repeat("d", 64)}
	t.Setenv("FAKE_GH_LOG", r.ghLog)
	t.Setenv("FAKE_GH_STATE", filepath.Join(root, "gh.state"))
	require.NoError(t, os.MkdirAll(r.tmp, 0o755))

	gitRun(t, root, "init", "--quiet", "--bare", "--initial-branch=main", r.bare)
	seed := filepath.Join(root, "seed")
	gitRun(t, root, "init", "--quiet", "--initial-branch=main", seed)
	identity(t, seed)
	writeFile(t, filepath.Join(seed, "data", "artists.yaml"), prCatalogV1)
	writeFile(t, filepath.Join(seed, "data", "artists.yaml.reviews.jsonl"), "{\"slug\":\"gza\"}\n")
	writeFile(t, filepath.Join(seed, "data", "catalog.release.json"), manifestJSON(strings.Repeat("b", 64)))
	writeFile(t, filepath.Join(seed, "README.md"), "readme\n")
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "commit", "--quiet", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", r.bare)
	gitRun(t, seed, "push", "--quiet", "origin", "main")

	for _, dir := range []string{r.clone, r.other} {
		gitRun(t, root, "clone", "--quiet", r.bare, dir)
		identity(t, dir)
	}
	// origin is configured as the GitHub repository and rewritten to the
	// temp bare repository, so fetch and push never leave the temp dir.
	gitRun(t, r.clone, "config", "remote.origin.url", prOriginURL)
	gitRun(t, r.clone, "config", "url."+r.bare+".insteadOf", prOriginURL)
	// The operator's uncommitted edits, plus unrelated dirt that must never
	// be committed.
	writeFile(t, filepath.Join(r.clone, "data", "artists.yaml"), prCatalogV2)
	writeFile(t, filepath.Join(r.clone, "data", "artists.yaml.reviews.jsonl"), "{\"slug\":\"gza\"}\n{\"slug\":\"rza\"}\n")
	writeFile(t, filepath.Join(r.clone, "data", "catalog.release.json"), manifestJSON(r.manifestSHA))
	writeFile(t, filepath.Join(r.clone, "README.md"), "dirty readme\n")
	writeFile(t, filepath.Join(r.clone, "scratch.txt"), "untracked\n")
	gitRun(t, r.clone, "add", "README.md") // something staged in the operator's index
	require.Equal(t, r.bare, strings.TrimSpace(gitRun(t, r.clone, "remote", "get-url", "origin")), "origin is the temp bare repo")
	return r
}

func identity(t *testing.T, dir string) {
	gitRun(t, dir, "config", "user.name", "Test Operator")
	gitRun(t, dir, "config", "user.email", "operator@example.invalid")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
}

func (r *prRepo) releaser(gh bool) *countingReleaser {
	return &countingReleaser{prereq: maintenance.Prereq{
		GhAuthenticated: gh, GoAvailable: true, PackageDirClean: true,
		LocalContentSHA256: r.manifestSHA, PinnedContentSHA256: r.manifestSHA, PinnedMatchesLocal: true,
		LastReleaseTag: "catalog-20261022T100000Z-abc",
	}}
}

func (r *prRepo) port(gh bool) *releasePRPort {
	return NewReleasePRPort(ReleasePRConfig{
		RepoRoot: r.clone, Repo: "owner/repo", BaseBranch: "main", Releaser: r.releaser(gh), TempDir: r.tmp,
	})
}

// state is everything of the operator's checkout release-pr must not touch.
func (r *prRepo) state() string {
	t := r.t
	return strings.Join([]string{
		gitRun(t, r.clone, "status", "--porcelain=v1", "--untracked-files=all", "--ignored"),
		gitRun(t, r.clone, "rev-parse", "HEAD"),
		gitRun(t, r.clone, "symbolic-ref", "HEAD"),
		gitRun(t, r.clone, "diff", "--cached", "--binary"),
		gitRun(t, r.clone, "diff", "--binary"),
		gitRun(t, r.clone, "worktree", "list", "--porcelain"),
		gitRun(t, r.clone, "for-each-ref", "refs/heads"),
		gitRun(t, r.clone, "stash", "list"),
	}, "\n--\n")
}

func (r *prRepo) ghCalls() string {
	data, err := os.ReadFile(r.ghLog)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	require.NoError(r.t, err)
	return string(data)
}

func (r *prRepo) remoteBranches() string {
	return strings.TrimSpace(gitRun(r.t, r.bare, "for-each-ref", "--format=%(refname:short)", "refs/heads/catalog-update-*"))
}

func (r *prRepo) requireClean() {
	entries, err := os.ReadDir(r.tmp)
	require.NoError(r.t, err)
	require.Empty(r.t, entries, "the temporary worktree directory is removed")
	wt := gitRun(r.t, r.clone, "worktree", "list", "--porcelain")
	require.Equal(r.t, 1, strings.Count(wt, "worktree "), "only the operator's worktree remains: %s", wt)
}

func (r *prRepo) request(p maintenance.PRPrereq) maintenance.PRRequest {
	branch, ok := maintenance.PRBranch(p.CatalogDigest, p.ManifestContentSHA256)
	require.True(r.t, ok)
	return maintenance.PRRequest{Branch: branch, Title: "Update catalog: rza", Body: "body\n", Files: maintenance.ReleasePRFiles}
}

func requireNoPaths(t *testing.T, r *prRepo, texts ...string) {
	t.Helper()
	for _, s := range texts {
		for _, bad := range []string{r.clone, r.bare, r.tmp, os.Getenv("HOME"), "operator@example.invalid", "Test Operator"} {
			require.NotContains(t, s, bad)
		}
		require.NotContains(t, s, "/private/")
		require.NotContains(t, s, "/var/")
	}
}

func TestReleasePRPortOpensBranchWithExactlyTheFiles(t *testing.T) {
	r := newPRRepo(t)
	before := r.state()
	port := r.port(true)
	ctx := context.Background()

	p, err := port.Check(ctx)
	require.NoError(t, err)
	require.True(t, p.GhAuthenticated)
	require.True(t, p.OriginResolved)
	require.True(t, p.ManifestMatchesLocal)
	require.Equal(t, "owner/repo", p.Repo)
	require.Equal(t, "main", p.BaseBranch)
	require.Equal(t, maintenance.ReleasePRFiles, p.FilesPresent)
	require.ElementsMatch(t, maintenance.ReleasePRFiles, p.FilesChanged)

	changes, err := port.Changes(ctx)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "rza", changes[0].Slug)
	require.Contains(t, changes[0].Kinds, artists.ChangeAdded)

	req := r.request(p)
	res, err := port.Open(ctx, req)
	require.NoError(t, err)
	require.False(t, res.Reused)
	require.Equal(t, "https://github.com/owner/repo/pull/7", res.URL)
	requireNoPaths(t, r, res.URL)

	stat := gitRun(t, r.bare, "show", "--name-only", "--format=%s%n%an", req.Branch)
	lines := strings.Fields(strings.TrimSpace(stat))
	require.Contains(t, stat, "Update catalog: rza")
	require.Contains(t, stat, "Test Operator", "committed as the operator's git user")
	files := strings.Split(strings.TrimSpace(strings.SplitN(stat, "Test Operator", 2)[1]), "\n")
	require.ElementsMatch(t, maintenance.ReleasePRFiles, files, "exactly the three files: %v", lines)
	require.Equal(t, strings.TrimSpace(gitRun(t, r.bare, "rev-parse", "main")),
		strings.TrimSpace(gitRun(t, r.bare, "rev-parse", req.Branch+"^")), "built on origin/main")
	require.Equal(t, prCatalogV2, gitRun(t, r.bare, "show", req.Branch+":data/artists.yaml"))
	require.Equal(t, "readme\n", gitRun(t, r.bare, "show", req.Branch+":README.md"), "operator dirt is not committed")

	calls := r.ghCalls()
	require.Equal(t, 1, strings.Count(calls, "pr create"))
	require.Contains(t, calls, "pr create --repo owner/repo --base main --head "+req.Branch+" --title Update catalog: rza --body-file ")
	require.NotContains(t, calls, "--force")
	require.Equal(t, before, r.state(), "operator working tree, index, HEAD and branch unchanged")
	r.requireClean()

	// Second run: same branch, open PR reused, nothing pushed again.
	tip := gitRun(t, r.bare, "rev-parse", req.Branch)
	res, err = port.Open(ctx, req)
	require.NoError(t, err)
	require.True(t, res.Reused)
	require.Equal(t, "https://github.com/owner/repo/pull/7", res.URL)
	require.Equal(t, 1, strings.Count(r.ghCalls(), "pr create"), "no second PR")
	require.Equal(t, tip, gitRun(t, r.bare, "rev-parse", req.Branch), "the branch is not pushed again")
	require.Equal(t, before, r.state())
	r.requireClean()
}

func TestReleasePRPortAbortsOnNewerOriginChanges(t *testing.T) {
	r := newPRRepo(t)
	writeFile(t, filepath.Join(r.other, "data", "artists.yaml"), prCatalogV1+"# edited elsewhere\n")
	gitRun(t, r.other, "commit", "--quiet", "-am", "concurrent edit")
	gitRun(t, r.other, "push", "--quiet", "origin", "main")

	before := r.state()
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	_, err = port.Open(context.Background(), r.request(p))
	require.ErrorIs(t, err, maintenance.ErrPROriginNewer)
	requireNoPaths(t, r, err.Error())
	require.Empty(t, r.remoteBranches(), "nothing pushed")
	require.Empty(t, r.ghCalls(), "gh never ran")
	require.Equal(t, before, r.state())
	r.requireClean()
}

func TestReleasePRPortRemovesWorktreeOnFailure(t *testing.T) {
	r := newPRRepo(t)
	hook := filepath.Join(r.bare, "hooks", "pre-receive")
	writeFile(t, hook, "#!/bin/sh\necho rejected >&2\nexit 1\n")
	require.NoError(t, os.Chmod(hook, 0o755))

	before := r.state()
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	_, err = port.Open(context.Background(), r.request(p))
	require.ErrorIs(t, err, maintenance.ErrPRGit)
	requireNoPaths(t, r, err.Error())
	require.NotContains(t, err.Error(), "rejected", "command output is never in errors")
	require.Empty(t, r.remoteBranches())
	require.Equal(t, before, r.state())
	r.requireClean()
}

func TestReleasePRPortRejectsOtherFiles(t *testing.T) {
	r := newPRRepo(t)
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	req := r.request(p)
	req.Files = []string{"data/artists.yaml", "README.md"}
	_, err = port.Open(context.Background(), req)
	require.ErrorIs(t, err, maintenance.ErrPRInvalidRequest)
	require.Empty(t, r.remoteBranches())
	r.requireClean()
}

func TestReleasePRPortNeedsGitIdentity(t *testing.T) {
	r := newPRRepo(t)
	gitRun(t, r.clone, "config", "--unset", "user.email")
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	_, err = port.Open(context.Background(), r.request(p))
	require.ErrorIs(t, err, maintenance.ErrGitIdentity)
	require.Empty(t, r.remoteBranches())
	r.requireClean()
}

func TestReleasePRPortRefusesDivergedBranch(t *testing.T) {
	r := newPRRepo(t)
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	req := r.request(p)
	writeFile(t, filepath.Join(r.other, "data", "artists.yaml"), prCatalogV1+"# someone else's branch\n")
	gitRun(t, r.other, "commit", "--quiet", "-am", "other")
	gitRun(t, r.other, "push", "--quiet", "origin", "HEAD:refs/heads/"+req.Branch)
	tip := gitRun(t, r.bare, "rev-parse", req.Branch)

	_, err = port.Open(context.Background(), req)
	require.ErrorIs(t, err, maintenance.ErrPRBranchDiverged)
	require.Equal(t, tip, gitRun(t, r.bare, "rev-parse", req.Branch), "never force-pushed")
	require.Empty(t, r.ghCalls())
	r.requireClean()
}

func TestReleasePRPortCancelAfterPushIsRerunnable(t *testing.T) {
	r := newPRRepo(t)
	before := r.state()
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	req := r.request(p)

	ctx, cancel := context.WithCancel(context.Background())
	port.afterPush = cancel
	_, err = port.Open(ctx, req)
	require.ErrorIs(t, err, maintenance.ErrPRNotOpened)
	require.Equal(t, req.Branch, r.remoteBranches(), "the branch was pushed")
	require.NotContains(t, r.ghCalls(), "pr create")
	r.requireClean()
	tip := gitRun(t, r.bare, "rev-parse", req.Branch)

	port.afterPush = nil
	res, err := port.Open(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.Reused)
	require.Equal(t, 1, strings.Count(r.ghCalls(), "pr create"))
	require.Equal(t, tip, gitRun(t, r.bare, "rev-parse", req.Branch), "the pushed branch is reused, not pushed again")
	require.Equal(t, before, r.state())
	r.requireClean()
}

func TestReleasePRPortGhFailureAfterPush(t *testing.T) {
	r := newPRRepo(t)
	t.Setenv("FAKE_GH_FAIL", "1")
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	_, err = port.Open(context.Background(), r.request(p))
	require.ErrorIs(t, err, maintenance.ErrPRNotOpened)
	requireNoPaths(t, r, err.Error())
	r.requireClean()
}

func TestReleasePRUnavailableWithoutGh(t *testing.T) {
	r := newPRRepo(t)
	a := maintenance.NewReleasePR(maintenance.PRDeps{Port: r.port(false)})
	ok, reason := a.Available()
	require.False(t, ok)
	require.Contains(t, reason, "gh auth login")
}

func TestReleasePRUnavailableOutsideACheckout(t *testing.T) {
	r := newPRRepo(t)
	dir := t.TempDir()
	port := NewReleasePRPort(ReleasePRConfig{RepoRoot: dir, Repo: "owner/repo", Releaser: r.releaser(true), TempDir: r.tmp})
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	require.False(t, p.OriginResolved)
	ok, reason := maintenance.NewReleasePR(maintenance.PRDeps{Port: port}).Available()
	require.False(t, ok)
	require.NotContains(t, reason, dir)
}

func TestReleasePRActionEndToEnd(t *testing.T) {
	r := newPRRepo(t)
	before := r.state()
	port := r.port(true)
	a := maintenance.NewReleasePR(maintenance.PRDeps{Port: port, Changes: port.Changes})
	ok, reason := a.Available()
	require.True(t, ok, reason)
	pv, err := a.Preview(context.Background(), nil)
	require.NoError(t, err)
	var texts []string
	for _, l := range pv.Scope {
		texts = append(texts, l.Label+" "+l.Value)
	}
	requireNoPaths(t, r, texts...)
	spec, err := a.Spec(maintenance.WithVerifiedFingerprint(context.Background(), pv.Fingerprint), nil)
	require.NoError(t, err)
	out, err := spec.Run(context.Background(), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status)
	require.Contains(t, out.Result.Summary, "https://github.com/owner/repo/pull/7")
	requireNoPaths(t, r, out.Result.Summary)
	require.Contains(t, r.ghCalls(), "--title Update catalog: rza")
	require.Equal(t, before, r.state())
	r.requireClean()
}

func TestOriginRepoParsing(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/Owner/Repo.git":      "Owner/Repo",
		"https://github.com/owner/repo":          "owner/repo",
		"git@github.com:owner/repo.git":          "owner/repo",
		"ssh://git@github.com/owner/repo.git":    "owner/repo",
		"https://gitlab.com/owner/repo.git":      "",
		"https://github.com/owner/repo/extra":    "",
		"https://user:tok@github.com/owner/repo": "",
	} {
		require.Equal(t, want, originRepo(url), url)
	}
}

func TestReleasePRPortRefusesOriginOfAnotherRepo(t *testing.T) {
	r := newPRRepo(t)
	gitRun(t, r.clone, "config", "remote.origin.url", "git@github.com:someone/fork.git")
	gitRun(t, r.clone, "config", "url."+r.bare+".insteadOf", "git@github.com:someone/fork.git")
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	require.True(t, p.OriginResolved)
	require.False(t, p.OriginMatchesRepo)
	ok, reason := maintenance.NewReleasePR(maintenance.PRDeps{Port: port}).Available()
	require.False(t, ok)
	require.Contains(t, reason, "does not point at the configured repository")
	require.NotContains(t, reason, "fork")

	p.CatalogDigest, p.ManifestContentSHA256 = strings.Repeat("c", 64), r.manifestSHA
	_, err = port.Open(context.Background(), r.request(p))
	require.ErrorIs(t, err, maintenance.ErrPROriginMismatch)
	require.NotContains(t, err.Error(), "fork")
	require.Empty(t, r.remoteBranches())
	require.Empty(t, r.ghCalls())
}

func TestReleasePRPortRefusesBranchWithOtherChanges(t *testing.T) {
	r := newPRRepo(t)
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	req := r.request(p)
	// The same three files as the operator's, plus a README change.
	for _, f := range maintenance.ReleasePRFiles {
		data, err := os.ReadFile(filepath.Join(r.clone, filepath.FromSlash(f)))
		require.NoError(t, err)
		writeFile(t, filepath.Join(r.other, filepath.FromSlash(f)), string(data))
	}
	writeFile(t, filepath.Join(r.other, "README.md"), "smuggled\n")
	gitRun(t, r.other, "commit", "--quiet", "-am", "catalog plus extra")
	gitRun(t, r.other, "push", "--quiet", "origin", "HEAD:refs/heads/"+req.Branch)

	_, err = port.Open(context.Background(), req)
	require.ErrorIs(t, err, maintenance.ErrPRBranchDiverged)
	require.Empty(t, r.ghCalls(), "gh never ran")
	r.requireClean()
}

func TestReleasePRPortRefusesBranchWithTwoCommits(t *testing.T) {
	r := newPRRepo(t)
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	req := r.request(p)
	writeFile(t, filepath.Join(r.other, "data", "artists.yaml"), prCatalogV1+"# step\n")
	gitRun(t, r.other, "commit", "--quiet", "-am", "first")
	for _, f := range maintenance.ReleasePRFiles {
		data, err := os.ReadFile(filepath.Join(r.clone, filepath.FromSlash(f)))
		require.NoError(t, err)
		writeFile(t, filepath.Join(r.other, filepath.FromSlash(f)), string(data))
	}
	gitRun(t, r.other, "commit", "--quiet", "-am", "second")
	gitRun(t, r.other, "push", "--quiet", "origin", "HEAD:refs/heads/"+req.Branch)

	_, err = port.Open(context.Background(), req)
	require.ErrorIs(t, err, maintenance.ErrPRBranchDiverged)
	require.Empty(t, r.ghCalls())
}

func TestReleasePRPortChecksBranchBeforeReusingOpenPR(t *testing.T) {
	r := newPRRepo(t)
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	req := r.request(p)
	writeFile(t, os.Getenv("FAKE_GH_STATE"), "") // gh reports an open PR
	port.afterPush = func() {
		// Someone adds a commit to the branch before the PR is looked up.
		gitRun(t, r.other, "fetch", "--quiet", "origin")
		gitRun(t, r.other, "checkout", "--quiet", "-B", "x", "origin/"+req.Branch)
		writeFile(t, filepath.Join(r.other, "README.md"), "smuggled\n")
		gitRun(t, r.other, "commit", "--quiet", "-am", "extra")
		gitRun(t, r.other, "push", "--quiet", "origin", "HEAD:refs/heads/"+req.Branch)
	}
	_, err = port.Open(context.Background(), req)
	require.ErrorIs(t, err, maintenance.ErrPRBranchDiverged)
	require.NotContains(t, r.ghCalls(), "pr create")
	r.requireClean()
}

func TestReleasePRPortSkipsOperatorHooks(t *testing.T) {
	r := newPRRepo(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, name := range []string{"post-checkout", "pre-commit", "commit-msg", "post-commit", "pre-push"} {
		hook := filepath.Join(r.clone, ".git", "hooks", name)
		writeFile(t, hook, "#!/bin/sh\necho "+name+" >> '"+marker+"'\n")
		require.NoError(t, os.Chmod(hook, 0o755))
	}
	before := r.state()
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	_, err = port.Open(context.Background(), r.request(p))
	require.NoError(t, err)
	require.NoFileExists(t, marker, "no operator hook ran")
	require.Equal(t, before, r.state())
	r.requireClean()
}

// requireOriginRefused checks that a push-redirecting origin configuration
// makes release-pr unavailable and Open refuse before anything is pushed.
func requireOriginRefused(t *testing.T, r *prRepo) {
	t.Helper()
	port := r.port(true)
	p, err := port.Check(context.Background())
	require.NoError(t, err)
	require.True(t, p.OriginResolved)
	require.False(t, p.OriginMatchesRepo)
	ok, reason := maintenance.NewReleasePR(maintenance.PRDeps{Port: port}).Available()
	require.False(t, ok)
	require.Contains(t, reason, "does not point at the configured repository")
	p.CatalogDigest, p.ManifestContentSHA256 = strings.Repeat("c", 64), r.manifestSHA
	_, err = port.Open(context.Background(), r.request(p))
	require.ErrorIs(t, err, maintenance.ErrPROriginMismatch)
	require.NotContains(t, err.Error(), "fork")
	require.Empty(t, r.remoteBranches(), "nothing pushed to origin")
	require.Empty(t, r.ghCalls(), "gh never ran")
	r.requireClean()
}

// forkBare is a second temp bare repository the push could be redirected to.
func forkBare(t *testing.T, r *prRepo) string {
	fork := filepath.Join(t.TempDir(), "fork.git")
	gitRun(t, r.clone, "init", "--quiet", "--bare", fork)
	return fork
}

func requireForkUntouched(t *testing.T, fork string) {
	require.Empty(t, strings.TrimSpace(gitRun(t, fork, "for-each-ref", "refs/heads")), "nothing pushed to the other repository")
}

func TestReleasePRPortRefusesPushURLOfAnotherRepo(t *testing.T) {
	r := newPRRepo(t)
	fork := forkBare(t, r)
	gitRun(t, r.clone, "config", "remote.origin.pushurl", "git@github.com:someone/fork.git")
	gitRun(t, r.clone, "config", "url."+fork+".insteadOf", "git@github.com:someone/fork.git")
	requireOriginRefused(t, r)
	requireForkUntouched(t, fork)
}

func TestReleasePRPortAcceptsPushURLOfSameRepo(t *testing.T) {
	r := newPRRepo(t)
	gitRun(t, r.clone, "config", "remote.origin.pushurl", "git@github.com:Owner/Repo.git")
	gitRun(t, r.clone, "config", "url."+r.bare+".insteadOf", "git@github.com:Owner/Repo.git")
	p, err := r.port(true).Check(context.Background())
	require.NoError(t, err)
	require.True(t, p.OriginMatchesRepo)
}

func TestReleasePRPortRefusesPushInsteadOf(t *testing.T) {
	r := newPRRepo(t)
	fork := forkBare(t, r)
	gitRun(t, r.clone, "config", "url."+fork+".pushInsteadOf", "https://github.com/owner/")
	requireOriginRefused(t, r)
	requireForkUntouched(t, fork)
}
