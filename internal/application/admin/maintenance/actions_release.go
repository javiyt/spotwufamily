package maintenance

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// Fixed messages of the release actions.
const (
	ghNotAuthenticatedReason = "GitHub CLI is not authenticated. Run `gh auth login` in a terminal."
	releaseScriptReason      = "python3 or the release script is unavailable"
	goMissingReason          = "Go is not installed or not on PATH"
	nothingPreparedWarning   = "Nothing is prepared for the current catalog content; run release-prepare first."
	publishTypedPhrase       = "publish release"
	// availabilityTTL bounds how long Available reuses a preflight; previews,
	// specs and the in-lock re-check always run a fresh one.
	availabilityTTL = 30 * time.Second
)

// ReleaseDeps are the ports of release-prepare and release-publish.
type ReleaseDeps struct {
	Releaser Releaser
	// Prepared remembers the content hash of the latest successful
	// release-prepare (backed by the job journal).
	Prepared PreparedStore
	// Locker is the cross-process database lock, held while preparing and
	// publishing so the local content cannot change underneath.
	Locker Locker
	// Display names only, never absolute paths.
	Repo         string
	PackageName  string
	ManifestName string
	// Now defaults to time.Now; it times the availability cache.
	Now func() time.Time
}

type releaseAction struct {
	deps    ReleaseDeps
	publish bool

	mu       sync.Mutex
	cachedAt time.Time
	cached   Prereq
	cacheErr error
}

// NewReleasePrepare returns release-prepare: under the database lock it
// builds the release package (complete backup, snapshot, manifest) in the
// package directory. It uploads nothing and needs Go, a clean package
// directory and a local database; the GitHub CLI is not needed.
func NewReleasePrepare(d ReleaseDeps) Action { return &releaseAction{deps: d} }

// NewReleasePublish returns release-publish: it uploads the prepared package
// as a GitHub release and then updates the pinned manifest. It needs an
// authenticated GitHub CLI, the typed phrase "publish release" and a
// successful release-prepare of the current local content. No other action
// ever publishes.
func NewReleasePublish(d ReleaseDeps) Action { return &releaseAction{deps: d, publish: true} }

func (a *releaseAction) Name() string {
	if a.publish {
		return "release-publish"
	}
	return "release-prepare"
}

func (a *releaseAction) Describe() Description {
	if a.publish {
		return Description{Title: "Publish catalog release to GitHub", UsesNetwork: true, Destructive: true}
	}
	return Description{Title: "Prepare catalog release package"}
}

func (a *releaseAction) Validate(p Params) error { return RejectUnknown(p) }

func (a *releaseAction) now() time.Time {
	if a.deps.Now != nil {
		return a.deps.Now()
	}
	return time.Now()
}

// Available uses a preflight at most availabilityTTL old.
func (a *releaseAction) Available() (bool, string) {
	a.mu.Lock()
	if a.cachedAt.IsZero() || a.now().Sub(a.cachedAt) >= availabilityTTL {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		a.cached, a.cacheErr = a.deps.Releaser.Preflight(ctx)
		cancel()
		a.cachedAt = a.now()
	}
	p, err := a.cached, a.cacheErr
	a.mu.Unlock()
	if err != nil {
		return false, releaseScriptReason
	}
	if a.publish {
		if !p.GhAuthenticated {
			return false, ghNotAuthenticatedReason
		}
		return true, ""
	}
	if err := a.prepareBlocked(p); err != nil {
		switch err {
		case ErrNoDatabase:
			return false, noDatabaseReason
		case ErrReleasePackageNotClean:
			return false, "The release package directory " + a.deps.PackageName + " is not empty; publish or remove it first"
		default:
			return false, goMissingReason
		}
	}
	return true, ""
}

// prepareBlocked returns why prepare cannot run, or nil.
func (a *releaseAction) prepareBlocked(p Prereq) error {
	switch {
	case p.LocalContentSHA256 == "":
		return ErrNoDatabase
	case !p.GoAvailable:
		return ErrReleasePrerequisite
	case !p.PackageDirClean:
		return ErrReleasePackageNotClean
	}
	return nil
}

// publishBlocked returns ErrNothingPrepared unless the latest successful
// release-prepare and the verified package on disk both hold the current
// local content.
func (a *releaseAction) publishBlocked(p Prereq) error {
	prepared, ok := a.deps.Prepared.LastPrepared()
	if !ok || p.LocalContentSHA256 == "" || prepared != p.LocalContentSHA256 ||
		p.PreparedTag == "" || p.PreparedContentSHA256 != p.LocalContentSHA256 {
		return ErrNothingPrepared
	}
	return nil
}

