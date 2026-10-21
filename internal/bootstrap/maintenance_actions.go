package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/musicbrainz"
	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// MaintenanceActionsConfig locates the files the discovery, audit and
// metadata refresh actions work on.
type MaintenanceActionsConfig struct {
	CatalogPath string
	LedgerPath  string
	DBPath      string
	// SnapshotPath receives the database snapshot written after a sync.
	SnapshotPath string
	// BackupDir receives the database backup taken before a metadata refresh
	// or a sync, and the timestamped catalog copy taken before a metadata
	// refresh or a discovery import.
	BackupDir string
	// Export roots written by the export actions (catalogexport options).
	ExportOutputDir  string
	ExportStaticDir  string
	ExportContentDir string
	Env              EnvLookup
	// Options tune the shared runners (e.g. WithWikipedia, test seams).
	Options []MaintenanceOption
}

// NewDiscoveryAuditRefreshActions wires discover-preview, discover-import,
// audit-profiles, refresh-metadata-preview and refresh-metadata to the
// shared runners, the YAML store, the SQLite cache and the database lock.
func NewDiscoveryAuditRefreshActions(cfg MaintenanceActionsConfig) []maintenance.Action {
	credentials := func() bool {
		return cfg.Env("SPOTIFY_CLIENT_ID") != "" && cfg.Env("SPOTIFY_CLIENT_SECRET") != ""
	}
	digest := fileDigest(cfg.CatalogPath, cfg.LedgerPath)
	catalogBackup := catalogBackuper{path: cfg.CatalogPath, dir: cfg.BackupDir}
	discover := maintenance.DiscoverDeps{Runner: discoverPort{cfg}, Digest: digest, CatalogBackuper: catalogBackup}
	refresh := maintenance.RefreshDeps{
		Runner:          refreshPort{cfg},
		Digest:          digest,
		Credentials:     credentials,
		Locker:          databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		Backuper:        databaseBackuper{dbPath: cfg.DBPath, dir: cfg.BackupDir},
		CatalogBackuper: catalogBackup,
	}
	return []maintenance.Action{
		maintenance.NewDiscoverPreview(discover),
		maintenance.NewDiscoverImport(discover),
		maintenance.NewAuditProfiles(maintenance.AuditDeps{Runner: auditPort{cfg}, Digest: digest, Credentials: credentials}),
		maintenance.NewRefreshMetadataPreview(refresh),
		maintenance.NewRefreshMetadata(refresh),
	}
}

// NewSyncActions wires sync-dry-run and sync to the shared sync runner, the
// YAML catalog (for the scope), the SQLite database (for the resumable run)
// and the database lock and backup.
func NewSyncActions(cfg MaintenanceActionsConfig) []maintenance.Action {
	deps := maintenance.SyncDeps{
		Runner:    syncPort{cfg},
		Digest:    fileDigest(cfg.CatalogPath),
		Syncable:  syncableArtists(cfg.CatalogPath),
		Resumable: resumableSyncRun(cfg.DBPath),
		Credentials: func() bool {
			return cfg.Env("SPOTIFY_CLIENT_ID") != "" && cfg.Env("SPOTIFY_CLIENT_SECRET") != ""
		},
		Locker:   databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		Backuper: databaseBackuper{dbPath: cfg.DBPath, dir: cfg.BackupDir},
	}
	return []maintenance.Action{maintenance.NewSyncDryRun(deps), maintenance.NewSync(deps)}
}

type syncPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.SyncPort = syncPort{}

