package bootstrap

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/release"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// ReleaseActionsConfig locates what the release actions work on.
type ReleaseActionsConfig struct {
	// RepoRoot is the absolute repository root (script location and
	// working directory of the release script).
	RepoRoot string
	// Repo is the GitHub repository (owner/name) releases are published to.
	Repo string
	// DBPath is the database whose lock the release jobs hold.
	DBPath string
	// JournalPath is the job journal the jobs.Manager persists to; the
	// latest successful release-prepare recorded there gates publishing.
	JournalPath string
	// Logger receives sanitized, truncated script stderr on failures.
	Logger *slog.Logger
	// Releaser replaces the release script (tests). Nil runs the script.
	Releaser maintenance.Releaser
}

// releasePackageName and releaseManifestName are the script's defaults,
// shown to users instead of paths.
const (
	releasePackageName  = "build/catalog-release"
	releaseManifestName = "data/catalog.release.json"
)

// NewReleaseScript builds the release script adapter for the repository.
func NewReleaseScript(repoRoot, repo string, logger *slog.Logger) (maintenance.Releaser, error) {
	return release.New(release.Config{RepoRoot: repoRoot, Repo: repo, Logger: logger})
}

// NewReleaseActions wires release-prepare and release-publish to the release
// script, the job journal and the database lock. These are the only actions
// that reach the Releaser, and only release-publish publishes.
func NewReleaseActions(cfg ReleaseActionsConfig) ([]maintenance.Action, error) {
	releaser := cfg.Releaser
	if releaser == nil {
		script, err := NewReleaseScript(cfg.RepoRoot, cfg.Repo, cfg.Logger)
		if err != nil {
			return nil, err
		}
		releaser = script
	} else if !release.ValidRepo(cfg.Repo) {
		return nil, release.ErrInvalidRepo
	}
	deps := maintenance.ReleaseDeps{
		Releaser:     releaser,
		Prepared:     journalPrepared{path: cfg.JournalPath},
		Locker:       databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		Repo:         cfg.Repo,
		PackageName:  releasePackageName,
		ManifestName: releaseManifestName,
		Logger:       cfg.Logger,
	}
	return []maintenance.Action{maintenance.NewReleasePrepare(deps), maintenance.NewReleasePublish(deps)}, nil
}

var contentSHAPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// journalPrepared reads the job journal: the latest succeeded release-prepare
// (by finish time) and the content hash in its report. A latest success
// without a valid report counts as nothing prepared.
type journalPrepared struct{ path string }

var _ maintenance.PreparedStore = journalPrepared{}

func (s journalPrepared) LastPrepared() (string, bool) {
	all, err := filesystem.NewJobJournal(s.path).Load(context.Background())
	if err != nil {
		return "", false
	}
	var (
		latest jobs.Job
		found  bool
	)
	for _, j := range all {
		if j.Kind != "release-prepare" || j.Status != jobs.StatusSucceeded {
			continue
		}
		if !found || j.FinishedAt.After(latest.FinishedAt) || j.FinishedAt.Equal(latest.FinishedAt) {
			latest, found = j, true
		}
	}
	if !found || latest.Result == nil || latest.Result.Report == nil {
		return "", false
	}
	rep := latest.Result.Report
	if len(rep.Columns) < 2 || rep.Columns[1] != "content_sha256" || len(rep.Rows) != 1 || len(rep.Rows[0]) < 2 {
		return "", false
	}
	sha := rep.Rows[0][1]
	if !contentSHAPattern.MatchString(sha) {
		return "", false
	}
	return sha, true
}

// releaseHashes runs one preflight for a freshness session.
func releaseHashes(r maintenance.Releaser) (maintenance.Prereq, bool) {
	if r == nil {
		return maintenance.Prereq{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := r.Preflight(ctx)
	return p, err == nil
}
