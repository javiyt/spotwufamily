// Package release runs scripts/automation/catalog-release.py with fixed
// arguments for the admin release actions.
//
// Security boundary: the command is always
//
//	python3 <root>/scripts/automation/catalog-release.py <sub> --repo <repo>
//
// where <sub> comes from a closed set of constants and <repo> is validated at
// construction and passed as its own argv element (no shell is involved). The
// child gets a minimal environment, runs in the repository root under a
// timeout and is killed with its process group when the context ends. Its
// stdout is parsed and never echoed; its stderr is only ever logged,
// truncated and with credentials redacted.
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
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

// ErrInvalidRepo is returned by New for a repository that is not owner/name.
var ErrInvalidRepo = errors.New("release: repository must be owner/name")

// scriptRel is the release script, relative to the repository root.
const scriptRel = "scripts/automation/catalog-release.py"

// subcommand is the closed set of script subcommands the adapter runs.
type subcommand string

const (
	subPreflight subcommand = "preflight"
	subPrepare   subcommand = "prepare"
	subPublish   subcommand = "publish"
)

// Default per-call timeouts. Preflight hashes the database; prepare backs it
// up and runs `go run` twice; publish uploads the assets.
var defaultTimeouts = map[subcommand]time.Duration{
	subPreflight: 2 * time.Minute,
	subPrepare:   30 * time.Minute,
	subPublish:   30 * time.Minute,
}

const (
	maxStdout    = 64 << 10
	maxStderr    = 64 << 10
	maxLoggedLen = 300
)

var (
	repoPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	tagPattern     = regexp.MustCompile(`^catalog-[A-Za-z0-9._-]+$`)
	shaPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	assetPattern   = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
	preparedLine   = regexp.MustCompile(`^Prepared (catalog-[A-Za-z0-9._-]+) in .+$`)
	noChangesLine  = "No catalog content changes; keeping current release."
	tokenPatterns  = regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|(?i)(token|authorization|password|secret)\s*[:=]?\s*\S+)`)
	allowedEnvKeys = []string{"PATH", "HOME", "LANG"}
	tokenEnvKeys   = []string{"GH_TOKEN", "GITHUB_TOKEN"}
)

// Config configures a Script.
type Config struct {
	// RepoRoot is the absolute repository root: the working directory and
	// the location of the script.
	RepoRoot string
	// Repo is the GitHub repository (owner/name) passed as --repo.
	Repo string
	// Logger receives at most a short, sanitized excerpt of the script's
	// stderr when a call fails. Nil discards it.
	Logger *slog.Logger
	// Timeout, when positive, bounds every call; otherwise each subcommand
	// has its own default.
	Timeout time.Duration
}

// Script runs the release script. It implements maintenance.Releaser.
type Script struct {
	root, script, repo string
	logger             *slog.Logger
	timeout            time.Duration
	publishedLine      *regexp.Regexp
}

var _ maintenance.Releaser = (*Script)(nil)

// New validates the configuration. It runs nothing.
func New(cfg Config) (*Script, error) {
	if !validRepo(cfg.Repo) {
		return nil, ErrInvalidRepo
	}
	if !filepath.IsAbs(cfg.RepoRoot) {
		return nil, errors.New("release: repository root must be absolute")
	}
	root := filepath.Clean(cfg.RepoRoot)
	return &Script{
		root:    root,
		script:  filepath.Join(root, filepath.FromSlash(scriptRel)),
		repo:    cfg.Repo,
		logger:  cfg.Logger,
		timeout: cfg.Timeout,
		publishedLine: regexp.MustCompile(`^Published https://github\.com/` +
			regexp.QuoteMeta(cfg.Repo) + `/releases/tag/(catalog-[A-Za-z0-9._-]+)$`),
	}, nil
}

