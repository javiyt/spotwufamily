package bootstrap

import (
	"context"
	"path/filepath"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// NewSeedAction wires db-seed to the YAML catalog, the existing SQLite
// database (never created), the database lock and backup.
func NewSeedAction(cfg MaintenanceActionsConfig) maintenance.Action {
	return maintenance.NewSeed(maintenance.SeedDeps{
		Port:           seedPort{cfg},
		DatabaseExists: func() bool { return databaseExists(cfg.DBPath) },
		Digest:         fileDigest(cfg.CatalogPath),
		Locker:         databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		Backuper:       databaseBackuper{dbPath: cfg.DBPath, dir: cfg.BackupDir},
		Provenance:     newProvenance(cfg.DBPath, cfg.CatalogPath),
		Unreconciled:   unreconciledArtists(cfg),
		DatabaseName:   filepath.Base(cfg.DBPath),
	})
}

// unreconciledArtists lists the artists whose bulk reconcile plan would
// still change something.
func unreconciledArtists(cfg MaintenanceActionsConfig) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		plan, err := reconcilePort{cfg}.PlanAll(ctx)
		if err != nil {
			return nil, err
		}
		var slugs []string
		for _, c := range plan.Changes() {
			slugs = append(slugs, c.Slug)
		}
		return slugs, nil
	}
}

type seedPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.SeedPort = seedPort{}

// Seed does what the CLI's artists seed-db does on the existing database:
// load and validate the catalog, migrate, mirror the configured artists
// (removed artists deactivated, removed aliases dropped).
// It returns the fingerprint of the catalog it loaded.
func (p seedPort) Seed(ctx context.Context) (artists.AppliedCatalog, error) {
	c, err := loadValidCatalog(ctx, p.cfg.CatalogPath)
	if err != nil {
		return artists.AppliedCatalog{}, err
	}
	if !databaseExists(p.cfg.DBPath) {
		return artists.AppliedCatalog{}, maintenance.ErrNoDatabase
	}
	database, err := openMigrated(ctx, p.cfg.DBPath)
	if err != nil {
		return artists.AppliedCatalog{}, err
	}
	defer func() { _ = database.Close() }()
	if err := database.SeedCatalog(ctx, c.Artists); err != nil {
		return artists.AppliedCatalog{}, err
	}
	return artists.FingerprintCatalog(c), nil
}

// loadValidCatalog loads the catalog; one that does not validate is
// maintenance.ErrCatalogInvalid.
func loadValidCatalog(ctx context.Context, path string) (catalog.EditorialCatalog, error) {
	c, err := yamlcatalog.NewStore().Load(ctx, path)
	if err != nil {
		return catalog.EditorialCatalog{}, err
	}
	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		return catalog.EditorialCatalog{}, maintenance.ErrCatalogInvalid
	}
	return c, nil
}

// NewPendingDeps wires what Pending and update-website read: the catalog
// fingerprint, the database presence and the SQLite provenance.
func NewPendingDeps(cfg MaintenanceActionsConfig) maintenance.PendingDeps {
	return maintenance.PendingDeps{
		DatabaseExists: func() bool { return databaseExists(cfg.DBPath) },
		Catalog: func(ctx context.Context) (artists.AppliedCatalog, error) {
			c, err := loadValidCatalog(ctx, cfg.CatalogPath)
			if err != nil {
				return artists.AppliedCatalog{}, err
			}
			return artists.FingerprintCatalog(c), nil
		},
		Provenance: newProvenance(cfg.DBPath, cfg.CatalogPath),
	}
}
