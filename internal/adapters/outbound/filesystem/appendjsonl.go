package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
)

// appendJSONLine appends one newline-terminated record to path (0600 file,
// 0700 dir, O_APPEND, fsync). If the file's last byte is not '\n' (a crash
// left a fragment) a leading '\n' is written first so the new record is never
// glued onto the fragment. Callers hold their own mutex.
func appendJSONLine(path string, line []byte, label string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s dir: %w", label, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", label, err)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", label, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", label, err)
	}
	return f.Close()
}
