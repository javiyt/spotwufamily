package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
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
	// Releaser supplies the pinned and local content hashes through its
	// read-only preflight. Nil leaves them unknown.
	Releaser maintenance.Releaser
}

// NewFreshnessSource reads the real files on demand and never creates,
// migrates or writes anything. Every method swallows errors into ok=false.
func NewFreshnessSource(cfg FreshnessConfig) maintenance.FreshnessSource {
	return freshnessSource{cfg: cfg}
}

type freshnessSource struct {
	cfg FreshnessConfig
	// release caches one preflight for a Freshness session; nil outside a
	// session, where every call runs its own preflight.
	release *releaseOnce
}

// releaseOnce runs the release preflight at most once.
type releaseOnce struct {
	once sync.Once
	p    maintenance.Prereq
	ok   bool
}

var _ maintenance.FreshnessSession = freshnessSource{}

// Session returns a source that runs the release preflight at most once,
// for the duration of one Freshness call.
func (s freshnessSource) Session() maintenance.FreshnessSource {
	return freshnessSource{cfg: s.cfg, release: &releaseOnce{}}
}

func (s freshnessSource) prereq() (maintenance.Prereq, bool) {
	if s.release == nil {
		return releaseHashes(s.cfg.Releaser)
	}
	s.release.once.Do(func() { s.release.p, s.release.ok = releaseHashes(s.cfg.Releaser) })
	return s.release.p, s.release.ok
}

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

// CatalogDigest is the canonical fingerprint digest of the editorial catalog
// as it is now, comparable with the digest recorded in catalog_applied. It
// never depends on file times.
func (s freshnessSource) CatalogDigest() string {
	current, err := s.currentFingerprint()
	if err != nil {
		return "unreadable"
	}
	return current.Digest
}

func (s freshnessSource) currentFingerprint() (artists.AppliedCatalog, error) {
	c, err := yamlcatalog.NewStore().Load(context.Background(), s.cfg.CatalogPath)
	if err != nil {
		return artists.AppliedCatalog{}, err
	}
	return artists.FingerprintCatalog(c), nil
}

// appliedCatalog reads the recorded provenance without creating or migrating
// anything; ok=false for a missing, unreadable or legacy database.
func (s freshnessSource) appliedCatalog() (artists.AppliedCatalog, bool) {
	database, err := openExisting(s.cfg.DBPath)
	if err != nil {
		return artists.AppliedCatalog{}, false
	}
	defer func() { _ = database.Close() }()
	applied, ok, err := database.AppliedCatalog(context.Background())
	if err != nil || !ok {
		return artists.AppliedCatalog{}, false
	}
	return applied, true
}

// SeededDigest is the digest recorded when the catalog was last applied to
// the database; ok=false when none was recorded or there is no database.
func (s freshnessSource) SeededDigest() (string, bool) {
	applied, ok := s.appliedCatalog()
	if !ok {
		return "", false
	}
	return applied.Digest, true
}

// AppliedChanges diffs the recorded applied catalog against the current one.
func (s freshnessSource) AppliedChanges() ([]artists.ArtistChange, bool) {
	applied, ok := s.appliedCatalog()
	if !ok {
		return nil, false
	}
	current, err := s.currentFingerprint()
	if err != nil {
		// Unloadable catalog: an applied row exists but nothing can be diffed.
		return nil, true
	}
	return artists.DiffCatalog(applied, current), true
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

// PinnedContentSHA is the content hash pinned in data/catalog.release.json,
// from the release preflight; ok=false when the preflight is unavailable or
// there is no valid pinned manifest.
func (s freshnessSource) PinnedContentSHA() (string, bool) {
	p, ok := s.prereq()
	return p.PinnedContentSHA256, ok && p.PinnedContentSHA256 != ""
}

// LocalContentSHA is the local database content hash, from the release
// preflight; ok=false when the preflight is unavailable or there is no
// database.
func (s freshnessSource) LocalContentSHA() (string, bool) {
	p, ok := s.prereq()
	return p.LocalContentSHA256, ok && p.LocalContentSHA256 != ""
}
