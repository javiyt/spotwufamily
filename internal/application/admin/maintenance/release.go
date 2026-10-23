package maintenance

import (
	"context"
	"errors"
)

// ResourceRelease is held by the release jobs: the release package directory,
// the pinned manifest and the GitHub release.
const ResourceRelease = "release"

// Sentinel errors of the release workflow; Safe maps them to fixed messages.
var (
	// ErrGhNotAuthenticated: the GitHub CLI has no usable login.
	ErrGhNotAuthenticated = errors.New("github cli not authenticated")
	// ErrReleasePrerequisite: python3, the release script or Go is missing,
	// or the script failed or returned output that could not be understood.
	ErrReleasePrerequisite = errors.New("release prerequisite missing")
	// ErrReleasePackageNotClean: the release package directory is not empty.
	ErrReleasePackageNotClean = errors.New("release package directory not clean")
	// ErrNothingPrepared: there is no successful release-prepare for the
	// current local content.
	ErrNothingPrepared = errors.New("nothing prepared")
	// ErrPublishInterrupted: the publish subprocess had started when it was
	// stopped (timeout or cancel); a partial GitHub release may exist.
	ErrPublishInterrupted = errors.New("publish interrupted")
)

// Prereq is the release script's preflight report. Empty strings mean the
// value is unknown (the script printed null).
type Prereq struct {
	GhAuthenticated    bool
	GoAvailable        bool
	PackageDirClean    bool
	PinnedMatchesLocal bool
	// LocalContentSHA256 is the content hash of the local database.
	LocalContentSHA256 string
	// PinnedContentSHA256 and LastReleaseTag come from the pinned manifest
	// (data/catalog.release.json).
	PinnedContentSHA256 string
	LastReleaseTag      string
	// Prepared* describe a verified package in the release package
	// directory; empty when there is none.
	PreparedTag           string
	PreparedContentSHA256 string
	PreparedAssets        []string
}

// PackageInfo describes a prepared release package. Assets are file names
// only, never paths.
type PackageInfo struct {
	Tag           string
	ContentSHA256 string
	Assets        []string
}

// PublishInfo is the outcome of a publish. NoChanges means the pinned
// manifest already had the prepared content, so nothing was uploaded and the
// manifest was not copied.
type PublishInfo struct {
	Tag       string
	Published bool
	NoChanges bool
}

// Releaser runs the release script with fixed arguments. Implementations
// never echo the script's output and never contain credentials in errors.
type Releaser interface {
	// Preflight is read-only.
	Preflight(ctx context.Context) (Prereq, error)
	// Prepare builds the release package locally; it uploads nothing.
	Prepare(ctx context.Context) (PackageInfo, error)
	// Publish uploads the prepared package as a GitHub release and then
	// updates the pinned manifest. It refuses with ErrGhNotAuthenticated,
	// before running anything, when the GitHub CLI is not authenticated.
	Publish(ctx context.Context) (PublishInfo, error)
}

// PreparedStore remembers the content hash of the latest successful
// release-prepare.
type PreparedStore interface {
	LastPrepared() (contentSHA string, ok bool)
}

// PreparedRecorder is optionally implemented by a PreparedStore that is told
// of every successful prepare, including one run as a stage of another job
// (update-website), which the job journal does not list as release-prepare.
type PreparedRecorder interface {
	RecordPrepared(contentSHA string) error
}
