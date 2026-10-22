package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

const (
	snapshotSourceBaseline  = "baseline"
	snapshotSourceSeed      = "seed"
	snapshotSourceReconcile = "reconcile"
	invalidatedPrefix       = "invalidated@"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (d *Database) tableExists(ctx context.Context, name string) (bool, error) {
	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}

	return count > 0, nil
}

// Impact reads the reconciliation state of one artist without writing. It
// tolerates databases that have not applied the reconciliation migration.
func (d *Database) Impact(ctx context.Context, slug string) (artists.ImpactState, error) {
	var state artists.ImpactState

	baseline, err := d.mappingBaseline(ctx, slug)
	if err != nil {
		return state, err
	}
	state.Baseline = baseline

	rows, err := d.db.QueryContext(ctx, `
SELECT sync_run_id, status, spotify_ids, COALESCE(finished_at, '')
FROM sync_run_artists
WHERE artist_slug = ? AND status = 'completed'
ORDER BY sync_run_id`, slug)
	if err != nil {
		return state, fmt.Errorf("read sync checkpoints of %s: %w", slug, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cp artists.Checkpoint
		if err := rows.Scan(&cp.RunID, &cp.Status, &cp.SpotifyIDs, &cp.FinishedAt); err != nil {
			return state, fmt.Errorf("scan sync checkpoint of %s: %w", slug, err)
		}
		state.Checkpoints = append(state.Checkpoints, cp)
	}
	if err := rows.Err(); err != nil {
		return state, fmt.Errorf("read sync checkpoints of %s: %w", slug, err)
	}

	var refresh artists.MetadataRefresh
	err = d.db.QueryRowContext(ctx, `SELECT refreshed_at, spotify_ids FROM artist_metadata_refreshes WHERE artist_slug = ?`, slug).
		Scan(&refresh.RefreshedAt, &refresh.SpotifyIDs)
	switch {
	case err == nil:
		state.MetadataRefresh = &refresh
	case err != sql.ErrNoRows:
		return state, fmt.Errorf("read metadata refresh of %s: %w", slug, err)
	}

	row, err := d.reconciliationRow(ctx, slug)
	if err != nil {
		return state, err
	}
	state.Reconciliation = row

	return state, nil
}

func (d *Database) mappingBaseline(ctx context.Context, slug string) (artists.MappingBaseline, error) {
	exists, err := d.tableExists(ctx, "artist_mapping_snapshots")
	if err != nil {
		return artists.MappingBaseline{}, err
	}
	if exists {
		var raw, checksum string
		err := d.db.QueryRowContext(ctx, `SELECT spotify_ids, checksum FROM artist_mapping_snapshots WHERE artist_slug = ?`, slug).Scan(&raw, &checksum)
		switch {
		case err == nil:
			return artists.MappingBaseline{SpotifyIDs: decodeIDs(raw), Source: artists.BaselineSnapshot, Checksum: checksum}, nil
		case err != sql.ErrNoRows:
			return artists.MappingBaseline{}, fmt.Errorf("read mapping snapshot of %s: %w", slug, err)
		}
	}

	ids, err := queryIDs(ctx, d.db, slug)
	if err != nil {
		return artists.MappingBaseline{}, err
	}
	if len(ids) > 0 {
		return artists.MappingBaseline{SpotifyIDs: ids, Source: artists.BaselineConfigured}, nil
	}

	return artists.MappingBaseline{Source: artists.BaselineNone}, nil
}

func queryIDs(ctx context.Context, db execer, slug string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT spotify_id FROM configured_artist_spotify_ids WHERE artist_slug = ? ORDER BY position`, slug)
	if err != nil {
		return nil, fmt.Errorf("read configured Spotify IDs of %s: %w", slug, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan configured Spotify ID of %s: %w", slug, err)
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

const reconciliationColumns = `artist_slug, status, previous_ids, target_ids, plan_checksum, resync_fingerprint, retired_at,
  backup_path, backup_checksum, snapshot_checksum_before, detected_at, updated_at, last_error`

func scanReconciliation(scan func(...any) error) (artists.PendingReconciliation, error) {
	var row artists.PendingReconciliation
	var previous, target string
	err := scan(&row.Slug, &row.Status, &previous, &target, &row.PlanChecksum, &row.ResyncFingerprint, &row.RetiredAt,
		&row.BackupPath, &row.BackupChecksum, &row.SnapshotChecksumBefore, &row.DetectedAt, &row.UpdatedAt, &row.LastError)
	row.PreviousIDs = decodeIDs(previous)
	row.TargetIDs = decodeIDs(target)

	return row, err
}

func (d *Database) reconciliationRow(ctx context.Context, slug string) (*artists.PendingReconciliation, error) {
	exists, err := d.tableExists(ctx, "artist_reconciliations")
	if err != nil || !exists {
		return nil, err
	}
	row, err := scanReconciliation(d.db.QueryRowContext(ctx, `SELECT `+reconciliationColumns+` FROM artist_reconciliations WHERE artist_slug = ?`, slug).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read reconciliation of %s: %w", slug, err)
	}

	return &row, nil
}

// PendingReconciliations lists journal rows whose consistency is not restored yet.
func (d *Database) PendingReconciliations(ctx context.Context) ([]artists.PendingReconciliation, error) {
	exists, err := d.tableExists(ctx, "artist_reconciliations")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+reconciliationColumns+` FROM artist_reconciliations WHERE status <> 'completed' ORDER BY artist_slug`)
	if err != nil {
		return nil, fmt.Errorf("read pending reconciliations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []artists.PendingReconciliation
	for rows.Next() {
		row, err := scanReconciliation(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan pending reconciliation: %w", err)
		}
		out = append(out, row)
	}

	return out, rows.Err()
}

// RecordReconciliationError remembers why a replacement sync stopped (rate
// limit, Spotify failure) on the open reconciliation of that artist. It is a
// no-op when the artist has none.
func (d *Database) RecordReconciliationError(ctx context.Context, slug, message string) error {
	exists, err := d.tableExists(ctx, "artist_reconciliations")
	if err != nil || !exists {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `
UPDATE artist_reconciliations SET last_error = ?, updated_at = ?
WHERE artist_slug = ? AND status <> 'completed'`, message, time.Now().UTC().Format(time.RFC3339), slug); err != nil {
		return fmt.Errorf("record reconciliation error of %s: %w", slug, err)
	}

	return nil
}

// BackupDatabase writes a consistent copy of the database plus a manifest
// holding the reviewed plan and content checksums. It refuses to replace
// existing files and never touches GitHub Release assets.
func (d *Database) BackupDatabase(ctx context.Context, request artists.BackupRequest) (artists.BackupInfo, error) {
	var info artists.BackupInfo
	if err := d.withContext(ctx); err != nil {
		return info, err
	}
	if err := os.MkdirAll(request.Dir, 0o755); err != nil {
		return info, fmt.Errorf("create backup directory: %w", err)
	}
	info.Path = filepath.Join(request.Dir, request.Name+".db")
	info.ManifestPath = filepath.Join(request.Dir, request.Name+".manifest.json")
	for _, path := range []string{info.Path, info.ManifestPath} {
		if _, err := os.Stat(path); err == nil {
			return info, fmt.Errorf("backup %s already exists; refusing to overwrite", path)
		} else if !os.IsNotExist(err) {
			return info, fmt.Errorf("stat backup %s: %w", path, err)
		}
	}

	snapshot, err := d.Snapshot(ctx)
	if err != nil {
		return info, err
	}
	sum := sha256.Sum256(snapshot)
	info.SnapshotChecksum = hex.EncodeToString(sum[:])

	if _, err := d.db.ExecContext(ctx, `VACUUM INTO ?`, info.Path); err != nil {
		_ = os.Remove(info.Path)
		return info, fmt.Errorf("copy database to backup: %w", err)
	}
	info.SHA256, err = fileSHA256(info.Path)
	if err != nil {
		return info, err
	}

	manifest, err := json.MarshalIndent(struct {
		DatabaseSHA256 string          `json:"database_sha256"`
		SnapshotSHA256 string          `json:"snapshot_sha256"`
		Plan           json.RawMessage `json:"plan"`
	}{info.SHA256, info.SnapshotChecksum, request.Manifest}, "", "  ")
	if err != nil {
		return info, fmt.Errorf("encode backup manifest: %w", err)
	}
	file, err := os.OpenFile(info.ManifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return info, fmt.Errorf("create backup manifest: %w", err)
	}
	if _, err := file.Write(append(manifest, '\n')); err != nil {
		_ = file.Close()
		return info, fmt.Errorf("write backup manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return info, fmt.Errorf("close backup manifest: %w", err)
	}

	return info, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ApplyReconciliation is atomic: either every step lands or none does.
func (d *Database) ApplyReconciliation(ctx context.Context, req artists.ApplyRequest) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reconciliation: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	for _, id := range req.AlbumIDs {
		if _, err := tx.ExecContext(ctx, `
UPDATE artist_albums SET active = 0, missing_since = ?
WHERE configured_artist_slug = ? AND album_id = ? AND active = 1`, req.Token, req.Slug, id); err != nil {
			return fmt.Errorf("retire album association %s: %w", id, err)
		}
	}
	for _, id := range req.TrackIDs {
		if _, err := tx.ExecContext(ctx, `
UPDATE artist_tracks SET active = 0, missing_since = ?
WHERE configured_artist_slug = ? AND track_id = ? AND active = 1`, req.Token, req.Slug, id); err != nil {
			return fmt.Errorf("retire track association %s: %w", id, err)
		}
	}

	// Checkpoints tied to other IDs stay in history but can no longer be
	// resumed from or counted as fresh.
	if _, err := tx.ExecContext(ctx, `
UPDATE sync_run_artists SET status = ?
WHERE artist_slug = ? AND status = 'completed' AND spotify_ids <> ?`, invalidatedPrefix+req.Token, req.Slug, req.Fingerprint); err != nil {
		return fmt.Errorf("invalidate sync checkpoints: %w", err)
	}

	invalidated := ""
	var refreshedAt, refreshIDs string
	err = tx.QueryRowContext(ctx, `SELECT refreshed_at, spotify_ids FROM artist_metadata_refreshes WHERE artist_slug = ?`, req.Slug).Scan(&refreshedAt, &refreshIDs)
	switch {
	case err == nil && refreshIDs != req.CurrentRefreshIDs:
		data, _ := json.Marshal(artists.MetadataRefresh{RefreshedAt: refreshedAt, SpotifyIDs: refreshIDs})
		invalidated = string(data)
		if _, err := tx.ExecContext(ctx, `DELETE FROM artist_metadata_refreshes WHERE artist_slug = ?`, req.Slug); err != nil {
			return fmt.Errorf("invalidate metadata refresh: %w", err)
		}
	case err != nil && err != sql.ErrNoRows:
		return fmt.Errorf("read metadata refresh: %w", err)
	}

	var configured int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM configured_artists WHERE slug = ?`, req.Slug).Scan(&configured); err != nil {
		return fmt.Errorf("check configured artist %s: %w", req.Slug, err)
	}
	if configured > 0 {
		status := artists.StatusCompleted
		if req.ResyncFingerprint != "" {
			status = artists.StatusAwaitingResync
		} else if err := upsertSnapshot(ctx, tx, req.Slug, req.TargetIDs, snapshotSourceReconcile, req.Token); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO artist_reconciliations(artist_slug, status, previous_ids, target_ids, plan_checksum, resync_fingerprint, retired_at,
  backup_path, backup_checksum, snapshot_checksum_before, invalidated, detected_at, updated_at, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(artist_slug) DO UPDATE SET
  status = excluded.status,
  previous_ids = excluded.previous_ids,
  target_ids = excluded.target_ids,
  plan_checksum = excluded.plan_checksum,
  resync_fingerprint = excluded.resync_fingerprint,
  retired_at = excluded.retired_at,
  backup_path = excluded.backup_path,
  backup_checksum = excluded.backup_checksum,
  snapshot_checksum_before = excluded.snapshot_checksum_before,
  invalidated = excluded.invalidated,
  updated_at = excluded.updated_at,
  last_error = ''`,
			req.Slug, status, encodeIDs(req.PreviousIDs), encodeIDs(req.TargetIDs), req.PlanChecksum, req.ResyncFingerprint, req.Token,
			req.Backup.Path, req.Backup.SHA256, req.Backup.SnapshotChecksum, invalidated, req.Token, req.Token,
		); err != nil {
			return fmt.Errorf("record reconciliation: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reconciliation: %w", err)
	}

	return nil
}

// RestoreAssociations reverts ApplyReconciliation identified by its token:
// associations, invalidated checkpoints and the metadata cache come back and
// the artist returns to the drifted state until it is reconciled again.
func (d *Database) RestoreAssociations(ctx context.Context, slug, retiredAt string) (int, int, error) {
	hasProvenance, err := d.HasProvenance(ctx)
	if err != nil {
		return 0, 0, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin restore associations: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	albums, err := execChanged(ctx, tx, `
UPDATE artist_albums SET active = 1, missing_since = NULL
WHERE configured_artist_slug = ? AND active = 0 AND missing_since = ?`, slug, retiredAt)
	if err != nil {
		return 0, 0, fmt.Errorf("restore album associations: %w", err)
	}
	tracks, err := execChanged(ctx, tx, `
UPDATE artist_tracks SET active = 1, missing_since = NULL
WHERE configured_artist_slug = ? AND active = 0 AND missing_since = ?`, slug, retiredAt)
	if err != nil {
		return 0, 0, fmt.Errorf("restore track associations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sync_run_artists SET status = 'completed' WHERE artist_slug = ? AND status = ?`, slug, invalidatedPrefix+retiredAt); err != nil {
		return 0, 0, fmt.Errorf("restore sync checkpoints: %w", err)
	}

	var previous, invalidated string
	err = tx.QueryRowContext(ctx, `SELECT previous_ids, invalidated FROM artist_reconciliations WHERE artist_slug = ? AND retired_at = ?`, slug, retiredAt).Scan(&previous, &invalidated)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return 0, 0, fmt.Errorf("read reconciliation of %s: %w", slug, err)
	default:
		if invalidated != "" {
			var refresh artists.MetadataRefresh
			if err := json.Unmarshal([]byte(invalidated), &refresh); err != nil {
				return 0, 0, fmt.Errorf("decode invalidated metadata refresh: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO artist_metadata_refreshes(artist_slug, refreshed_at, spotify_ids) VALUES (?, ?, ?)
ON CONFLICT(artist_slug) DO UPDATE SET refreshed_at = excluded.refreshed_at, spotify_ids = excluded.spotify_ids`,
				slug, refresh.RefreshedAt, refresh.SpotifyIDs); err != nil {
				return 0, 0, fmt.Errorf("restore metadata refresh: %w", err)
			}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if err := upsertSnapshot(ctx, tx, slug, decodeIDs(previous), snapshotSourceReconcile, now); err != nil {
			return 0, 0, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE artist_reconciliations
SET status = 'drifted', retired_at = '', resync_fingerprint = '', invalidated = '', last_error = '', updated_at = ?
WHERE artist_slug = ?`, now, slug); err != nil {
			return 0, 0, fmt.Errorf("reopen reconciliation of %s: %w", slug, err)
		}
	}
	// The restored associations no longer match what was recorded as
	// applied: the database reads as unknown until it is checked again.
	if hasProvenance {
		if err := clearProvenance(ctx, tx); err != nil {
			return 0, 0, err
		}
	}

	return albums, tracks, tx.Commit()
}

// noteMappingDrift records the first baseline of an artist and keeps the
// journal aligned with the YAML mapping being seeded. It must run before
// configured_artist_spotify_ids is rewritten.
func noteMappingDrift(ctx context.Context, tx *sql.Tx, slug string, current []string, now string) error {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT spotify_ids FROM artist_mapping_snapshots WHERE artist_slug = ?`, slug).Scan(&raw)
	var baseline []string
	switch {
	case err == sql.ErrNoRows:
		previous, err := queryIDs(ctx, tx, slug)
		if err != nil {
			return err
		}
		source := snapshotSourceBaseline
		baseline = previous
		if len(previous) == 0 {
			source, baseline = snapshotSourceSeed, current
		}
		if err := upsertSnapshot(ctx, tx, slug, baseline, source, now); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("read mapping snapshot of %s: %w", slug, err)
	default:
		baseline = decodeIDs(raw)
	}

	var status, target string
	err = tx.QueryRowContext(ctx, `SELECT status, target_ids FROM artist_reconciliations WHERE artist_slug = ?`, slug).Scan(&status, &target)
	hasRow := err == nil
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read reconciliation of %s: %w", slug, err)
	}
	pending := hasRow && status != artists.StatusCompleted

	if sameIDSet(baseline, current) {
		if pending && status == artists.StatusDrifted {
			if _, err := tx.ExecContext(ctx, `DELETE FROM artist_reconciliations WHERE artist_slug = ?`, slug); err != nil {
				return fmt.Errorf("clear reconciliation of %s: %w", slug, err)
			}
		}
		return nil
	}
	if pending && sameIDSet(decodeIDs(target), current) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO artist_reconciliations(artist_slug, status, previous_ids, target_ids, detected_at, updated_at)
VALUES (?, 'drifted', ?, ?, ?, ?)
ON CONFLICT(artist_slug) DO UPDATE SET
  status = 'drifted',
  previous_ids = excluded.previous_ids,
  target_ids = excluded.target_ids,
  plan_checksum = '',
  resync_fingerprint = '',
  retired_at = '',
  invalidated = '',
  last_error = '',
  detected_at = excluded.detected_at,
  updated_at = excluded.updated_at`,
		slug, encodeIDs(baseline), encodeIDs(current), now, now)
	if err != nil {
		return fmt.Errorf("record mapping drift of %s: %w", slug, err)
	}

	return nil
}

// completeResync finishes an awaiting reconciliation once the replacement IDs
// were synced, advancing the last-synced snapshot. It runs in the checkpoint's
// transaction so the two cannot disagree.
func completeResync(ctx context.Context, tx *sql.Tx, slug, fingerprint, at string) error {
	var target, wanted string
	err := tx.QueryRowContext(ctx, `
SELECT target_ids, resync_fingerprint FROM artist_reconciliations
WHERE artist_slug = ? AND status = 'awaiting_resync'`, slug).Scan(&target, &wanted)
	if err == sql.ErrNoRows || (err == nil && wanted != fingerprint) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read awaiting reconciliation of %s: %w", slug, err)
	}
	if err := upsertSnapshot(ctx, tx, slug, decodeIDs(target), snapshotSourceReconcile, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE artist_reconciliations SET status = 'completed', last_error = '', updated_at = ? WHERE artist_slug = ?`, at, slug); err != nil {
		return fmt.Errorf("complete reconciliation of %s: %w", slug, err)
	}

	return nil
}

func upsertSnapshot(ctx context.Context, db execer, slug string, ids []string, source, at string) error {
	if ids == nil {
		ids = []string{}
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO artist_mapping_snapshots(artist_slug, spotify_ids, checksum, recorded_at, source)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(artist_slug) DO UPDATE SET
  spotify_ids = excluded.spotify_ids,
  checksum = excluded.checksum,
  recorded_at = excluded.recorded_at,
  source = excluded.source`,
		slug, encodeIDs(ids), idsChecksum(ids), at, source); err != nil {
		return fmt.Errorf("record mapping snapshot of %s: %w", slug, err)
	}

	return nil
}

func encodeIDs(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	data, _ := json.Marshal(ids)

	return string(data)
}

func decodeIDs(raw string) []string {
	var ids []string
	if raw == "" || json.Unmarshal([]byte(raw), &ids) != nil {
		return nil
	}

	return ids
}

func idsChecksum(ids []string) string {
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(encodeIDs(sorted)))

	return hex.EncodeToString(sum[:])
}

func sameIDSet(a, b []string) bool {
	return idsChecksum(a) == idsChecksum(b)
}
