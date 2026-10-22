package bootstrap

import (
	"context"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

// NewReconcileActions wires reconcile-dry-run and reconcile-apply to the
// artists reconcile use case over the YAML catalog and the existing SQLite
// database, the database lock and the admin backup directory (the use case's
// own VACUUM INTO backup and manifest, as the CLI's --backup-dir).
func NewReconcileActions(cfg MaintenanceActionsConfig) []maintenance.Action {
	deps := maintenance.ReconcileDeps{
		Port:           reconcilePort{cfg},
		DatabaseExists: func() bool { return databaseExists(cfg.DBPath) },
		Artists:        catalogSlugs(cfg.CatalogPath),
		Locker:         databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		BackupName:     displayName(cfg.BackupDir),
		Provenance:     newProvenance(cfg.DBPath, cfg.CatalogPath),
	}
	return []maintenance.Action{maintenance.NewReconcileDryRun(deps), maintenance.NewReconcileApply(deps)}
}

// catalogSlugs lists every artist in the catalog.
func catalogSlugs(catalogPath string) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		c, err := yamlcatalog.NewStore().Load(ctx, catalogPath)
		if err != nil {
			return nil, err
		}
		slugs := make([]string, 0, len(c.Artists))
		for _, a := range c.Artists {
			slugs = append(slugs, a.Slug)
		}
		return slugs, nil
	}
}

type reconcilePort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.ReconcilePort = reconcilePort{}

// Plan runs the use case's dry run. Like the CLI's dry run it never migrates
// the database, and it never creates one.
func (p reconcilePort) Plan(ctx context.Context, slug string) (artists.ReconcileReport, error) {
	database, err := openExisting(p.cfg.DBPath)
	if err != nil {
		return artists.ReconcileReport{}, err
	}
	defer func() { _ = database.Close() }()
	return artists.NewReconcileArtist(yamlcatalog.NewStore(), database).Run(ctx, artists.ReconcileInput{
		CatalogPath: p.cfg.CatalogPath,
		Slug:        slug,
	})
}

// Migrate migrates the existing database, as the CLI does before an apply.
func (p reconcilePort) Migrate(ctx context.Context) error {
	if !databaseExists(p.cfg.DBPath) {
		return maintenance.ErrNoDatabase
	}
	database, err := openMigrated(ctx, p.cfg.DBPath)
	if err != nil {
		return err
	}
	return database.Close()
}

// Apply runs the use case's apply into the admin backup directory: it
// re-checks the checksum, writes the VACUUM INTO backup and manifest and
// applies the plan in one transaction.
func (p reconcilePort) Apply(ctx context.Context, slug, checksum string) (artists.ReconcileReport, error) {
	database, err := openExisting(p.cfg.DBPath)
	if err != nil {
		return artists.ReconcileReport{}, err
	}
	defer func() { _ = database.Close() }()
	return artists.NewReconcileArtist(yamlcatalog.NewStore(), database).Run(ctx, artists.ReconcileInput{
		CatalogPath:     p.cfg.CatalogPath,
		Slug:            slug,
		Apply:           true,
		ConfirmChecksum: checksum,
		BackupDir:       p.cfg.BackupDir,
	})
}
