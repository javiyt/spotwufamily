package bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

// FreshnessConfig locates the files whose freshness is compared. It mirrors
// MaintenanceActionsConfig.
type FreshnessConfig struct {
	CatalogPath  string
	LedgerPath   string
	DBPath       string
	SnapshotPath string
	// Export roots checked for drift against the database.
	ExportOutputDir  string
	ExportStaticDir  string
	ExportContentDir string
}

// NewFreshnessSource reads the real files on demand and never creates,
// migrates or writes anything. Every method swallows errors into ok=false.
func NewFreshnessSource(cfg FreshnessConfig) maintenance.FreshnessSource {
	return freshnessSource{cfg}
}

type freshnessSource struct{ cfg FreshnessConfig }

var _ maintenance.FreshnessSource = freshnessSource{}

func (s freshnessSource) actionsConfig() MaintenanceActionsConfig {
	return MaintenanceActionsConfig{
		CatalogPath: s.cfg.CatalogPath, LedgerPath: s.cfg.LedgerPath, DBPath: s.cfg.DBPath,
		SnapshotPath:    s.cfg.SnapshotPath,
		ExportOutputDir: s.cfg.ExportOutputDir, ExportStaticDir: s.cfg.ExportStaticDir,
		ExportContentDir: s.cfg.ExportContentDir,
	}
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// CatalogDigest hashes the catalog file content (absent hashes as absent).
func (s freshnessSource) CatalogDigest() string {
	d, err := fileDigest(s.cfg.CatalogPath)(context.Background())
	if err != nil {
		return "unreadable"
	}
	return d
}

// SeededDigest: the database records no catalog digest, so the rule is
// timestamp based. The latest successful or partial sync run read the catalog when it
// started; if the catalog file was not modified after that start, the seeded
// digest is the current catalog digest, otherwise a marker that never equals
// it. The heuristic can read fresh while stale in two narrow cases: an
// mtime-preserving restore of the catalog, and an edit between the sync
// reading the catalog and BeginSyncRun. ok=false when no sync run has completed.
func (s freshnessSource) SeededDigest() (string, bool) {
	database, err := openExisting(s.cfg.DBPath)
	if err != nil {
		return "", false
	}
	defer func() { _ = database.Close() }()
	var started string
	err = database.DB().QueryRowContext(context.Background(),
		`SELECT started_at FROM sync_runs WHERE status IN ('success','partial') ORDER BY id DESC LIMIT 1`).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return "", false
	}
	at, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(s.cfg.CatalogPath)
	if err != nil || info.ModTime().After(at) {
		return "catalog-modified-after-seed", true
	}
	return s.CatalogDigest(), true
}

// SnapshotDigest hashes the decompressed snapshot content.
func (s freshnessSource) SnapshotDigest() (string, bool) {
	data, err := sqliteadapter.ReadSnapshot(s.cfg.SnapshotPath)
	if err != nil {
		return "", false
	}
	return sum(data), true
}

// DatabaseState is Absent when there is no database file, Unreadable when the
// file exists but cannot be opened and dumped.
func (s freshnessSource) DatabaseState() maintenance.DatabaseState {
	if !databaseExists(s.cfg.DBPath) {
		return maintenance.DatabaseAbsent
	}
	if _, ok := s.DatabaseDigest(); !ok {
		return maintenance.DatabaseUnreadable
	}
	return maintenance.DatabaseReadable
}

// DatabaseDigest hashes the deterministic snapshot a fresh dump would produce.
func (s freshnessSource) DatabaseDigest() (string, bool) {
	database, err := openExisting(s.cfg.DBPath)
	if err != nil {
		return "", false
	}
	defer func() { _ = database.Close() }()
	data, err := database.Snapshot(context.Background())
	if err != nil {
		return "", false
	}
	return sum(data), true
}

// ExportDigest returns DatabaseDigest when a fresh export would not change
// any file (checked with the diff writer, nothing is written) and a marker
// that never equals it otherwise (missing files count as changed). ok=false
// when the export cannot be evaluated.
func (s freshnessSource) ExportDigest() (string, bool) {
	db, ok := s.DatabaseDigest()
	if !ok {
		return "", false
	}
	diff := filesystem.NewDiffWriter()
	if _, err := (exportPort{s.actionsConfig()}).Run(context.Background(), diff); err != nil {
		return "", false
	}
	if len(diff.Changed()) > 0 {
		return "export-differs", true
	}
	return db, true
}

// PinnedContentSHA and LocalContentSHA come from the release script's
// preflight (data/catalog.release.json vs the local content hash), which is
// delivered in Task 12/17. Until then they are unknown.
func (freshnessSource) PinnedContentSHA() (string, bool) { return "", false }
func (freshnessSource) LocalContentSHA() (string, bool)  { return "", false }
