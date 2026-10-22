package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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
	// CatalogPath is the editorial catalog a successful prepare or publish is
	// checkpointed against. Empty records no provenance.
	CatalogPath string
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

// NewReleaseActions wires release-prepare, release-publish and release-pr to
// the release script, the job journal and the database lock. These are the
// only actions that reach the Releaser, and only release-publish publishes;
// release-pr only reads its preflight, then pushes a branch and opens a pull
// request.
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
	if cfg.CatalogPath != "" {
		deps.Provenance = newProvenance(cfg.DBPath, cfg.CatalogPath)
	}
	actions := []maintenance.Action{maintenance.NewReleasePrepare(deps), maintenance.NewReleasePublish(deps)}
	if releasePRApplies(cfg) {
		port := NewReleasePRPort(ReleasePRConfig{RepoRoot: cfg.RepoRoot, Repo: cfg.Repo, Releaser: releaser, Logger: cfg.Logger})
		actions = append(actions, maintenance.NewReleasePR(maintenance.PRDeps{Port: port, Changes: port.Changes,
			Provenance: deps.Provenance}))
	}
	return actions, nil
}

// releasePRApplies reports whether release-pr commits the catalog the admin
// edits: it always commits the repository's own data/artists.yaml, so an
// admin configured with another catalog file does not get it.
func releasePRApplies(cfg ReleaseActionsConfig) bool {
	if cfg.CatalogPath == "" {
		return true
	}
	want := filepath.Join(cfg.RepoRoot, filepath.FromSlash(prCatalogFile))
	return filepath.Clean(cfg.CatalogPath) == filepath.Clean(want)
}

var contentSHAPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// journalPrepared reads the job journal: the latest succeeded release-prepare
// (by finish time) and the content hash in its report. A latest success
// without a valid report counts as nothing prepared.
type journalPrepared struct{ path string }

var _ maintenance.PreparedStore = journalPrepared{}

var _ maintenance.PreparedRecorder = journalPrepared{}

// preparedRecord is the sidecar of the job journal naming the latest
// successful prepare, whichever job ran it.
type preparedRecord struct {
	ContentSHA256 string    `json:"content_sha256"`
	At            time.Time `json:"at"`
}

func (s journalPrepared) recordPath() string { return s.path + ".prepared.json" }

// RecordPrepared writes the sidecar atomically.
func (s journalPrepared) RecordPrepared(sha string) error {
	if !contentSHAPattern.MatchString(sha) {
		return errors.New("invalid prepared content hash")
	}
	data, err := json.Marshal(preparedRecord{ContentSHA256: sha, At: time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.recordPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.recordPath())
}

func (s journalPrepared) recorded() (preparedRecord, bool) {
	data, err := os.ReadFile(s.recordPath())
	if err != nil {
		return preparedRecord{}, false
	}
	var rec preparedRecord
	if json.Unmarshal(data, &rec) != nil || !contentSHAPattern.MatchString(rec.ContentSHA256) {
		return preparedRecord{}, false
	}
	return rec, true
}

// LastPrepared is the content hash of the latest successful prepare: the
// newer of the journal's last release-prepare job and the sidecar.
func (s journalPrepared) LastPrepared() (string, bool) {
	sha, at, ok := s.journalPrepared()
	if rec, has := s.recorded(); has && (!ok || !rec.At.Before(at)) {
		return rec.ContentSHA256, true
	}
	return sha, ok
}

func (s journalPrepared) journalPrepared() (string, time.Time, bool) {
	all, err := filesystem.NewJobJournal(s.path).Load(context.Background())
	if err != nil {
		return "", time.Time{}, false
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
		return "", time.Time{}, false
	}
	rep := latest.Result.Report
	if len(rep.Columns) < 2 || rep.Columns[1] != "content_sha256" || len(rep.Rows) != 1 || len(rep.Rows[0]) < 2 {
		return "", time.Time{}, false
	}
	sha := rep.Rows[0][1]
	if !contentSHAPattern.MatchString(sha) {
		return "", time.Time{}, false
	}
	return sha, latest.FinishedAt, true
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
