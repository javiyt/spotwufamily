package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/catalogaudit"
	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// NewPublishPrepActions wires db-verify, db-snapshot, export, audit and
// refresh-export to the SQLite database, the snapshot file, the export roots
// and the database lock. None of them creates or migrates the database.
func NewPublishPrepActions(cfg MaintenanceActionsConfig) []maintenance.Action {
	export := exportPort{cfg}
	return maintenance.NewPublishPrepActions(maintenance.PublishPrepDeps{
		DatabaseExists: func() bool { return databaseExists(cfg.DBPath) },
		Digest:         publishPrepDigest(cfg),
		Verifier:       verifyPort{cfg},
		Snapshot:       snapshotPort{cfg},
		Export:         export,
		Stager:         exportStager{cfg},
		Audit:          repoAudit(cfg, export),
		Locker:         databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")},
		DatabaseName:   filepath.Base(cfg.DBPath),
		SnapshotName:   filepath.Base(cfg.SnapshotPath),
		ExportNames: []string{
			displayName(cfg.ExportOutputDir) + "/",
			displayName(cfg.ExportStaticDir) + "/search-index.json",
			displayName(cfg.ExportContentDir) + "/",
		},
	})
}

// displayName shows the last two elements of path ("data/generated"), which
// identify an export root without revealing where the repository lives.
func displayName(path string) string {
	return filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
}

func (cfg MaintenanceActionsConfig) exportRoots() []string {
	return []string{cfg.ExportOutputDir, cfg.ExportStaticDir, cfg.ExportContentDir}
}

func databaseExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// openExisting opens the database without creating it.
func openExisting(path string) (*sqliteadapter.Database, error) {
	if !databaseExists(path) {
		return nil, maintenance.ErrNoDatabase
	}
	return sqliteadapter.Open(path)
}

// verifyDatabase runs the read-only checks and maps a failed check to
// maintenance.ErrDatabaseInvalid.
func verifyDatabase(ctx context.Context, database *sqliteadapter.Database) (maintenance.VerifyResult, error) {
	migrations, err := sqliteadapter.EmbeddedMigrations()
	if err != nil {
		return maintenance.VerifyResult{}, err
	}
	report, err := database.Verify(ctx, migrations)
	result := maintenance.VerifyResult{Migrations: report.Migrations, Checks: report.Checks}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		for _, check := range []string{"integrity_check", "foreign_key_check", "migrations"} {
			if !slices.Contains(report.Checks, check) {
				result.FailedCheck = check
				break
			}
		}
		return result, fmt.Errorf("%w: %w", maintenance.ErrDatabaseInvalid, err)
	}
	return result, nil
}

// publishPrepDigest fingerprints the database (and its WAL), the snapshot and
// every file under the export roots by name, size and modification time, so
// it stays cheap on large exports.
func publishPrepDigest(cfg MaintenanceActionsConfig) maintenance.CatalogDigest {
	return func(ctx context.Context) (string, error) {
		h := sha256.New()
		stat := func(path string) error {
			info, err := os.Stat(path)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				_, _ = fmt.Fprintf(h, "absent;")
			case err != nil:
				return err
			default:
				_, _ = fmt.Fprintf(h, "%d:%d;", info.Size(), info.ModTime().UnixNano())
			}
			return nil
		}
		for _, path := range []string{cfg.DBPath, cfg.DBPath + "-wal", cfg.SnapshotPath} {
			if err := stat(path); err != nil {
				return "", err
			}
		}
		for _, root := range cfg.exportRoots() {
			_, _ = fmt.Fprintf(h, "root;")
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(root, path)
				_, _ = fmt.Fprintf(h, "%q:%d:%d;", rel, info.Size(), info.ModTime().UnixNano())
				return nil
			})
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}

type verifyPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.VerifyPort = verifyPort{}

// Verify checks the database like the CLI's db verify: integrity, foreign
// keys, migrations and, when asked, an existing snapshot's freshness.
func (p verifyPort) Verify(ctx context.Context, checkSnapshot bool) (maintenance.VerifyResult, error) {
	database, err := openExisting(p.cfg.DBPath)
	if err != nil {
		return maintenance.VerifyResult{}, err
	}
	defer func() { _ = database.Close() }()
	result, err := verifyDatabase(ctx, database)
	if err != nil || !checkSnapshot {
		return result, err
	}
	generated, err := database.Snapshot(ctx)
	if err != nil {
		return result, err
	}
	current, err := sqliteadapter.ReadSnapshot(p.cfg.SnapshotPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		result.Snapshot = maintenance.SnapshotAbsent
	case err != nil:
		result.Snapshot = maintenance.SnapshotUnreadable
	case bytes.Equal(generated, current):
		result.Snapshot = maintenance.SnapshotFresh
	default:
		result.Snapshot = maintenance.SnapshotStale
	}
	return result, nil
}

type snapshotPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.SnapshotPort = snapshotPort{}