func (a *releaseAction) fingerprint(p Prereq) string {
	if a.publish {
		return fingerprint(a.Name(), p.LocalContentSHA256, p.PinnedContentSHA256, p.PreparedTag)
	}
	return fingerprint(a.Name(), p.LocalContentSHA256, p.PinnedContentSHA256,
		yesNo(p.PackageDirClean), yesNo(p.GoAvailable))
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (a *releaseAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	p, err := a.deps.Releaser.Preflight(ctx)
	if err != nil {
		return Preview{}, err
	}
	pv := Preview{Title: a.Describe().Title, Fingerprint: a.fingerprint(p)}
	if a.publish {
		return a.publishPreview(pv, p)
	}
	ready := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	pv.Scope = []ScopeLine{
		{Label: "Python and release script", Value: "available"},
		{Label: "Go", Value: ready(p.GoAvailable, "available", "missing")},
		{Label: "Package directory " + a.deps.PackageName, Value: ready(p.PackageDirClean, "empty", "not empty")},
		{Label: "GitHub CLI", Value: ready(p.GhAuthenticated, "authenticated", "not authenticated (needed only to publish)")},
		{Label: "Local content sha256", Value: orNone(p.LocalContentSHA256)},
		{Label: "Published content sha256", Value: orNone(p.PinnedContentSHA256)},
		{Label: "Last release", Value: orNone(p.LastReleaseTag)},
		{Label: "Writes", Value: "a release package (complete database backup, snapshot and manifest) in " +
			a.deps.PackageName + "; nothing is uploaded"},
	}
	if p.LocalContentSHA256 == "" {
		pv.Warnings = append(pv.Warnings, noDatabaseReason+".")
	}
	if !p.GoAvailable {
		pv.Warnings = append(pv.Warnings, goMissingReason+".")
	}
	if !p.PackageDirClean {
		pv.Warnings = append(pv.Warnings, "The package directory is not empty; publish or remove the prepared package first.")
	}
	if !p.GhAuthenticated {
		pv.Warnings = append(pv.Warnings, "Publishing will need the GitHub CLI: run `gh auth login` in a terminal.")
	}
	if p.PinnedMatchesLocal {
		pv.Warnings = append(pv.Warnings, "Local content matches the published catalog; publishing would change nothing.")
	}
	return pv, nil
}

func (a *releaseAction) publishPreview(pv Preview, p Prereq) (Preview, error) {
	if !p.GhAuthenticated {
		return Preview{}, ErrGhNotAuthenticated
	}
	tag := orNone(p.PreparedTag)
	pv.Destructive, pv.UsesNetwork, pv.RequiresTyped = true, true, publishTypedPhrase
	pv.Scope = []ScopeLine{
		{Label: "Release tag", Value: tag},
		{Label: "Repository", Value: a.deps.Repo},
		{Label: "Prepared content sha256", Value: orNone(p.PreparedContentSHA256)},
		{Label: "Local content sha256", Value: orNone(p.LocalContentSHA256)},
		{Label: "Published content sha256", Value: orNone(p.PinnedContentSHA256)},
		{Label: "Last release", Value: orNone(p.LastReleaseTag)},
		{Label: "Writes", Value: "GitHub release " + tag + " in " + a.deps.Repo +
			" with the prepared assets, then " + a.deps.ManifestName + " is updated"},
	}
	if a.publishBlocked(p) != nil {
		pv.Warnings = append(pv.Warnings, nothingPreparedWarning)
	}
	if p.PinnedMatchesLocal {
		pv.Warnings = append(pv.Warnings, "Local content matches the published catalog; nothing will be uploaded.")
	}
	pv.Warnings = append(pv.Warnings, "A published release cannot be removed from here.")
	return pv, nil
}

// check verifies p against the confirmed fingerprint and the action's
// requirements.
func (a *releaseAction) check(p Prereq, fp string) error {
	if a.publish && !p.GhAuthenticated {
		return ErrGhNotAuthenticated
	}
	if a.fingerprint(p) != fp {
		return ErrStalePreview
	}
	if a.publish {
		return a.publishBlocked(p)
	}
	return a.prepareBlocked(p)
}

// Spec binds the job to the verified preview fingerprint (failing closed
// without one), refuses a state that already differs and re-checks inside the
// job once the database lock is held.
func (a *releaseAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	fp, err := verifiedFingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	p, err := a.deps.Releaser.Preflight(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	if err := a.check(p, fp); err != nil {
		return jobs.Spec{}, err
	}
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: []string{ResourceRelease, ResourceDatabase},
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, fp)
		},
	}, nil
}

func (a *releaseAction) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Safe(err)
}

func (a *releaseAction) run(ctx context.Context, r jobs.Reporter, fp string) (jobs.Outcome, error) {
	r.Step("locking database")
	release, err := a.deps.Locker.TryLock()
	if err != nil {
		return jobs.Outcome{}, Safe(err)
	}
	defer release()

	r.Step("checking prerequisites")
	p, err := a.deps.Releaser.Preflight(ctx)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	if err := a.check(p, fp); err != nil {
		return jobs.Outcome{}, Safe(err)
	}
	// Last point where a cancel leaves everything untouched.
	if err := ctx.Err(); err != nil {
		return jobs.Outcome{}, err
	}
	if a.publish {
		r.Step("publishing")
		info, err := a.deps.Releaser.Publish(ctx)
		if err != nil {
			return jobs.Outcome{}, a.fail(ctx, err)
		}
		var summary string
		switch {
		case info.NoChanges:
			summary = "No catalog content changes; release " + info.Tag + " was not published."
		case info.Published:
			summary = "Published catalog release " + info.Tag + "."
		default:
			return jobs.Outcome{}, Safe(ErrReleasePrerequisite)
		}
		return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary}}, nil
	}
	r.Step("preparing package")
	info, err := a.deps.Releaser.Prepare(ctx)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	summary := "Prepared catalog release " + info.Tag + " in " + a.deps.PackageName +
		". Nothing was uploaded; run release-publish to publish it."
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{
		Summary: summary,
		Report: &jobs.Report{
			Name:    a.Name(),
			Columns: []string{"tag", "content_sha256", "assets"},
			Rows:    [][]string{{info.Tag, info.ContentSHA256, strings.Join(info.Assets, " ")}},
		},
	}}, nil
}
