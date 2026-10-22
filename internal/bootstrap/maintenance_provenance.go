package bootstrap

import (
	"context"
	"errors"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// databaseProvenance records provenance in the existing SQLite database. It
// never creates or migrates it: on a database without the provenance tables
// the writes are skipped, so it keeps reading as unknown.
type databaseProvenance struct{ dbPath, catalogPath string }

var _ maintenance.Provenance = databaseProvenance{}

func newProvenance(dbPath, catalogPath string) maintenance.Provenance {
	return databaseProvenance{dbPath: dbPath, catalogPath: catalogPath}
}

func (p databaseProvenance) with(fn func(*sqliteadapter.Database) error) error {
	database, err := openExisting(p.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	return fn(database)
}

// write runs fn only when the database has the provenance tables.
func (p databaseProvenance) write(ctx context.Context, fn func(*sqliteadapter.Database) error) error {
	return p.with(func(database *sqliteadapter.Database) error {
		has, err := database.HasProvenance(ctx)
		if err != nil || !has {
			return err
		}
		return fn(database)
	})
}

func (p databaseProvenance) AppliedCatalog(ctx context.Context) (artists.AppliedCatalog, bool, error) {
	var (
		applied artists.AppliedCatalog
		ok      bool
	)
	err := p.with(func(database *sqliteadapter.Database) error {
		var err error
		applied, ok, err = database.AppliedCatalog(ctx)
		return err
	})
	return applied, ok, err
}

func (p databaseProvenance) RecordApplied(ctx context.Context, applied artists.AppliedCatalog) error {
	return p.write(ctx, func(database *sqliteadapter.Database) error { return database.RecordApplied(ctx, applied) })
}

func (p databaseProvenance) Checkpoint(ctx context.Context, stage string) (artists.StageCheckpoint, bool, error) {
	var (
		cp artists.StageCheckpoint
		ok bool
	)
	err := p.with(func(database *sqliteadapter.Database) error {
		var err error
		cp, ok, err = database.Checkpoint(ctx, stage)
		return err
	})
	return cp, ok, err
}

func (p databaseProvenance) RecordCheckpoint(ctx context.Context, cp artists.StageCheckpoint) error {
	return p.write(ctx, func(database *sqliteadapter.Database) error { return database.RecordCheckpoint(ctx, cp) })
}

// DatabaseDigest hashes the deterministic snapshot, as the freshness source
// does, so checkpoints compare with its DatabaseDigest.
func (p databaseProvenance) DatabaseDigest(ctx context.Context) (string, error) {
	var digest string
	err := p.with(func(database *sqliteadapter.Database) error {
		var err error
		digest, err = snapshotDigest(ctx, database)
		return err
	})
	return digest, err
}

func (p databaseProvenance) CatalogDigest(ctx context.Context) (string, error) {
	if p.catalogPath == "" {
		return "", errors.New("no catalog configured")
	}
	c, err := yamlcatalog.NewStore().Load(ctx, p.catalogPath)
	if err != nil {
		return "", err
	}
	return artists.FingerprintCatalog(c).Digest, nil
}

func snapshotDigest(ctx context.Context, database *sqliteadapter.Database) (string, error) {
	data, err := database.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return sum(data), nil
}

// capturingCatalogStore remembers the last catalog it loaded, so a run can
// record the catalog it actually used instead of re-reading the file later.
type capturingCatalogStore struct {
	inner  yamlcatalog.Store
	loaded *catalog.EditorialCatalog
}

func (s *capturingCatalogStore) Load(ctx context.Context, path string) (catalog.EditorialCatalog, error) {
	c, err := s.inner.Load(ctx, path)
	if err == nil {
		s.loaded = &c
	} else {
		s.loaded = nil
	}
	return c, err
}
