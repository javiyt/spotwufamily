package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/release"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// DefaultReleaseRepo is the GitHub repository catalog releases go to unless
// SPOTWUFAMILY_RELEASE_REPO names another one.
const DefaultReleaseRepo = "javiyt/spotwufamily"

// ReleaseRepoEnv overrides DefaultReleaseRepo. The value ReleaseDisabled
// registers no release action (the smoke test uses it to never reach gh).
const ReleaseRepoEnv = "SPOTWUFAMILY_RELEASE_REPO"

// ReleaseDisabled is the ReleaseRepoEnv value that turns the release
// actions off.
const ReleaseDisabled = "none"

// tokenSecretLen is the size of the per-process preview token secret.
const tokenSecretLen = 32

// defaultShutdownWait bounds how long Close waits for cancelled jobs.
const defaultShutdownWait = 5 * time.Second

// AdminConfig configures the full admin composition: the catalog and
// database the admin serves plus the maintenance workflows.
type AdminConfig struct {
	CatalogPath string
	DBPath      string
	// RepoRoot is the repository root: the release script location and the
	// base of build/admin, the generated site exports. Relative values are
	// made absolute; empty means the current directory.
	RepoRoot string
	// Env reads configuration (credentials at action time, the release
	// repository). Nil means os.Getenv.
	Env EnvLookup
	// Logger receives server-side detail never shown to users. Nil discards.
	Logger *slog.Logger
	// ShutdownWait bounds how long Close waits for running jobs to observe
	// cancellation. Zero means 5s.
	ShutdownWait time.Duration

	// Test seams.
	releaser maintenance.Releaser
	options  []MaintenanceOption
	now      func() time.Time
}

// AdminPaths are the files the admin maintenance workflows read and write.
type AdminPaths struct {
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// WorkDir is <repo>/build/admin: job journal and database backups.
	WorkDir      string
	JournalPath  string
	BackupDir    string
	OperationLog string
	LedgerPath   string
	SnapshotPath string
	// Export roots, as the CLI export defaults.
	ExportOutputDir  string
	ExportStaticDir  string
	ExportContentDir string
}

// Paths derives every maintenance path. The ledger lives next to the
// catalog and the snapshot next to the database, as the CLI defaults do
// for data/artists.yaml and data/catalog.db; the work dir and exports are
// under the repository root.
func (c AdminConfig) Paths() (AdminPaths, error) {
	root := c.RepoRoot
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return AdminPaths{}, fmt.Errorf("resolve repository root: %w", err)
	}
	work := filepath.Join(root, "build", "admin")
	return AdminPaths{
		RepoRoot:         root,
		WorkDir:          work,
		JournalPath:      filepath.Join(work, "jobs.jsonl"),
		BackupDir:        filepath.Join(work, "backups"),
		OperationLog:     c.CatalogPath + ".operations.jsonl",
		LedgerPath:       filepath.Join(filepath.Dir(c.CatalogPath), "wu-candidates.yaml"),
		SnapshotPath:     filepath.Join(filepath.Dir(c.DBPath), "catalog.snapshot.sql.gz"),
		ExportOutputDir:  filepath.Join(root, "site", "data", "generated"),
		ExportStaticDir:  filepath.Join(root, "site", "static"),
		ExportContentDir: filepath.Join(root, "site", "content", "generated"),
	}, nil
}

// Aliases give the two embedded services distinct field names.
type (
	catalogService     = admin.Service
	maintenanceService = maintenance.Service
)

// Admin is the admin server's service: the catalog queries and edits of
// admin.Service plus the maintenance workflows (operations, jobs, publish).
type Admin struct {
	catalogService
	*maintenanceService

	paths   AdminPaths
	manager *jobs.Manager
	release bool
}

// holdCatalog takes the in-process catalog-yaml resource for one catalog
// write, so an editor or resolver save never interleaves with a maintenance
// job that rewrites the catalog from an in-memory copy (refresh-metadata,
// discover-import), and such a job cannot start mid-save. It fails with a
// *jobs.ConflictError while a job holds the catalog.
func (a *Admin) holdCatalog() (func(), error) {
	return a.manager.Acquire(maintenance.ResourceCatalogYAML)
}

