package filesystem

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const fileBackupTimeLayout = "20060102T150405Z"

// maxFileBackupAttempts bounds the suffixes tried for one second's backups.
const maxFileBackupAttempts = 100

// BackupFile copies src to <dir>/<base>-<UTC yyyymmddThhmmssZ><ext> (with a
// -2, -3... suffix when backups share a second), mode 0600, creating dir
// with mode 0700. It never replaces an existing file and returns the backup's
// file name (no directory). A failed copy leaves no file behind.
func BackupFile(src, dir string, at time.Time) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open %s for backup: %w", filepath.Base(src), err)
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	ext := filepath.Ext(src)
	stamp := strings.TrimSuffix(filepath.Base(src), ext) + "-" + at.UTC().Format(fileBackupTimeLayout)

	var (
		name, path string
		out        *os.File
	)
	for attempt := 1; attempt <= maxFileBackupAttempts; attempt++ {
		name = stamp + ext
		if attempt > 1 {
			name = fmt.Sprintf("%s-%d%s", stamp, attempt, ext)
		}
		path = filepath.Join(dir, name)
		out, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("backup %s already exists; refusing to overwrite", name)
		}
		return "", fmt.Errorf("create backup %s: %w", name, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write backup %s: %w", name, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sync backup %s: %w", name, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close backup %s: %w", name, err)
	}
	return name, nil
}