func validRepo(repo string) bool {
	if !repoPattern.MatchString(repo) {
		return false
	}
	for _, part := range strings.Split(repo, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, "-") || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// preflightJSON mirrors the script's preflight object. Pointers detect
// missing keys; null strings stay nil.
type preflightJSON struct {
	GhAuthenticated       *bool     `json:"gh_authenticated"`
	GoAvailable           *bool     `json:"go_available"`
	PackageDirClean       *bool     `json:"package_dir_clean"`
	PinnedMatchesLocal    *bool     `json:"pinned_matches_local"`
	LocalContentSHA256    *string   `json:"local_content_sha256"`
	PinnedContentSHA256   *string   `json:"pinned_content_sha256"`
	LastReleaseTag        *string   `json:"last_release_tag"`
	PreparedTag           *string   `json:"prepared_tag"`
	PreparedContentSHA256 *string   `json:"prepared_content_sha256"`
	PreparedAssets        *[]string `json:"prepared_assets"`
}

// Preflight runs the read-only preflight and parses its JSON object.
func (s *Script) Preflight(ctx context.Context) (maintenance.Prereq, error) {
	out, err := s.run(ctx, subPreflight)
	if err != nil {
		return maintenance.Prereq{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var raw preflightJSON
	if err := dec.Decode(&raw); err != nil {
		return maintenance.Prereq{}, s.malformed(subPreflight)
	}
	if dec.More() {
		return maintenance.Prereq{}, s.malformed(subPreflight)
	}
	if raw.GhAuthenticated == nil || raw.GoAvailable == nil || raw.PackageDirClean == nil ||
		raw.PinnedMatchesLocal == nil || raw.PreparedAssets == nil {
		return maintenance.Prereq{}, s.malformed(subPreflight)
	}
	p := maintenance.Prereq{
		GhAuthenticated:    *raw.GhAuthenticated,
		GoAvailable:        *raw.GoAvailable,
		PackageDirClean:    *raw.PackageDirClean,
		PinnedMatchesLocal: *raw.PinnedMatchesLocal,
	}
	fields := []struct {
		src     *string
		dst     *string
		pattern *regexp.Regexp
	}{
		{raw.LocalContentSHA256, &p.LocalContentSHA256, shaPattern},
		{raw.PinnedContentSHA256, &p.PinnedContentSHA256, shaPattern},
		{raw.LastReleaseTag, &p.LastReleaseTag, tagPattern},
		{raw.PreparedTag, &p.PreparedTag, tagPattern},
		{raw.PreparedContentSHA256, &p.PreparedContentSHA256, shaPattern},
	}
	for _, f := range fields {
		if f.src == nil {
			continue
		}
		if !f.pattern.MatchString(*f.src) {
			return maintenance.Prereq{}, s.malformed(subPreflight)
		}
		*f.dst = *f.src
	}
	for _, name := range *raw.PreparedAssets {
		if !assetPattern.MatchString(name) {
			return maintenance.Prereq{}, s.malformed(subPreflight)
		}
		p.PreparedAssets = append(p.PreparedAssets, name)
	}
	return p, nil
}

// Prepare runs prepare, then reads the prepared package back through
// preflight (which verifies its manifest and asset checksums).
func (s *Script) Prepare(ctx context.Context) (maintenance.PackageInfo, error) {
	out, err := s.run(ctx, subPrepare)
	if err != nil {
		return maintenance.PackageInfo{}, err
	}
	m := preparedLine.FindStringSubmatch(singleLine(out))
	if m == nil {
		return maintenance.PackageInfo{}, s.malformed(subPrepare)
	}
	p, err := s.Preflight(ctx)
	if err != nil {
		return maintenance.PackageInfo{}, err
	}
	if p.PreparedTag != m[1] || p.PreparedContentSHA256 == "" {
		return maintenance.PackageInfo{}, s.malformed(subPrepare)
	}
	return maintenance.PackageInfo{Tag: p.PreparedTag, ContentSHA256: p.PreparedContentSHA256, Assets: p.PreparedAssets}, nil
}

// Publish refuses before running anything when the GitHub CLI is not
// authenticated; otherwise it runs publish and parses its final line.
func (s *Script) Publish(ctx context.Context) (maintenance.PublishInfo, error) {
	p, err := s.Preflight(ctx)
	if err != nil {
		return maintenance.PublishInfo{}, err
	}
	if !p.GhAuthenticated {
		return maintenance.PublishInfo{}, maintenance.ErrGhNotAuthenticated
	}
	out, err := s.run(ctx, subPublish)
	if err != nil {
		return maintenance.PublishInfo{}, err
	}
	line := singleLine(out)
	if line == noChangesLine {
		return maintenance.PublishInfo{Tag: p.PreparedTag, NoChanges: true}, nil
	}
	if m := s.publishedLine.FindStringSubmatch(line); m != nil {
		return maintenance.PublishInfo{Tag: m[1], Published: true}, nil
	}
	return maintenance.PublishInfo{}, s.malformed(subPublish)
}

// singleLine returns the output when it is exactly one line.
func singleLine(out []byte) string {
	s := strings.TrimSuffix(string(out), "\n")
	if strings.ContainsAny(s, "\r\n") {
		return ""
	}
	return s
}

func (s *Script) malformed(sub subcommand) error {
	s.log(sub, "unexpected output", "")
	return fmt.Errorf("release %s: unexpected script output: %w", sub, maintenance.ErrReleasePrerequisite)
}

// run executes one subcommand. The returned error never contains output.
func (s *Script) run(ctx context.Context, sub subcommand) ([]byte, error) {
	timeout := defaultTimeouts[sub]
	if timeout == 0 {
		return nil, fmt.Errorf("release: unknown subcommand: %w", maintenance.ErrReleasePrerequisite)
	}
	if s.timeout > 0 {
		timeout = s.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", s.script, string(sub), "--repo", s.repo)
	cmd.Dir = s.root
	cmd.Env = s.env()
	cmd.Stdin = nil
	stdout := &limitedBuffer{max: maxStdout}
	stderr := &limitedBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second
	killProcessGroup(cmd)

	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		s.log(sub, "interrupted", "")
		return nil, fmt.Errorf("release %s: %w", sub, ctxErr)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			s.log(sub, "failed", stderr.String())
			return nil, fmt.Errorf("release %s: exit status %d: %w", sub, exitErr.ExitCode(), maintenance.ErrReleasePrerequisite)
		}
		// Not started (python3 missing, bad working directory): the
		// error names no output, only the command.
		s.log(sub, "could not start", "")
		return nil, fmt.Errorf("release %s: could not run python3: %w", sub, maintenance.ErrReleasePrerequisite)
	}
	if stdout.truncated {
		return nil, s.malformed(sub)
	}
	return stdout.Bytes(), nil
}

// env is PATH, HOME and LANG plus GH_TOKEN/GITHUB_TOKEN only when already set
// (non-empty) in this process. Nothing else is inherited.
func (s *Script) env() []string {
	var env []string
	for _, k := range allowedEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, k := range tokenEnvKeys {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func (s *Script) log(sub subcommand, msg, stderr string) {
	if s.logger == nil {
		return
	}
	attrs := []any{"subcommand", string(sub)}
	if stderr != "" {
		attrs = append(attrs, "stderr", s.sanitize(stderr))
	}
	s.logger.Warn("release script "+msg, attrs...)
}

// sanitize redacts credentials and local paths, replaces control
// characters and truncates.
func (s *Script) sanitize(text string) string {
	for _, k := range tokenEnvKeys {
		if v := os.Getenv(k); v != "" {
			text = strings.ReplaceAll(text, v, "[redacted]")
		}
	}
	text = tokenPatterns.ReplaceAllString(text, "[redacted]")
	text = strings.ReplaceAll(text, s.root, "<repo>")
	if home := os.Getenv("HOME"); home != "" && home != "/" {
		text = strings.ReplaceAll(text, home, "~")
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > maxLoggedLen {
		text = string(r[:maxLoggedLen]) + "…"
	}
	return text
}

// limitedBuffer keeps the first max bytes and drops the rest.
type limitedBuffer struct {
	bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		b.truncated = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