// Run builds a sync runner for this call (dry or real) and runs it. The
// runner records pending reconciliations and writes the (partial) snapshot.
func (p syncPort) Run(ctx context.Context, o catalogsync.Options) (maintenance.SyncRunResult, error) {
	opts := append([]MaintenanceOption{WithDryRun(o.DryRun)}, p.cfg.Options...)
	runner, closeFn, err := NewSyncRunner(ctx, p.cfg.CatalogPath, p.cfg.DBPath, p.cfg.SnapshotPath, p.cfg.Env, opts...)
	if errors.Is(err, ErrSpotifyCredentialsMissing) {
		return maintenance.SyncRunResult{}, maintenance.ErrCredentialsMissing
	}
	if err != nil {
		return maintenance.SyncRunResult{}, err
	}
	defer closeFn()
	result, err := runner.Run(ctx, o)
	return maintenance.SyncRunResult{Report: result.Report, SnapshotErr: result.SnapshotErr}, err
}

// syncableArtists lists enabled artists with a Spotify profile the catalog's
// sync policy allows, exactly as catalogsync selects them.
func syncableArtists(catalogPath string) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		c, err := yamlcatalog.NewStore().Load(ctx, catalogPath)
		if err != nil {
			return nil, err
		}
		var slugs []string
		for _, artist := range c.Artists {
			if artist.Enabled && len(artist.SyncableSpotifyIDs(c.ProfileSyncPolicy)) > 0 {
				slugs = append(slugs, artist.Slug)
			}
		}
		return slugs, nil
	}
}

// resumableSyncRun finds the partial run a resumed sync would continue (the
// default market, incremental sync). A missing database has none; an
// existing one is read without being migrated.
func resumableSyncRun(dbPath string) func(context.Context) (maintenance.ResumableRun, bool, error) {
	return func(ctx context.Context) (maintenance.ResumableRun, bool, error) {
		if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
			return maintenance.ResumableRun{}, false, nil
		}
		database, err := sqliteadapter.Open(dbPath)
		if err != nil {
			return maintenance.ResumableRun{}, false, err
		}
		defer func() { _ = database.Close() }()
		run, ok, err := database.FindResumableSyncRun(ctx, catalogsync.SyncRun{Market: catalogsync.MarketOrDefault("")})
		if err != nil || !ok {
			return maintenance.ResumableRun{}, false, err
		}
		completed := make([]string, 0, len(run.CompletedArtistSpotifyIDs))
		for slug := range run.CompletedArtistSpotifyIDs {
			completed = append(completed, slug)
		}
		sort.Strings(completed)
		return maintenance.ResumableRun{ID: run.ID, CompletedSlugs: completed}, true, nil
	}
}