// ApplyChange is admin.Service.ApplyChange serialised against maintenance
// jobs; a dry run writes nothing and is never blocked.
func (a *Admin) ApplyChange(ctx context.Context, req admin.ChangeRequest) (admin.ChangeResult, error) {
	if req.DryRun {
		return a.catalogService.ApplyChange(ctx, req)
	}
	release, err := a.holdCatalog()
	if err != nil {
		return admin.ChangeResult{}, err
	}
	defer release()
	return a.catalogService.ApplyChange(ctx, req)
}

// ApplyRelationship is admin.Service.ApplyRelationship serialised against
// maintenance jobs; a dry run writes nothing and is never blocked.
func (a *Admin) ApplyRelationship(ctx context.Context, req admin.RelationshipRequest) (admin.RelationshipResult, error) {
	if req.DryRun {
		return a.catalogService.ApplyRelationship(ctx, req)
	}
	release, err := a.holdCatalog()
	if err != nil {
		return admin.RelationshipResult{}, err
	}
	defer release()
	return a.catalogService.ApplyRelationship(ctx, req)
}

// ApplyResolution is admin.Service.ApplyResolution serialised against
// maintenance jobs.
func (a *Admin) ApplyResolution(ctx context.Context, req admin.ResolveRequest) (admin.ResolveResult, error) {
	release, err := a.holdCatalog()
	if err != nil {
		return admin.ResolveResult{}, err
	}
	defer release()
	return a.catalogService.ApplyResolution(ctx, req)
}

// Paths reports where the workflows keep their files.
func (a *Admin) Paths() AdminPaths { return a.paths }

// ReleaseConfigured reports whether the release actions were registered.
func (a *Admin) ReleaseConfigured() bool { return a.release }

// NewAdmin composes the admin service with the maintenance workflows. It
// starts offline: no database, Spotify credentials, python3 or gh is needed
// at startup, the actions that need them report why they are unavailable.
// Jobs left running by a previous process are marked interrupted.
//
// The preview token secret is random per process, so preview tokens die on
// restart. The returned function cancels running jobs, waits a bounded time
// for them and releases every handle.
func NewAdmin(ctx context.Context, cfg AdminConfig) (*Admin, func(), error) {
	if cfg.Env == nil {
		cfg.Env = os.Getenv
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.ShutdownWait <= 0 {
		cfg.ShutdownWait = defaultShutdownWait
	}
	paths, err := cfg.Paths()
	if err != nil {
		return nil, nil, err
	}
	secret := make([]byte, tokenSecretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, fmt.Errorf("generate preview token secret: %w", err)
	}

	logger := cfg.Logger
	var svcRef atomic.Pointer[maintenance.Service]
	manager := jobs.NewManager(filesystem.NewJobJournal(paths.JournalPath), cfg.now, newJobID,
		jobs.WithSanitizer(jobErrorSanitizer(logger, cfg.Env)),
		jobs.WithOnFinish(func(j jobs.Job) {
			if s := svcRef.Load(); s != nil {
				s.FinishHook()(j)
			}
		}),
	)
	interrupted, err := manager.Recover(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("recover admin jobs: %w", err)
	}

	actionsCfg := MaintenanceActionsConfig{
		CatalogPath:      cfg.CatalogPath,
		LedgerPath:       paths.LedgerPath,
		DBPath:           cfg.DBPath,
		SnapshotPath:     paths.SnapshotPath,
		BackupDir:        paths.BackupDir,
		ExportOutputDir:  paths.ExportOutputDir,
		ExportStaticDir:  paths.ExportStaticDir,
		ExportContentDir: paths.ExportContentDir,
		Env:              cfg.Env,
		Options:          cfg.options,
	}
	var actions []maintenance.Action
	actions = append(actions, NewDiscoveryAuditRefreshActions(actionsCfg)...)
	actions = append(actions, NewSyncActions(actionsCfg)...)
	actions = append(actions, NewPublishPrepActions(actionsCfg)...)
	actions = append(actions, NewReconcileActions(actionsCfg)...)
	actions = append(actions, NewSeedAction(actionsCfg))

	releaser, releaseActions := adminRelease(cfg, paths, logger)
	actions = append(actions, releaseActions...)

	freshness := NewFreshnessSource(FreshnessConfig{
		CatalogPath: cfg.CatalogPath, LedgerPath: paths.LedgerPath, DBPath: cfg.DBPath,
		SnapshotPath:    paths.SnapshotPath,
		ExportOutputDir: paths.ExportOutputDir, ExportStaticDir: paths.ExportStaticDir,
		ExportContentDir: paths.ExportContentDir,
		Releaser:         releaser,
	})
	svc := maintenance.NewService(manager, actions, secret, cfg.now,
		maintenance.WithFreshness(freshness),
		maintenance.WithPending(NewPendingDeps(actionsCfg)),
		maintenance.WithOperationLog(filesystem.NewOperationLog(paths.OperationLog)),
		maintenance.WithOperationLogErrorHandler(operationLogErrorHandler(logger)),
	)
	svcRef.Store(svc)
	// Jobs a previous process left running end as interrupted: record that
	// in the operations log as their finished entry.
	finish := svc.FinishHook()
	for _, j := range interrupted {
		finish(j)
	}

	core, closeCore, err := NewAdminService(cfg.CatalogPath, cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	a := &Admin{catalogService: core, maintenanceService: svc, paths: paths, manager: manager, release: len(releaseActions) > 0}

	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			a.stopJobs(cfg.ShutdownWait)
			closeCore()
		})
	}
	return a, closeFn, nil
}

