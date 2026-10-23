package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

var _ artists.ProvenanceStore = (*Database)(nil)

// AppliedCatalog returns the catalog fingerprint last applied, ok=false when
// none was recorded (including databases predating the provenance migration).
func (d *Database) AppliedCatalog(ctx context.Context) (artists.AppliedCatalog, bool, error) {
	var applied artists.AppliedCatalog
	exists, err := d.tableExists(ctx, "catalog_applied")
	if err != nil || !exists {
		return applied, false, err
	}
	var raw string
	err = d.db.QueryRowContext(ctx, `SELECT digest, artists_json, applied_at FROM catalog_applied WHERE id = 1`).
		Scan(&applied.Digest, &raw, &applied.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return artists.AppliedCatalog{}, false, nil
	}
	if err != nil {
		return artists.AppliedCatalog{}, false, fmt.Errorf("read applied catalog: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &applied.Artists); err != nil {
		return artists.AppliedCatalog{}, false, fmt.Errorf("decode applied catalog: %w", err)
	}

	return applied, true, nil
}

func (d *Database) RecordApplied(ctx context.Context, applied artists.AppliedCatalog) error {
	data, err := json.Marshal(applied.Artists)
	if err != nil {
		return fmt.Errorf("encode applied catalog: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO catalog_applied(id, digest, artists_json, applied_at) VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET digest = excluded.digest, artists_json = excluded.artists_json, applied_at = excluded.applied_at`,
		applied.Digest, string(data), applied.AppliedAt); err != nil {
		return fmt.Errorf("record applied catalog: %w", err)
	}

	return nil
}

func (d *Database) Checkpoint(ctx context.Context, stage string) (artists.StageCheckpoint, bool, error) {
	var cp artists.StageCheckpoint
	exists, err := d.tableExists(ctx, "stage_checkpoints")
	if err != nil || !exists {
		return cp, false, err
	}
	err = d.db.QueryRowContext(ctx, `SELECT stage, catalog_digest, db_digest, at FROM stage_checkpoints WHERE stage = ?`, stage).
		Scan(&cp.Stage, &cp.CatalogDigest, &cp.DBDigest, &cp.At)
	if errors.Is(err, sql.ErrNoRows) {
		return artists.StageCheckpoint{}, false, nil
	}
	if err != nil {
		return artists.StageCheckpoint{}, false, fmt.Errorf("read %s checkpoint: %w", stage, err)
	}

	return cp, true, nil
}

func (d *Database) RecordCheckpoint(ctx context.Context, cp artists.StageCheckpoint) error {
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO stage_checkpoints(stage, catalog_digest, db_digest, at) VALUES (?, ?, ?, ?)
ON CONFLICT(stage) DO UPDATE SET catalog_digest = excluded.catalog_digest, db_digest = excluded.db_digest, at = excluded.at`,
		cp.Stage, cp.CatalogDigest, cp.DBDigest, cp.At); err != nil {
		return fmt.Errorf("record %s checkpoint: %w", cp.Stage, err)
	}

	return nil
}

// HasProvenance reports whether the provenance tables exist (false on a
// database predating the provenance migration).
func (d *Database) HasProvenance(ctx context.Context) (bool, error) {
	for _, table := range []string{"catalog_applied", "stage_checkpoints"} {
		exists, err := d.tableExists(ctx, table)
		if err != nil || !exists {
			return false, err
		}
	}

	return true, nil
}

// ClearProvenance forgets the applied catalog and every stage checkpoint, so
// a database whose content was replaced or rolled back reads as unknown. It
// is a no-op on a database without the provenance tables.
func (d *Database) ClearProvenance(ctx context.Context) error {
	has, err := d.HasProvenance(ctx)
	if err != nil || !has {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear provenance: %w", err)
	}
	defer rollbackUnlessCommitted(tx)
	if err := clearProvenance(ctx, tx); err != nil {
		return err
	}

	return tx.Commit()
}

func clearProvenance(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{`DELETE FROM catalog_applied`, `DELETE FROM stage_checkpoints`} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("clear provenance: %w", err)
		}
	}

	return nil
}
