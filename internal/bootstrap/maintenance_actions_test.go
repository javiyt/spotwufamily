package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type nopReporter struct{}

func (nopReporter) Step(string)       {}
func (nopReporter) Progress(int, int) {}
func (nopReporter) Count(string, int) {}

func actionsByName(cfg MaintenanceActionsConfig) map[string]maintenance.Action {
	out := map[string]maintenance.Action{}
	for _, a := range NewDiscoveryAuditRefreshActions(cfg) {
		out[a.Name()] = a
	}
	return out
}

func newActionsConfig(t *testing.T, env EnvLookup, opts ...MaintenanceOption) (MaintenanceActionsConfig, maintenancePaths) {
	t.Helper()
	paths := newMaintenancePaths(t)
	return MaintenanceActionsConfig{
		CatalogPath: paths.catalog,
		LedgerPath:  filepath.Join(filepath.Dir(paths.catalog), "wu-candidates.yaml"),
		DBPath:      paths.db,
		BackupDir:   filepath.Join(filepath.Dir(paths.catalog), "backups"),
		Env:         env,
		Options:     opts,
	}, paths
}

// confirmedSpec calls Spec as Service.Run does: with the fingerprint of a
// fresh preview the user confirmed.
func confirmedSpec(t *testing.T, a maintenance.Action, p maintenance.Params) (jobs.Spec, error) {
	t.Helper()
	ctx := context.Background()
	if pv, err := a.Preview(ctx, p); err == nil {
		ctx = maintenance.WithVerifiedFingerprint(ctx, pv.Fingerprint)
	}
	return a.Spec(ctx, p)
}

func runSpec(t *testing.T, a maintenance.Action, p maintenance.Params) (jobs.Outcome, error) {
	t.Helper()
	require.NoError(t, a.Validate(p))
	spec, err := confirmedSpec(t, a, p)
	require.NoError(t, err)
	return spec.Run(context.Background(), nopReporter{})
}

func TestMaintenanceActionsAreRegistered(t *testing.T) {
	cfg, _ := newActionsConfig(t, envMap(nil))
	var names []string
	for _, a := range NewDiscoveryAuditRefreshActions(cfg) {
		names = append(names, a.Name())
	}
	require.Equal(t, []string{"discover-preview", "discover-import", "audit-profiles", "refresh-metadata-preview", "refresh-metadata"}, names)
}

func TestOfflineAuditActionNeedsNoCredentialsOrDatabase(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	actions := actionsByName(cfg)

	ok, reason := actions["refresh-metadata"].Available()
	require.False(t, ok)
	require.Equal(t, "Spotify credentials are not configured", reason)

	audit := actions["audit-profiles"]
	ok, _ = audit.Available()
	require.True(t, ok)
	out, err := runSpec(t, audit, maintenance.Params{"offline": "true"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status)
	require.Len(t, out.Result.Report.Rows, 1)
	require.Equal(t, "wu-tang-clan", out.Result.Report.Rows[0][0])
	require.NoFileExists(t, paths.db, "an offline audit never creates the database")
}

func TestRefreshActionLocksAndBacksUpDatabase(t *testing.T) {
	cfg, paths := newActionsConfig(t, credentials, withArtistFetcher(stubArtistFetcher{}))
	refresh := actionsByName(cfg)["refresh-metadata"]

	out, err := runSpec(t, refresh, maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)

	backups, err := os.ReadDir(cfg.BackupDir)
	require.NoError(t, err)
	require.Len(t, backups, 1)

	release, err := filesystem.NewResourceLock(paths.db + ".lock").TryLock()
	require.NoError(t, err, "the job releases the database lock")
	release()
}

func TestRefreshActionRefusesWhileDatabaseIsLocked(t *testing.T) {
	cfg, paths := newActionsConfig(t, credentials, withArtistFetcher(stubArtistFetcher{}))
	release, err := filesystem.NewResourceLock(paths.db + ".lock").TryLock()
	require.NoError(t, err)
	defer release()

	_, err = runSpec(t, actionsByName(cfg)["refresh-metadata"], maintenance.Params{})

	var se *maintenance.SafeError
	require.True(t, errors.As(err, &se), "got %v", err)
	require.Equal(t, "conflict", se.Code)
	require.NoDirExists(t, cfg.BackupDir)
}

func TestRefreshPreviewActionWritesNothing(t *testing.T) {
	cfg, paths := newActionsConfig(t, credentials, withArtistFetcher(stubArtistFetcher{}))
	before, err := os.ReadFile(paths.catalog)
	require.NoError(t, err)

	out, err := runSpec(t, actionsByName(cfg)["refresh-metadata-preview"], maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status)

	after, err := os.ReadFile(paths.catalog)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoFileExists(t, paths.db)
	require.NoDirExists(t, cfg.BackupDir)
}

func TestCatalogDigestTracksCatalogAndLedger(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	digest := fileDigest(cfg.CatalogPath, cfg.LedgerPath)

	first, err := digest(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.LedgerPath, []byte("version: 1\n"), 0o644))
	second, err := digest(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.NoError(t, os.WriteFile(paths.catalog, []byte(maintenanceCatalogYAML+"\n"), 0o644))
	third, err := digest(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, second, third)
}

type okSyncFetcher struct{}

func (okSyncFetcher) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	return catalog.ArtistCandidate{SpotifyID: id, Name: "Wu-Tang Clan"}, nil
}

