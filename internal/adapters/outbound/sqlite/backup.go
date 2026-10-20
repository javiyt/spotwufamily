package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const backupTimeLayout = "20060102T150405Z"

// backupNow is the clock used to name backups; tests replace it.
var backupNow = time.Now

// BackupTo writes a consistent copy of the database to
// <dir>/<base>-<UTC yyyymmddThhmmssZ>.db with mode 0600, verifies the copy and
// returns its file name. It never replaces an existing file.
func (d *Database) BackupTo(ctx context.Context, dir string) (string, error) {
	if err := d.withContext(ctx); err != nil {
		return "", err
	}
	if d.path == "" {
		return "", errors.New("backup database: source path is unknown")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}

	base := strings.TrimSuffix(filepath.Base(d.path), filepath.Ext(d.path))
	name := base + "-" + backupNow().UTC().Format(backupTimeLayout) + ".db"
	path := filepath.Join(dir, name)

	// Creating the empty target exclusively reserves the name and fixes its
	// mode; VACUUM INTO accepts an existing empty file.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("backup %s already exists; refusing to overwrite", name)
		}
		return "", fmt.Errorf("create backup %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close backup %s: %w", name, err)
	}

	if _, err := d.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("copy database to backup: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("restrict backup %s: %w", name, err)
	}
	if err := verifyBackup(ctx, path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("verify backup %s: %w", name, err)
	}

	return name, nil
}

// verifyBackup runs the regular Verify checks against a read-only handle on
// the copy.
func verifyBackup(ctx context.Context, path string) error {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		return err
	}
	db, err := sql.Open(DriverName, "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()

	_, err = (&Database{db: db, path: path}).Verify(ctx, migrations)
	return err
}
