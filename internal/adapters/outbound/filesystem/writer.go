package filesystem

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Writer struct{}

func NewWriter() Writer {
	return Writer{}
}

func (Writer) WriteFile(path string, data []byte) (bool, error) {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, data) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read existing %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create parent for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return false, fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return false, fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return false, fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return false, fmt.Errorf("replace %s: %w", path, err)
	}

	return true, nil
}

func (Writer) PruneDir(dir string, keep map[string]struct{}) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (!strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".md")) {
			return nil
		}
		if _, ok := keep[path]; ok {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove obsolete export %s: %w", path, err)
		}

		return nil
	})
}