// adminRelease builds the release actions; any configuration problem
// leaves them unregistered so the admin still starts.
func adminRelease(cfg AdminConfig, paths AdminPaths, logger *slog.Logger) (maintenance.Releaser, []maintenance.Action) {
	repo := cfg.Env(ReleaseRepoEnv)
	if repo == ReleaseDisabled {
		return nil, nil
	}
	if repo == "" {
		repo = DefaultReleaseRepo
	}
	if !release.ValidRepo(repo) {
		logger.Warn("release actions disabled: invalid " + ReleaseRepoEnv)
		return nil, nil
	}
	root := paths.RepoRoot
	releaser := cfg.releaser
	if releaser == nil {
		script, err := NewReleaseScript(root, repo, logger)
		if err != nil {
			logger.Warn("release actions disabled", "err", err)
			return nil, nil
		}
		releaser = script
	}
	actions, err := NewReleaseActions(ReleaseActionsConfig{
		RepoRoot: root, Repo: repo, DBPath: cfg.DBPath, CatalogPath: cfg.CatalogPath,
		JournalPath: paths.JournalPath, Logger: logger, Releaser: releaser,
	})
	if err != nil {
		logger.Warn("release actions disabled", "err", err)
		return nil, nil
	}
	return releaser, actions
}

// stopJobs cancels every active job and waits up to bound for them to
// finish. Work that cannot be cancelled (a started release publish) is not
// waited for past the bound; the next start marks it interrupted.
func (a *Admin) stopJobs(bound time.Duration) {
	var active []string
	for _, j := range a.manager.List() {
		if !j.Status.Terminal() {
			active = append(active, j.ID)
			_ = a.manager.Cancel(j.ID)
		}
	}
	if len(active) == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		for _, id := range active {
			a.manager.Wait(id)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(bound):
	}
}

// secretEnvKeys name the credentials whose values are redacted from logs.
var secretEnvKeys = []string{"SPOTIFY_CLIENT_ID", "SPOTIFY_CLIENT_SECRET", "GH_TOKEN", "GITHUB_TOKEN"}

// logSecrets returns the configured credential values to redact.
func logSecrets(env EnvLookup) []string {
	out := make([]string, 0, len(secretEnvKeys))
	for _, k := range secretEnvKeys {
		if v := env(k); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// jobErrorSanitizer turns a failed job's error into its user-safe message
// and logs the underlying cause (credentials redacted) server-side. A
// cancellation is logged at info level: it is an expected end.
func jobErrorSanitizer(logger *slog.Logger, env EnvLookup) func(error) string {
	return func(err error) string {
		cause := maintenance.LogText(err, logSecrets(env)...)
		if maintenance.IsCancellation(err) {
			logger.Info("admin job stopped", "cause", cause)
		} else {
			logger.Error("admin job failed", "cause", cause)
		}
		var s interface{ Safe() string }
		if errors.As(err, &s) {
			return s.Safe()
		}
		return "operation failed"
	}
}

// newJobID returns a random UUID (version 4): hex and dashes only.
func newJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24+).
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// operationLogErrorHandler logs operation log failures. The error says
// whether it was a read or a write; a canceled context is not a failure.
func operationLogErrorHandler(logger *slog.Logger) func(error) {
	return func(err error) {
		if errors.Is(err, context.Canceled) {
			logger.Info("admin operation log request canceled", "err", err)
			return
		}
		logger.Error("admin operation log failed", "err", err)
	}
}