// Write verifies the database, writes the snapshot to a temporary file with
// the existing sqlite WriteSnapshot and, only if it differs from the current
// snapshot, copies the current one to <snapshot>.prev (replacing any older
// .prev) and renames the new one into place.
func (p snapshotPort) Write(ctx context.Context) (maintenance.SnapshotWrite, error) {
	database, err := openExisting(p.cfg.DBPath)
	if err != nil {
		return maintenance.SnapshotWrite{}, err
	}
	defer func() { _ = database.Close() }()
	if _, err := verifyDatabase(ctx, database); err != nil {
		return maintenance.SnapshotWrite{}, err
	}

	path := p.cfg.SnapshotPath
	// The temporary name keeps the snapshot's suffix so WriteSnapshot
	// compresses it the same way.
	tmp, err := reserveTemp(path)
	if err != nil {
		return maintenance.SnapshotWrite{}, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := database.WriteSnapshot(ctx, tmp); err != nil {
		return maintenance.SnapshotWrite{}, err
	}
	generated, err := sqliteadapter.ReadSnapshot(tmp)
	if err != nil {
		return maintenance.SnapshotWrite{}, err
	}
	current, readErr := sqliteadapter.ReadSnapshot(path)
	if readErr == nil && bytes.Equal(current, generated) {
		return maintenance.SnapshotWrite{}, nil
	}
	if err := ctx.Err(); err != nil {
		return maintenance.SnapshotWrite{}, err
	}

	var result maintenance.SnapshotWrite
	previous, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return maintenance.SnapshotWrite{}, err
	default:
		if err := writeAtomic(path+".prev", previous); err != nil {
			return maintenance.SnapshotWrite{}, fmt.Errorf("keep previous snapshot: %w", err)
		}
		result.PreviousKept = true
	}
	if err := os.Rename(tmp, path); err != nil {
		return maintenance.SnapshotWrite{}, fmt.Errorf("replace snapshot: %w", err)
	}
	result.Changed = true
	return result, nil
}

// reserveTemp returns an unused hidden path next to path ending in its base
// name. The file does not exist when it returns.
func reserveTemp(path string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*-"+filepath.Base(path))
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

// writeAtomic writes data to path through a temporary file and a rename.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*-"+filepath.Base(path))
	if err != nil {
		return err
	}
	name := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, 0o644)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

type exportPort struct{ cfg MaintenanceActionsConfig }

var _ maintenance.ExportPort = exportPort{}

// Run verifies the database (as the CLI's export does) and runs the catalog
// export into w with the verified relationships from the editorial catalog.
func (p exportPort) Run(ctx context.Context, w catalogexport.Writer) (catalogexport.Report, error) {
	database, err := openExisting(p.cfg.DBPath)
	if err != nil {
		return catalogexport.Report{}, err
	}
	defer func() { _ = database.Close() }()
	if _, err := verifyDatabase(ctx, database); err != nil {
		return catalogexport.Report{}, err
	}
	relationships, err := exportRelationships(ctx, p.cfg.CatalogPath)
	if err != nil {
		return catalogexport.Report{}, err
	}
	return catalogexport.NewExportCatalog(database, w).Run(ctx, catalogexport.Options{
		OutputDir:     p.cfg.ExportOutputDir,
		StaticDir:     p.cfg.ExportStaticDir,
		ContentDir:    p.cfg.ExportContentDir,
		Relationships: relationships,
	})
}

// exportRelationships loads the verified relationships; a missing catalog
// exports none, as the CLI's export does without --catalog.
func exportRelationships(ctx context.Context, path string) ([]catalogexport.Relationship, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	c, err := yamlcatalog.NewStore().Load(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("load artist catalog: %w", err)
	}
	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		return nil, fmt.Errorf("artist catalog is invalid: %s", issues[0].Error())
	}
	return catalogexport.RelationshipsFromCatalog(c), nil
}

type exportStager struct{ cfg MaintenanceActionsConfig }

var _ maintenance.ExportStager = exportStager{}

func (s exportStager) Stage() (maintenance.StagedExport, error) {
	return filesystem.NewStagedWriter(s.cfg.exportRoots()...)
}

// repoAudit runs the read-only snapshot and export audit, including whether
// an export of the database now would change any file.
func repoAudit(cfg MaintenanceActionsConfig, export exportPort) maintenance.RepoAuditPort {
	return func(ctx context.Context) (catalogaudit.Report, error) {
		database, err := openExisting(cfg.DBPath)
		if err != nil {
			return catalogaudit.Report{}, err
		}
		defer func() { _ = database.Close() }()
		return catalogaudit.Run(ctx, catalogaudit.Options{
			Database:     database,
			ReadSnapshot: sqliteadapter.ReadSnapshot,
			SnapshotPath: cfg.SnapshotPath,
			Stat:         os.Stat,
			OutputDir:    cfg.ExportOutputDir,
			StaticDir:    cfg.ExportStaticDir,
			ExportDrift: func(ctx context.Context) ([]string, error) {
				diff := filesystem.NewDiffWriter()
				if _, err := export.Run(ctx, diff); err != nil {
					return nil, err
				}
				var labels []string
				for _, path := range diff.Changed() {
					labels = append(labels, exportLabel(cfg, path))
				}
				return labels, nil
			},
		})
	}
}

// exportLabel names an export file relative to its root's display name.
func exportLabel(cfg MaintenanceActionsConfig, path string) string {
	for _, root := range cfg.exportRoots() {
		if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
			return displayName(root) + "/" + filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}
