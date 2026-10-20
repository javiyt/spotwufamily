package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/musicbrainz"
	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// MaintenanceActionsConfig locates the files the discovery, audit and
// metadata refresh actions work on.
type MaintenanceActionsConfig struct {
	CatalogPath string
	LedgerPath  string
	DBPath      string
	// BackupDir receives the database backup taken before a metadata refresh.
	BackupDir string
	Env       EnvLookup
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
	discover := maintenance.DiscoverDeps{Runner: discoverPort{cfg}, Digest: digest}
	refresh := maintenance.RefreshDeps{
		Runner:      refreshPort{cfg},
		Digest:      digest,
		Credentials: credentials,
		Locker:      databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		Backuper:    databaseBackuper{dbPath: cfg.DBPath, dir: cfg.BackupDir},
	}
	return []maintenance.Action{
		maintenance.NewDiscoverPreview(discover),
		maintenance.NewDiscoverImport(discover),
		maintenance.NewAuditProfiles(maintenance.AuditDeps{Runner: auditPort{cfg}, Digest: digest, Credentials: credentials}),
		maintenance.NewRefreshMetadataPreview(refresh),
		maintenance.NewRefreshMetadata(refresh),
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

type discoverPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.DiscoverPort = discoverPort{}

func (p discoverPort) Run(ctx context.Context, apply bool, progress func(artists.DiscoverWuFamilyProgress)) (artists.DiscoverWuFamilyReport, error) {
	runner, closeFn, err := NewDiscoverRunner(ctx, p.cfg.CatalogPath, p.cfg.Options...)
	if err != nil {
		return artists.DiscoverWuFamilyReport{}, err
	}
	defer closeFn()
	return runner.Run(ctx, artists.DiscoverWuFamilyOptions{LedgerPath: p.cfg.LedgerPath, Apply: apply, Progress: progress})
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
