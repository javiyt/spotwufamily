package bootstrap

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
	"regexp"
	"slices"
	"strings"
	"time"

	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

// ReleasePRConfig locates the checkout release-pr works on.
type ReleasePRConfig struct {
	// RepoRoot is the absolute root of the operator's checkout; it must be
	// the top level of a git working tree.
	RepoRoot string
	// Repo is the GitHub repository (owner/name) the pull request targets.
	Repo string
	// BaseBranch defaults to "main".
	BaseBranch string
	// Releaser tells whether gh is authenticated and whether the pinned
	// manifest matches the local database.
	Releaser maintenance.Releaser
	// TempDir is the parent of the temporary worktree; it defaults to the
	// system temp dir (outside the repository).
	TempDir string
	// Logger receives fixed lines naming the failed git/gh subcommand only.
	Logger *slog.Logger
}

const (
	defaultPRBase   = "main"
	prCatalogFile   = "data/artists.yaml"
	prCommandLimit  = 4 << 20
	prCleanupBudget = 30 * time.Second
)

var prURLLine = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+`)

// releasePRPort builds the pull request with git and gh: fixed arguments,
// no shell, stdin closed. It never writes to the operator's working tree,
// index, HEAD or branches; it commits in a detached temporary worktree and
// pushes the commit straight to the remote branch.
type releasePRPort struct {
	cfg ReleasePRConfig
	// afterPush runs right after a successful push (tests).
	afterPush func()
}

var _ maintenance.PRPort = (*releasePRPort)(nil)

// NewReleasePRPort returns the git/gh adapter of release-pr.
func NewReleasePRPort(cfg ReleasePRConfig) *releasePRPort {
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = defaultPRBase
	}
	return &releasePRPort{cfg: cfg}
}

func (g *releasePRPort) baseRef() string { return "refs/remotes/origin/" + g.cfg.BaseBranch }

// commandEnv is the process environment without the variables that would
// point git at another repository, and with credential prompts disabled.
func commandEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_TERMINAL_PROMPT":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

// exec runs name with args in dir. The error names only the subcommand,
// never output, paths or URLs.
func (g *releasePRPort) exec(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = commandEnv()
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if stdout.Len() > prCommandLimit {
		return "", fmt.Errorf("%s: output too large: %w", name, maintenance.ErrPRGit)
	}
	if err != nil {
		sub := prSubcommand(args)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("%s %s: %w", name, sub, ctxErr)
		}
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return stdout.String(), &prCmdError{name: name, sub: sub, code: code}
	}
	return stdout.String(), nil
}

// prCmdError is a failed command; it wraps ErrPRGit and carries the exit
// code so callers can tell "absent" (exit 1) from a failure.
type prCmdError struct {
	name, sub string
	code      int
}

func (e *prCmdError) Error() string {
	return fmt.Sprintf("%s %s: exit status %d", e.name, e.sub, e.code)
}
func (e *prCmdError) Unwrap() error { return maintenance.ErrPRGit }

func exitCode(err error) int {
	var ce *prCmdError
	if errors.As(err, &ce) {
		return ce.code
	}
	return -1
}

// prSubcommand is the first non-flag argument (after -C dir), for errors
// and logs.
func prSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-C" || a == "-c":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

func (g *releasePRPort) git(ctx context.Context, dir string, args ...string) (string, error) {
	return g.exec(ctx, dir, "git", append([]string{"-C", dir}, args...)...)
}

func (g *releasePRPort) log(msg string, err error) {
	if g.cfg.Logger == nil {
		return
	}
	var ce *prCmdError
	if errors.As(err, &ce) {
		g.cfg.Logger.Warn("release-pr: "+msg, "command", ce.name+" "+ce.sub, "exit", ce.code)
		return
	}
	g.cfg.Logger.Warn("release-pr: " + msg)
}

// toplevel reports whether RepoRoot is the top level of a git checkout, so
// git never walks up to an enclosing repository.
func (g *releasePRPort) toplevel(ctx context.Context) bool {
	out, err := g.git(ctx, g.cfg.RepoRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	want, err1 := filepath.EvalSymlinks(g.cfg.RepoRoot)
	got, err2 := filepath.EvalSymlinks(strings.TrimSpace(out))
	return err1 == nil && err2 == nil && want == got
}

// originResolved: the checkout has an origin remote and a fetched base.
func (g *releasePRPort) originResolved(ctx context.Context) bool {
	if !g.toplevel(ctx) {
		return false
	}
	if _, err := g.git(ctx, g.cfg.RepoRoot, "remote", "get-url", "origin"); err != nil {
		return false
	}
	_, err := g.git(ctx, g.cfg.RepoRoot, "rev-parse", "--verify", "--quiet", g.baseRef()+"^{commit}")
	return err == nil
}

// prGitHubURL matches the GitHub remote URL forms, capturing owner/repo.
var prGitHubURL = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// originRepo parses owner/repo from a GitHub remote URL; "" otherwise.
func originRepo(url string) string {
	m := prGitHubURL.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return m[1]
}

// originMatchesRepo reports whether origin's configured URL (raw, before any
// insteadOf rewrite) names cfg.Repo, so the push and the pull request target
// the same repository. The URL never leaves this function.
func (g *releasePRPort) originMatchesRepo(ctx context.Context) bool {
	out, err := g.git(ctx, g.cfg.RepoRoot, "config", "--get", "remote.origin.url")
	if err != nil {
		return false
	}
	repo := originRepo(out)
	return repo != "" && strings.EqualFold(repo, g.cfg.Repo)
}

// blob is the object id of path at rev, "" when it does not exist there.
func (g *releasePRPort) blob(ctx context.Context, rev, path string) (string, error) {
	out, err := g.git(ctx, g.cfg.RepoRoot, "rev-parse", "--verify", "--quiet", rev+":"+path)
	if err != nil {
		if ctx.Err() == nil && exitCode(err) == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// localBlob is the object id the working copy of path would have, "" when
// it does not exist.
func (g *releasePRPort) localBlob(ctx context.Context, path string) (string, error) {
	if !g.present(path) {
		return "", nil
	}
	out, err := g.git(ctx, g.cfg.RepoRoot, "hash-object", "--", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *releasePRPort) present(path string) bool {
	info, err := os.Lstat(filepath.Join(g.cfg.RepoRoot, filepath.FromSlash(path)))
	return err == nil && info.Mode().IsRegular()
}

// Check is read-only and makes no network calls (the base branch is the
// last fetched one).
func (g *releasePRPort) Check(ctx context.Context) (maintenance.PRPrereq, error) {
	p := maintenance.PRPrereq{Repo: g.cfg.Repo, BaseBranch: g.cfg.BaseBranch}
	pre, err := g.cfg.Releaser.Preflight(ctx)
	if err != nil {
		return maintenance.PRPrereq{}, err
	}
	p.GhAuthenticated = pre.GhAuthenticated
	p.ManifestMatchesLocal = pre.PinnedMatchesLocal && pre.PinnedContentSHA256 != "" &&
		pre.PinnedContentSHA256 == pre.LocalContentSHA256
	p.ManifestContentSHA256 = pre.PinnedContentSHA256
	p.ReleaseTag = pre.LastReleaseTag
	for _, f := range maintenance.ReleasePRFiles {
		if g.present(f) {
			p.FilesPresent = append(p.FilesPresent, f)
		}
	}
	p.OriginResolved = g.originResolved(ctx)
	if !p.OriginResolved {
		return p, nil
	}
	p.OriginMatchesRepo = g.originMatchesRepo(ctx)
	digest, err := g.catalogDigest(ctx)
	if err != nil {
		return maintenance.PRPrereq{}, err
	}
	p.CatalogDigest = digest
	for _, f := range p.FilesPresent {
		local, err := g.localBlob(ctx, f)
		if err != nil {
			return maintenance.PRPrereq{}, err
		}
		remote, err := g.blob(ctx, g.baseRef(), f)
		if err != nil {
			return maintenance.PRPrereq{}, err
		}
		if local != remote {
			p.FilesChanged = append(p.FilesChanged, f)
		}
	}
	return p, nil
}

func (g *releasePRPort) catalogDigest(ctx context.Context) (string, error) {
	c, err := yamlcatalog.NewStore().Load(ctx, filepath.Join(g.cfg.RepoRoot, filepath.FromSlash(prCatalogFile)))
	if err != nil {
		return "", fmt.Errorf("release-pr: catalog cannot be read: %w", maintenance.ErrPRGit)
	}
	return artists.FingerprintCatalog(c).Digest, nil
}

// Changes diffs the catalog on the last fetched base branch against the
// local one. Without a readable base catalog it returns nil.
func (g *releasePRPort) Changes(ctx context.Context) ([]artists.ArtistChange, error) {
	base, err := g.git(ctx, g.cfg.RepoRoot, "show", g.baseRef()+":"+prCatalogFile)
	if err != nil {
		return nil, nil
	}
	// Internal failures (temp files, an unreadable catalog) only cost the
	// generic title, and their errors would carry paths: return nothing.
	dir, err := os.MkdirTemp(g.cfg.TempDir, "spotwu-release-pr-")
	if err != nil {
		return nil, nil
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "artists.yaml")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		return nil, nil
	}
	store := yamlcatalog.NewStore()
	prev, err := store.Load(ctx, path)
	if err != nil {
		return nil, nil
	}
	cur, err := store.Load(ctx, filepath.Join(g.cfg.RepoRoot, filepath.FromSlash(prCatalogFile)))
	if err != nil {
		return nil, nil
	}
	return artists.DiffCatalog(artists.FingerprintCatalog(prev), artists.FingerprintCatalog(cur)), nil
}

// Open fetches origin, refuses conflicting origin changes, commits the
// request's files in a temporary detached worktree of the fetched base
// branch, pushes the commit to the request's branch (never forced) and opens
// the pull request unless one is open. A branch already on origin with the
// same file content is reused; with other content Open fails. The worktree
// and the temporary directory are always removed.
func (g *releasePRPort) Open(ctx context.Context, req maintenance.PRRequest) (maintenance.PRResult, error) {
	if err := maintenance.ValidatePRRequest(req); err != nil {
		return maintenance.PRResult{}, err
	}
	if err := g.requireIdentity(ctx); err != nil {
		return maintenance.PRResult{}, err
	}
	if !g.toplevel(ctx) {
		return maintenance.PRResult{}, fmt.Errorf("release-pr: not a checkout: %w", maintenance.ErrPRGit)
	}
	if !g.originMatchesRepo(ctx) {
		return maintenance.PRResult{}, maintenance.ErrPROriginMismatch
	}
	if _, err := g.git(ctx, g.cfg.RepoRoot, "fetch", "--quiet", "--no-tags", "origin"); err != nil {
		g.log("fetch failed", err)
		return maintenance.PRResult{}, err
	}
	if _, err := g.git(ctx, g.cfg.RepoRoot, "rev-parse", "--verify", "--quiet", g.baseRef()+"^{commit}"); err != nil {
		return maintenance.PRResult{}, err
	}
	want, err := g.desired(ctx, req.Files)
	if err != nil {
		return maintenance.PRResult{}, err
	}
	changed := false
	for _, f := range req.Files {
		base, err := g.blob(ctx, g.baseRef(), f)
		if err != nil {
			return maintenance.PRResult{}, err
		}
		changed = changed || want[f] != base
	}
	if !changed {
		return maintenance.PRResult{}, maintenance.ErrPRNothingToOpen
	}

	tmp, err := os.MkdirTemp(g.cfg.TempDir, "spotwu-release-pr-")
	if err != nil {
		return maintenance.PRResult{}, fmt.Errorf("release-pr: temp dir: %w", maintenance.ErrPRGit)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	exists, err := g.remoteBranch(ctx, req.Branch, want)
	if err != nil {
		return maintenance.PRResult{}, err
	}
	if !exists {
		if err := g.commitAndPush(ctx, tmp, req); err != nil {
			return maintenance.PRResult{}, err
		}
		if g.afterPush != nil {
			g.afterPush()
		}
	}
	res, err := g.openPR(ctx, tmp, req, func() error {
		// The branch an open pull request shows must still be ours.
		ok, err := g.remoteBranch(ctx, req.Branch, want)
		if err == nil && !ok {
			err = maintenance.ErrPRBranchDiverged
		}
		return err
	})
	if errors.Is(err, maintenance.ErrPRBranchDiverged) {
		return maintenance.PRResult{}, maintenance.ErrPRBranchDiverged
	}
	if err != nil {
		g.log("pull request not opened", err)
		return maintenance.PRResult{}, errors.Join(maintenance.ErrPRNotOpened, err)
	}
	return res, nil
}

func (g *releasePRPort) requireIdentity(ctx context.Context) error {
	for _, key := range []string{"user.name", "user.email"} {
		out, err := g.git(ctx, g.cfg.RepoRoot, "config", "--get", key)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || strings.TrimSpace(out) == "" {
			return maintenance.ErrGitIdentity
		}
	}
	return nil
}

// desired maps every ReleasePRFiles path to the object id the pull request
// commit gives it, and refuses when origin changed a file since the
// checkout's merge base while the local copy differs from origin's.
func (g *releasePRPort) desired(ctx context.Context, files []string) (map[string]string, error) {
	mbOut, err := g.git(ctx, g.cfg.RepoRoot, "merge-base", "HEAD", g.baseRef())
	if err != nil {
		return nil, err
	}
	mb := strings.TrimSpace(mbOut)
	want := map[string]string{}
	for _, f := range maintenance.ReleasePRFiles {
		base, err := g.blob(ctx, g.baseRef(), f)
		if err != nil {
			return nil, err
		}
		local := base
		if slices.Contains(files, f) && g.present(f) {
			if local, err = g.localBlob(ctx, f); err != nil {
				return nil, err
			}
		}
		old, err := g.blob(ctx, mb, f)
		if err != nil {
			return nil, err
		}
		if base != old && local != base {
			return nil, maintenance.ErrPROriginNewer
		}
		want[f] = local
	}
	return want, nil
}

// remoteBranch reports whether branch exists on origin. An existing branch
// is reused only when it is one commit on top of the base that changes
// nothing but ReleasePRFiles, and its tip has exactly the wanted content of
// every one of them.
func (g *releasePRPort) remoteBranch(ctx context.Context, branch string, want map[string]string) (bool, error) {
	ref := "refs/heads/" + branch
	out, err := g.git(ctx, g.cfg.RepoRoot, "ls-remote", "--heads", "origin", ref)
	if err != nil {
		g.log("ls-remote failed", err)
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	tracking := "refs/remotes/origin/" + branch
	if _, err := g.git(ctx, g.cfg.RepoRoot, "fetch", "--quiet", "--no-tags", "origin", "+"+ref+":"+tracking); err != nil {
		g.log("fetch of the branch failed", err)
		return false, err
	}
	// Only one commit on top of the base, touching only the allowed files.
	count, err := g.git(ctx, g.cfg.RepoRoot, "rev-list", "--count", g.baseRef()+".."+tracking)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(count) != "1" {
		return false, maintenance.ErrPRBranchDiverged
	}
	names, err := g.git(ctx, g.cfg.RepoRoot, "diff", "--name-only", "--no-renames", g.baseRef()+"..."+tracking)
	if err != nil {
		return false, err
	}
	for _, name := range strings.Split(strings.TrimSpace(names), "\n") {
		if name != "" && !slices.Contains(maintenance.ReleasePRFiles, name) {
			return false, maintenance.ErrPRBranchDiverged
		}
	}
	for _, f := range maintenance.ReleasePRFiles {
		got, err := g.blob(ctx, tracking, f)
		if err != nil {
			return false, err
		}
		if got != want[f] {
			return false, maintenance.ErrPRBranchDiverged
		}
	}
	return true, nil
}

// commitAndPush commits req.Files in a detached worktree of the base branch
// inside tmp and pushes it to req.Branch without force. The worktree is
// removed even when the context is cancelled.
func (g *releasePRPort) commitAndPush(ctx context.Context, tmp string, req maintenance.PRRequest) (err error) {
	wt := filepath.Join(tmp, "worktree")
	// An empty hooks directory: the operator's hooks (post-checkout, LFS,
	// pre-push, …) never run for this commit.
	hooks := filepath.Join(tmp, "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		return fmt.Errorf("release-pr: hooks dir: %w", maintenance.ErrPRGit)
	}
	noHooks := "core.hooksPath=" + hooks
	if _, err := g.git(ctx, g.cfg.RepoRoot, "-c", noHooks, "worktree", "add", "--quiet", "--detach", wt, g.baseRef()); err != nil {
		g.log("worktree add failed", err)
		g.removeWorktree(ctx, wt)
		return err
	}
	defer g.removeWorktree(ctx, wt)
	for _, f := range req.Files {
		if !g.present(f) {
			continue
		}
		if err := copyFile(filepath.Join(g.cfg.RepoRoot, filepath.FromSlash(f)), filepath.Join(wt, filepath.FromSlash(f))); err != nil {
			return fmt.Errorf("release-pr: copy: %w", maintenance.ErrPRGit)
		}
	}
	if _, err := g.git(ctx, wt, append([]string{"add", "--"}, req.Files...)...); err != nil {
		g.log("add failed", err)
		return err
	}
	if _, err := g.git(ctx, wt, "diff", "--cached", "--quiet"); err == nil {
		return maintenance.ErrPRNothingToOpen
	} else if exitCode(err) != 1 {
		return err
	}
	if _, err := g.git(ctx, wt, "-c", noHooks, "commit", "--quiet", "--no-verify", "-m", req.Title); err != nil {
		g.log("commit failed", err)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := g.git(ctx, wt, "-c", noHooks, "push", "--quiet", "--no-verify", "origin", "HEAD:refs/heads/"+req.Branch); err != nil {
		g.log("push failed", err)
		return err
	}
	return nil
}

// removeWorktree removes the worktree, detached from the job's cancel. Only
// when that fails is its directory deleted and the stale record pruned.
func (g *releasePRPort) removeWorktree(ctx context.Context, wt string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), prCleanupBudget)
	defer cancel()
	if _, err := g.git(ctx, g.cfg.RepoRoot, "worktree", "remove", "--force", wt); err != nil {
		g.log("worktree remove failed", err)
		_ = os.RemoveAll(wt)
		_, _ = g.git(ctx, g.cfg.RepoRoot, "worktree", "prune")
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// openPR reuses an open pull request for the branch or creates one. gh runs
// in tmp, never in the operator's checkout.
func (g *releasePRPort) openPR(ctx context.Context, tmp string, req maintenance.PRRequest, verify func() error) (maintenance.PRResult, error) {
	out, err := g.exec(ctx, tmp, "gh", "pr", "list", "--repo", g.cfg.Repo, "--head", req.Branch,
		"--state", "open", "--json", "url", "--limit", "1")
	if err != nil {
		return maintenance.PRResult{}, err
	}
	var open []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &open); err != nil {
		return maintenance.PRResult{}, fmt.Errorf("gh pr list: unexpected output: %w", maintenance.ErrPRGit)
	}
	if len(open) > 0 {
		if !prURLLine.MatchString(open[0].URL) {
			return maintenance.PRResult{}, fmt.Errorf("gh pr list: unexpected url: %w", maintenance.ErrPRGit)
		}
		if err := verify(); err != nil {
			return maintenance.PRResult{}, err
		}
		return maintenance.PRResult{URL: prURLLine.FindString(open[0].URL), Reused: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return maintenance.PRResult{}, err
	}
	bodyFile := filepath.Join(tmp, "pr-body.md")
	if err := os.WriteFile(bodyFile, []byte(req.Body), 0o600); err != nil {
		return maintenance.PRResult{}, fmt.Errorf("release-pr: body: %w", maintenance.ErrPRGit)
	}
	out, err = g.exec(ctx, tmp, "gh", "pr", "create", "--repo", g.cfg.Repo, "--base", g.cfg.BaseBranch,
		"--head", req.Branch, "--title", req.Title, "--body-file", bodyFile)
	if err != nil {
		return maintenance.PRResult{}, err
	}
	url := prURLLine.FindString(out)
	if url == "" {
		return maintenance.PRResult{}, fmt.Errorf("gh pr create: no url: %w", maintenance.ErrPRGit)
	}
	return maintenance.PRResult{URL: url}, nil
}
