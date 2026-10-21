package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// Resource keys shared by maintenance jobs.
const (
	ResourceCatalogYAML = "catalog-yaml"
	ResourceDatabase    = "database"
)

// spotifyUnavailableReason is shown when an action needs Spotify credentials.
const spotifyUnavailableReason = "Spotify credentials are not configured"

// ErrLocked reports that another process holds a resource; Safe maps it to a
// conflict. Locker implementations wrap it.
var ErrLocked = errors.New("resource is locked by another operation")

// CatalogDigest returns a digest of the editorial inputs (catalog and
// candidate ledger) so previews can detect that they changed.
type CatalogDigest func(ctx context.Context) (string, error)

// CredentialsChecker reports whether Spotify credentials are configured.
type CredentialsChecker func() bool

// Locker takes a cross-process lock without waiting. It returns an error
// wrapping ErrLocked when another operation holds it.
type Locker interface {
	TryLock() (release func(), err error)
}

// Backuper writes a verified backup of the database.
type Backuper interface {
	Backup(ctx context.Context) error
}

// CatalogBackuper writes a timestamped copy of the catalog YAML to the admin
// backup folder. Jobs that rewrite the catalog call it first: the store's
// .bak only keeps the state before the last save.
type CatalogBackuper interface {
	BackupCatalog(ctx context.Context) error
}

// errNoCatalogBackup fails a catalog-writing job closed when no backuper is
// wired.
var errNoCatalogBackup = errors.New("catalog backup is not configured")

// backupCatalog takes the catalog backup, failing closed without a backuper.
func backupCatalog(ctx context.Context, b CatalogBackuper) error {
	if b == nil {
		return errNoCatalogBackup
	}
	return b.BackupCatalog(ctx)
}

// catalogBackupNote describes the catalog backup in previews; it names no path.
const catalogBackupNote = "a timestamped copy of the catalog is written to the admin backup folder first (.bak keeps only the last save)"

// fingerprint hashes parts unambiguously.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(lp(p)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

var errInvalidBool = &SafeError{Code: "validation", Message: "Boolean parameters must be \"true\" or \"false\"."}

// validBool accepts an absent/empty value, "true" or "false".
func validBool(p Params, key string) error {
	switch p[key] {
	case "", "true", "false":
		return nil
	}
	return errInvalidBool
}

func boolParam(p Params, key string) bool { return p[key] == "true" }

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func validSlug(s string) bool { return len(s) <= 100 && slugPattern.MatchString(s) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// joinLimited joins up to limit values and marks that more were left out.
func joinLimited(values []string, limit int) string {
	if len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:limit], ", ") + ", …"
}