// fileDigest hashes the content of each file; a missing file hashes as absent.
func fileDigest(paths ...string) maintenance.CatalogDigest {
	return func(context.Context) (string, error) {
		h := sha256.New()
		for _, path := range paths {
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				_, _ = fmt.Fprintf(h, "absent;")
			case err != nil:
				return "", err
			default:
				sum := sha256.Sum256(data)
				_, _ = fmt.Fprintf(h, "%s;", hex.EncodeToString(sum[:]))
			}
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}

// catalogBackuper copies the catalog YAML to the backup directory as
// <base>-<UTC timestamp>.yaml (mode 0600) before a job rewrites it.
type catalogBackuper struct{ path, dir string }

var _ maintenance.CatalogBackuper = catalogBackuper{}

func (b catalogBackuper) BackupCatalog(context.Context) error {
	_, err := filesystem.BackupFile(b.path, b.dir, time.Now())
	return err
}

type discoverPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.DiscoverPort = discoverPort{}

func (p discoverPort) Run(ctx context.Context, apply bool, only []string, progress func(artists.DiscoverWuFamilyProgress)) (artists.DiscoverWuFamilyReport, error) {
	runner, closeFn, err := NewDiscoverRunner(ctx, p.cfg.CatalogPath, p.cfg.Options...)
	if err != nil {
		return artists.DiscoverWuFamilyReport{}, err
	}
	defer closeFn()
	return runner.Run(ctx, artists.DiscoverWuFamilyOptions{LedgerPath: p.cfg.LedgerPath, Apply: apply, OnlySlugs: only, Progress: progress})
}

type refreshPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.RefreshPort = refreshPort{}

func (p refreshPort) Run(ctx context.Context, dryRun, force bool, progress func(artists.RefreshGenresProgress)) (artists.RefreshGenresReport, error) {
	opts := append([]MaintenanceOption{WithDryRun(dryRun)}, p.cfg.Options...)
	runner, closeFn, err := NewRefreshRunner(ctx, p.cfg.CatalogPath, p.cfg.DBPath, p.cfg.Env, opts...)
	if errors.Is(err, ErrSpotifyCredentialsMissing) {
		return artists.RefreshGenresReport{}, maintenance.ErrCredentialsMissing
	}
	if err != nil {
		return artists.RefreshGenresReport{}, err
	}
	defer closeFn()
	return runner.Run(ctx, artists.RefreshGenresOptions{DryRun: dryRun, Force: force, Progress: progress})
}

// auditPort runs the profile audit. It opens an existing database only as an
// album/track cache and never creates or migrates it; offline it uses no
// Spotify client and no MusicBrainz client.
type auditPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.AuditPort = auditPort{}

func (p auditPort) Run(ctx context.Context, artist string, offline bool, progress func(done, total int)) (artists.ProfileAuditReport, error) {
	var cache artists.SpotifyAlbumCache
	if info, err := os.Stat(p.cfg.DBPath); err == nil && !info.IsDir() {
		database, err := sqliteadapter.Open(p.cfg.DBPath)
		if err != nil {
			return artists.ProfileAuditReport{}, err
		}
		defer func() { _ = database.Close() }()
		cache = database
	}

	var (
		source      artists.SpotifyProfileSource
		musicBrainz artists.MusicBrainzReleaseGroupSearcher = offlineMusicBrainz{}
	)
	if offline {
		source = artists.NewCacheFirstProfileSource(cache, nil)
	} else {
		clientConfig, err := spotifyConfig(p.cfg.Env, resolveMaintenanceConfig(p.cfg.Options))
		if errors.Is(err, ErrSpotifyCredentialsMissing) {
			return artists.ProfileAuditReport{}, maintenance.ErrCredentialsMissing
		}
		if err != nil {
			return artists.ProfileAuditReport{}, err
		}
		client, err := spotifyadapter.NewClient(clientConfig)
		if err != nil {
			return artists.ProfileAuditReport{}, err
		}
		source = artists.NewCacheFirstProfileSource(cache, spotifyadapter.NewProfileSource(client))
		musicBrainz = musicbrainz.NewClient(musicbrainz.Config{UserAgent: p.cfg.Env("MUSICBRAINZ_USER_AGENT")})
	}

	options := artists.AuditProfilesOptions{CatalogPath: p.cfg.CatalogPath, ArtistSlug: artist, Offline: offline}
	if progress != nil {
		options.Progress = func(done, total int, _ artists.ProfileAuditResult) { progress(done, total) }
	}
	return artists.NewAuditProfiles(yamlcatalog.NewStore(), source, musicBrainz).Run(ctx, options)
}

// offlineMusicBrainz is never queried: the audit skips MusicBrainz offline.
type offlineMusicBrainz struct{}

func (offlineMusicBrainz) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return nil, artists.ErrSourceUnavailable
}

// databaseLocker maps the file lock's ErrLocked to maintenance.ErrLocked so
// the lock path never reaches users.
type databaseLocker struct{ lock *filesystem.ResourceLock }

var _ maintenance.Locker = databaseLocker{}

func (l databaseLocker) TryLock() (func(), error) {
	release, err := l.lock.TryLock()
	if errors.Is(err, filesystem.ErrLocked) {
		return nil, maintenance.ErrLocked
	}
	return release, err
}

// databaseBackuper migrates the database (a backup is verified against all
// migrations) and writes a VACUUM INTO copy to dir.
type databaseBackuper struct{ dbPath, dir string }

var _ maintenance.Backuper = databaseBackuper{}

func (b databaseBackuper) Backup(ctx context.Context) error {
	database, err := openMigrated(ctx, b.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	_, err = database.BackupTo(ctx, b.dir)
	return err
}
