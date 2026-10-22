package maintenance

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// ReleasePRFiles are the only paths release-pr ever commits, relative to the
// repository root: the catalog, its review log and the release manifest.
var ReleasePRFiles = []string{"data/artists.yaml", "data/artists.yaml.reviews.jsonl", "data/catalog.release.json"}

// releasePRKeyFiles are the files of which at least one must differ from
// the base branch for a pull request to be worth opening.
var releasePRKeyFiles = []string{"data/artists.yaml", "data/catalog.release.json"}

// Sentinel errors of release-pr. PRPort implementations wrap them (never
// with command output, paths or account details); the action maps them to
// fixed messages.
var (
	// ErrPROriginNewer: origin changed one of the files since the checkout
	// the operator edited from, and the local copy differs from origin's.
	ErrPROriginNewer = errors.New("origin has newer changes to catalog data")
	// ErrPRBranchDiverged: the branch exists on origin with other content.
	ErrPRBranchDiverged = errors.New("pull request branch exists with different content")
	// ErrPRNothingToOpen: the files already match the base branch.
	ErrPRNothingToOpen = errors.New("nothing to open")
	// ErrPRNotOpened: the branch was pushed but the pull request was not
	// opened (gh failed or the run was cancelled). Re-running opens it.
	ErrPRNotOpened = errors.New("branch pushed, pull request not opened")
	// ErrPROriginMismatch: origin does not point at the configured
	// repository the pull request would be opened in.
	ErrPROriginMismatch = errors.New("origin does not point at the configured repository")
	// ErrGitIdentity: git user.name or user.email is not configured.
	ErrGitIdentity = errors.New("git identity not configured")
	// ErrPRGit: a git or gh command failed.
	ErrPRGit = errors.New("git or gh command failed")
	// ErrPRInvalidRequest: the request names other files or a bad branch.
	ErrPRInvalidRequest = errors.New("invalid pull request request")
)

// Fixed messages of release-pr.
const (
	prTypedPhrase       = "open pull request"
	prOriginReason      = "The git remote origin or its base branch cannot be resolved in this checkout"
	prOriginRepoReason  = "The git remote origin does not point at the configured repository"
	prManifestReason    = "The release manifest is not pinned to the local database content; publish the release first"
	prNothingReason     = "Nothing to open: the catalog and release manifest already match origin"
	prCheckReason       = "The pull request prerequisites could not be checked"
	prCheckingReason    = "Checking pull request prerequisites; reload the page in a moment"
	prGenericTitle      = "Update catalog data"
	prMaxBodyChanges    = 100
	prTitleSlugs        = 3
	prMaxTitleLen       = 200
	prMaxBodyLen        = 60000
	prBranchPrefix      = "catalog-update-"
	prOriginNewerMsg    = "origin has newer changes to catalog data; update your checkout first"
	prBranchDivergedMsg = "The pull request branch already exists on origin with different content; it is never force-pushed. Delete the branch on GitHub or update the catalog, then retry."
	prIdentityMsg       = "Git user.name and user.email are not configured; set them with git config and retry."
	prGitMsg            = "A git or GitHub CLI command failed; nothing was force-pushed. Check the checkout and retry."
	prInvalidMsg        = "The pull request request is not valid."
)