func (okSyncFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return nil, nil
}

func (okSyncFetcher) GetAlbum(context.Context, string) (catalog.Release, error) {
	return catalog.Release{}, nil
}

func (okSyncFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return nil, nil
}

func syncActionsByName(cfg MaintenanceActionsConfig) map[string]maintenance.Action {
	out := map[string]maintenance.Action{}
	for _, a := range NewSyncActions(cfg) {
		out[a.Name()] = a
	}
	return out
}

func newSyncActionsConfig(t *testing.T, env EnvLookup, opts ...MaintenanceOption) (MaintenanceActionsConfig, maintenancePaths) {
	t.Helper()
	cfg, paths := newActionsConfig(t, env, opts...)
	cfg.SnapshotPath = paths.snapshot
	return cfg, paths
}

func TestSyncActionsAreRegistered(t *testing.T) {
	cfg, _ := newSyncActionsConfig(t, envMap(nil))
	var names []string
	for _, a := range NewSyncActions(cfg) {
		names = append(names, a.Name())
	}
	require.Equal(t, []string{"sync-dry-run", "sync"}, names)
}

func TestSyncActionBacksUpMigratesAndWritesSnapshot(t *testing.T) {
	cfg, paths := newSyncActionsConfig(t, credentials, withSyncFetcher(okSyncFetcher{}))
	sync := syncActionsByName(cfg)["sync"]

	pv, err := sync.Preview(context.Background(), maintenance.Params{"scope": "artist", "artists": "wu-tang-clan"})
	require.NoError(t, err)
	require.Empty(t, pv.RequiresTyped)

	out, err := runSpec(t, sync, maintenance.Params{"scope": "artist", "artists": "wu-tang-clan"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	backups, err := os.ReadDir(cfg.BackupDir)
	require.NoError(t, err)
	require.Len(t, backups, 1, "a database that did not exist yet is migrated, then backed up")
	require.FileExists(t, paths.snapshot)

	release, err := filesystem.NewResourceLock(paths.db + ".lock").TryLock()
	require.NoError(t, err, "the job releases the database lock")
	release()
}

func TestSyncActionRateLimitEndsPartialResumableAndPreviewSeesRun(t *testing.T) {
	cfg, paths := newSyncActionsConfig(t, credentials, withSyncFetcher(abortingFetcher{}))
	sync := syncActionsByName(cfg)["sync"]

	out, err := runSpec(t, sync, maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusPartial, out.Status)
	require.True(t, out.Resumable)
	require.Contains(t, out.Result.Summary, "Spotify rate limit reached")
	require.FileExists(t, paths.snapshot, "the partial snapshot is written")

	pv, err := sync.Preview(context.Background(), maintenance.Params{"scope": "all", "resume": "true"})
	require.NoError(t, err)
	var resume string
	for _, line := range pv.Scope {
		if line.Label == "Resume" {
			resume = line.Value
		}
	}
	require.Contains(t, resume, "continues partial run #")
}

func TestSyncDryRunActionWritesNothing(t *testing.T) {
	cfg, paths := newSyncActionsConfig(t, envMap(nil))
	dry := syncActionsByName(cfg)["sync-dry-run"]
	ok, _ := dry.Available()
	require.True(t, ok)

	out, err := runSpec(t, dry, maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status)
	require.Contains(t, out.Result.Summary, "Dry run: 1 artists")
	require.NoFileExists(t, paths.db)
	require.NoFileExists(t, paths.snapshot)
	require.NoDirExists(t, cfg.BackupDir)
}

func TestSyncActionRejectsUnknownSlug(t *testing.T) {
	cfg, _ := newSyncActionsConfig(t, credentials)
	_, err := syncActionsByName(cfg)["sync"].Preview(context.Background(), maintenance.Params{"scope": "artist", "artists": "cappadonna"})
	var se *maintenance.SafeError
	require.ErrorAs(t, err, &se)
	require.Equal(t, "validation", se.Code)
	require.Contains(t, se.Message, "cappadonna")
}