var (
	prBranchPattern = regexp.MustCompile(`^catalog-update-[0-9a-f]{12}-[0-9a-f]{12}$`)
	prHexPattern    = regexp.MustCompile(`^[0-9a-f]{12,}$`)
	prURLPattern    = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+$`)
	prTagPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	prRepoPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	prBasePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
)

// PRPrereq is what release-pr needs to know before opening a pull request.
type PRPrereq struct {
	GhAuthenticated bool
	// OriginResolved: the checkout has an origin remote and a fetched base
	// branch.
	OriginResolved bool
	// OriginMatchesRepo: origin's configured URL names Repo, so the push
	// and the pull request target the same repository.
	OriginMatchesRepo bool
	// Repo is the owner/repo display string, BaseBranch the branch name.
	Repo       string
	BaseBranch string
	// ManifestMatchesLocal: the pinned release manifest has the content
	// hash of the local database.
	ManifestMatchesLocal  bool
	ManifestContentSHA256 string
	ReleaseTag            string
	// CatalogDigest is the fingerprint digest of the catalog now.
	CatalogDigest string
	// FilesPresent are the ReleasePRFiles that exist locally; FilesChanged
	// those whose local content differs from the base branch.
	FilesPresent []string
	FilesChanged []string
}

// PRRequest is one pull request to open. Files must be a subset of
// ReleasePRFiles.
type PRRequest struct {
	Branch, Title, Body string
	Files               []string
}

// PRResult is the opened (or already open) pull request.
type PRResult struct {
	URL    string
	Reused bool
}

// PRPort builds the branch, commit and pull request. Check is read-only and
// local; Open fetches, commits in a temporary worktree, pushes (never
// forced) and opens the pull request unless one is already open. Neither
// touches the operator's working tree, index or current branch.
type PRPort interface {
	Check(ctx context.Context) (PRPrereq, error)
	Open(ctx context.Context, req PRRequest) (PRResult, error)
}

// PRDeps are the ports of release-pr.
type PRDeps struct {
	Port PRPort
	// Changes lists the artist changes the pull request carries; it only
	// shapes the title and body, and an error or nil gives a generic text.
	Changes func(ctx context.Context) ([]artists.ArtistChange, error)
	// Now defaults to time.Now; it times the availability cache.
	Now func() time.Time
}

// ValidatePRRequest checks req names only ReleasePRFiles (each once, at
// least the catalog or the manifest), a release-pr branch and a one-line
// title.
func ValidatePRRequest(req PRRequest) error {
	if !prBranchPattern.MatchString(req.Branch) {
		return ErrPRInvalidRequest
	}
	if req.Title == "" || len(req.Title) > prMaxTitleLen || strings.ContainsAny(req.Title, "\r\n") {
		return ErrPRInvalidRequest
	}
	if req.Body == "" || len(req.Body) > prMaxBodyLen {
		return ErrPRInvalidRequest
	}
	if len(req.Files) == 0 {
		return ErrPRInvalidRequest
	}
	seen := map[string]bool{}
	key := false
	for _, f := range req.Files {
		if !slices.Contains(ReleasePRFiles, f) || seen[f] {
			return ErrPRInvalidRequest
		}
		seen[f] = true
		key = key || slices.Contains(releasePRKeyFiles, f)
	}
	if !key {
		return ErrPRInvalidRequest
	}
	return nil
}

// PRBranch is the deterministic branch for a catalog digest and a release
// manifest content hash; ok=false when either is not a usable hash.
func PRBranch(catalogDigest, manifestSHA string) (string, bool) {
	if !prHexPattern.MatchString(catalogDigest) || !prHexPattern.MatchString(manifestSHA) {
		return "", false
	}
	return prBranchPrefix + catalogDigest[:12] + "-" + manifestSHA[:12], true
}

type releasePRAction struct {
	deps PRDeps

	flight     sync.Mutex
	mu         sync.Mutex
	cachedAt   time.Time
	cached     PRPrereq
	cacheErr   error
	refreshing bool
}

// NewReleasePR returns release-pr: after release-publish pinned the manifest
// to the local database, it commits exactly ReleasePRFiles to a
// deterministic branch built on a fresh origin base branch, pushes it (never
// forced) and opens a pull request with gh, reusing an open one. It needs
// the typed phrase "open pull request". It never merges.
func NewReleasePR(d PRDeps) Action { return &releasePRAction{deps: d} }

func (a *releasePRAction) Name() string { return "release-pr" }

func (a *releasePRAction) Describe() Description {
	return Description{Title: "Open catalog pull request", UsesNetwork: true, Destructive: true}
}

func (a *releasePRAction) Validate(p Params) error { return RejectUnknown(p) }

func (a *releasePRAction) now() time.Time {
	if a.deps.Now != nil {
		return a.deps.Now()
	}
	return time.Now()
}

// Available reuses a check at most availabilityTTL old, running one when
// needed.
func (a *releasePRAction) Available() (bool, string) {
	a.refresh()
	a.mu.Lock()
	p, err := a.cached, a.cacheErr
	a.mu.Unlock()
	return prAvailability(p, err)
}

// AvailableNow never checks on the caller's goroutine: it starts one
// background check when the cached one is missing or expired.
func (a *releasePRAction) AvailableNow() (bool, string) {
	a.mu.Lock()
	expired := a.cachedAt.IsZero() || a.now().Sub(a.cachedAt) >= availabilityTTL
	if expired && !a.refreshing {
		a.refreshing = true
		go func() {
			defer func() {
				if recover() != nil {
					a.store(PRPrereq{}, errReleaseCheckPanicked)
				}
				a.mu.Lock()
				a.refreshing = false
				a.mu.Unlock()
			}()
			a.refresh()
		}()
	}
	p, err, ok := a.cached, a.cacheErr, !a.cachedAt.IsZero()
	a.mu.Unlock()
	if !ok {
		return false, prCheckingReason
	}
	return prAvailability(p, err)
}

func (a *releasePRAction) refresh() {
	a.flight.Lock()
	defer a.flight.Unlock()
	a.mu.Lock()
	fresh := !a.cachedAt.IsZero() && a.now().Sub(a.cachedAt) < availabilityTTL
	a.mu.Unlock()
	if fresh {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), availabilityTimeout)
	defer cancel()
	p, err := a.check(ctx)
	a.store(p, err)
}

// check runs Port.Check, recovering a panic as a failed check.
func (a *releasePRAction) check(ctx context.Context) (p PRPrereq, err error) {
	defer func() {
		if recover() != nil {
			p, err = PRPrereq{}, errReleaseCheckPanicked
		}
	}()
	return a.deps.Port.Check(ctx)
}

func (a *releasePRAction) store(p PRPrereq, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cached, a.cacheErr, a.cachedAt = p, err, time.Now()
	if a.deps.Now != nil {
		a.cachedAt = a.deps.Now()
	}
}

func prAvailability(p PRPrereq, err error) (bool, string) {
	if err != nil {
		return false, prCheckReason
	}
	if reason := prBlockedReason(p); reason != "" {
		return false, reason
	}
	return true, ""
}

// prBlockedReason is why no pull request can be opened with p, or "".
func prBlockedReason(p PRPrereq) string {
	switch {
	case !p.GhAuthenticated:
		return ghNotAuthenticatedReason
	case !p.OriginResolved || !prRepoPattern.MatchString(p.Repo) || !validPRBase(p.BaseBranch):
		return prOriginReason
	case !p.OriginMatchesRepo:
		return prOriginRepoReason
	case !p.ManifestMatchesLocal:
		return prManifestReason
	}
	if _, ok := PRBranch(p.CatalogDigest, p.ManifestContentSHA256); !ok {
		return prManifestReason
	}
	for _, f := range releasePRKeyFiles {
		if slices.Contains(p.FilesChanged, f) {
			return ""
		}
	}
	return prNothingReason
}

// request builds the pull request for p.
func (a *releasePRAction) request(ctx context.Context, p PRPrereq) PRRequest {
	branch, _ := PRBranch(p.CatalogDigest, p.ManifestContentSHA256)
	var files []string
	for _, f := range ReleasePRFiles {
		if slices.Contains(p.FilesPresent, f) {
			files = append(files, f)
		}
	}
	var changes []artists.ArtistChange
	if a.deps.Changes != nil {
		if c, err := a.deps.Changes(ctx); err == nil {
			changes = c
		}
	}
	return PRRequest{Branch: branch, Title: prTitle(changes), Body: prBody(changes, p.ReleaseTag), Files: files}
}

// prChangeLines are the valid slugs with their change kinds, sorted by slug.
func prChangeLines(changes []artists.ArtistChange) (slugs, lines []string) {
	sorted := slices.Clone(changes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	for _, c := range sorted {
		if !validSlug(c.Slug) {
			continue
		}
		kinds := make([]string, 0, len(c.Kinds))
		for _, k := range c.Kinds {
			if validSlug(strings.ReplaceAll(string(k), "_", "-")) {
				kinds = append(kinds, string(k))
			}
		}
		slugs = append(slugs, c.Slug)
		lines = append(lines, "- "+c.Slug+": "+orNone(strings.Join(kinds, ", ")))
	}
	return slugs, lines
}

func prTitle(changes []artists.ArtistChange) string {
	slugs, _ := prChangeLines(changes)
	if len(slugs) == 0 {
		return prGenericTitle
	}
	return "Update catalog: " + joinLimited(slugs, prTitleSlugs)
}

// prBody names the release tag, the changed artists and the deploy; never a
// path or an account.
func prBody(changes []artists.ArtistChange, tag string) string {
	if !prTagPattern.MatchString(tag) {
		tag = "unknown"
	}
	_, lines := prChangeLines(changes)
	var b strings.Builder
	b.WriteString("Catalog data update opened from the admin Operations page.\n\n")
	b.WriteString("Catalog release: " + tag + "\n\n")
	b.WriteString("This pull request carries the editorial catalog, its review log and the release manifest pinned to release " + tag + ".\n\n")
	if len(lines) == 0 {
		b.WriteString("Changed artists: none listed (catalog data only).\n\n")
	} else {
		b.WriteString("Changed artists:\n\n")
		limit := min(len(lines), prMaxBodyChanges)
		b.WriteString(strings.Join(lines[:limit], "\n") + "\n")
		if len(lines) > limit {
			b.WriteString("- and " + plural(len(lines)-limit, "more artist") + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Merging this pull request triggers the GitHub Pages deploy of the site with this release. " +
		"Until it is merged, the published release is not pinned by the site.\n")
	return b.String()
}

func (a *releasePRAction) fingerprint(p PRPrereq, req PRRequest) string {
	parts := []string{a.Name(), p.Repo, p.BaseBranch, p.CatalogDigest, p.ManifestContentSHA256, p.ReleaseTag,
		req.Branch, req.Title, req.Body, strings.Join(req.Files, ","), strings.Join(p.FilesChanged, ",")}
	return fingerprint(parts...)
}

func (a *releasePRAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	p, err := a.deps.Port.Check(ctx)
	if err != nil {
		return Preview{}, prFail(ctx, err)
	}
	req := a.request(ctx, p)
	pv := Preview{
		Title:         a.Describe().Title,
		Destructive:   true,
		UsesNetwork:   true,
		RequiresTyped: prTypedPhrase,
		Fingerprint:   a.fingerprint(p, req),
		Scope: []ScopeLine{
			{Label: "Repository", Value: orNone(prSafeValue(p.Repo, prRepoPattern))},
			{Label: "Base branch", Value: orNone(prBaseValue(p.BaseBranch))},
			{Label: "Branch", Value: orNone(req.Branch)},
			{Label: "Files", Value: orNone(strings.Join(req.Files, ", "))},
			{Label: "Title", Value: req.Title},
			{Label: "Release", Value: orNone(prSafeValue(p.ReleaseTag, prTagPattern))},
			{Label: "Writes", Value: "a commit on the branch above, built in a temporary worktree on the fetched base branch, " +
				"pushed to origin (never forced), and a pull request opened with the GitHub CLI; your checkout is not changed"},
		},
	}
	if reason := prBlockedReason(p); reason != "" {
		pv.Warnings = append(pv.Warnings, reason+".")
	}
	pv.Warnings = append(pv.Warnings, "The pull request is not merged from here; merging it deploys the site.")
	return pv, nil
}

// validPRBase accepts a plain branch name without "..".
func validPRBase(b string) bool {
	return prBasePattern.MatchString(b) && !strings.Contains(b, "..")
}

func prSafeValue(v string, re *regexp.Regexp) string {
	if re.MatchString(v) {
		return v
	}
	return ""
}

func prBaseValue(b string) string {
	if validPRBase(b) {
		return b
	}
	return ""
}

// verify refuses p when it cannot open a pull request or no longer matches
// the confirmed fingerprint (a catalog edited since the preview).
func (a *releasePRAction) verify(ctx context.Context, p PRPrereq, fp string) (PRRequest, error) {
	if !p.GhAuthenticated {
		return PRRequest{}, ErrGhNotAuthenticated
	}
	req := a.request(ctx, p)
	if a.fingerprint(p, req) != fp {
		return PRRequest{}, ErrStalePreview
	}
	if reason := prBlockedReason(p); reason != "" {
		return PRRequest{}, &SafeError{Code: "unavailable", Message: reason + "."}
	}
	return req, nil
}

// Spec binds the job to the verified preview fingerprint and re-checks it in
// the job, so a catalog changed after confirming aborts before any commit.
func (a *releasePRAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	fp, err := verifiedFingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	p, err := a.deps.Port.Check(ctx)
	if err != nil {
		return jobs.Spec{}, prFail(ctx, err)
	}
	if _, err := a.verify(ctx, p, fp); err != nil {
		return jobs.Spec{}, err
	}
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: []string{ResourceRelease, ResourceCatalogYAML},
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, fp)
		},
	}, nil
}

func (a *releasePRAction) run(ctx context.Context, r jobs.Reporter, fp string) (jobs.Outcome, error) {
	r.Step("checking prerequisites")
	p, err := a.deps.Port.Check(ctx)
	if err != nil {
		return jobs.Outcome{}, prFail(ctx, err)
	}
	req, err := a.verify(ctx, p, fp)
	if err != nil {
		return jobs.Outcome{}, Safe(err)
	}
	if err := ctx.Err(); err != nil {
		return jobs.Outcome{}, err
	}
	r.Step("opening pull request")
	res, err := a.deps.Port.Open(ctx, req)
	switch {
	case err == nil:
	case errors.Is(err, ErrPRNothingToOpen):
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{
			Summary: "Nothing to open: the catalog and release manifest already match origin " + p.BaseBranch + ".",
		}}, nil
	case errors.Is(err, ErrPRNotOpened):
		msg := "Branch " + req.Branch + " was pushed, but the pull request was not opened. Run release-pr again to open it."
		out := jobs.Outcome{Result: jobs.Result{Summary: msg}}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, &SafeError{Code: "interrupted", Message: msg, cause: err}
	default:
		return jobs.Outcome{}, prFail(ctx, err)
	}
	url := res.URL
	if !prURLPattern.MatchString(url) {
		url = "(link not shown)"
	}
	summary := "Opened pull request " + url + " from " + req.Branch + " into " + p.BaseBranch + "."
	reused := "no"
	if res.Reused {
		summary = "Pull request already open: " + url + " (" + req.Branch + ")."
		reused = "yes"
	}
	summary += " Merging it deploys the site; until then release " + orNone(prSafeValue(p.ReleaseTag, prTagPattern)) +
		" is not pinned by the site."
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{
		Summary: summary,
		Report: &jobs.Report{
			Name:    a.Name(),
			Columns: []string{"branch", "base", "pull_request", "reused"},
			Rows:    [][]string{{req.Branch, p.BaseBranch, url, reused}},
		},
	}}, nil
}

// prFail maps a release-pr error to a fixed message; a cancel stays a cancel.
func prFail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return prSafe(err)
}

func prSafe(err error) error {
	var se *SafeError
	if errors.As(err, &se) {
		return se
	}
	var msg, code string
	switch {
	case errors.Is(err, ErrPROriginNewer):
		code, msg = "conflict", prOriginNewerMsg
	case errors.Is(err, ErrPROriginMismatch):
		code, msg = "conflict", prOriginRepoReason+"; the pull request was not opened."
	case errors.Is(err, ErrPRBranchDiverged):
		code, msg = "conflict", prBranchDivergedMsg
	case errors.Is(err, ErrGitIdentity):
		code, msg = "unavailable", prIdentityMsg
	case errors.Is(err, ErrPRInvalidRequest):
		code, msg = "validation", prInvalidMsg
	case errors.Is(err, ErrPRGit):
		code, msg = "unavailable", prGitMsg
	default:
		return Safe(err)
	}
	return &SafeError{Code: code, Message: msg, cause: err}
}
