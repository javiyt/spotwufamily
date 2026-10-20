package filesystem

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// stagingPrefix names the hidden staging directories created next to each
// export root.
const stagingPrefix = ".export-staging-"

// StagedWriter collects an export without touching the published files. Each
// changed file is written into a hidden staging directory next to the export
// root it belongs to (so publishing is a same-filesystem rename) and prunes
// are recorded. Commit publishes the staged files and applies the prunes;
// Discard removes the staging directories. A cancelled or failed export is
// discarded and leaves the previous artifacts byte-identical.
type StagedWriter struct {
	roots   []string
	staging map[string]string // root -> staging dir
	files   []stagedFile
	prunes  []stagedPrune
	done    bool
}

type stagedFile struct{ staged, target string }

type stagedPrune struct {
	dir  string
	keep map[string]struct{}
}

// NewStagedWriter stages writes under roots. It removes staging directories
// left behind by an interrupted earlier run; callers serialize exports (the
// admin export holds the database lock and the export resource).
func NewStagedWriter(roots ...string) (*StagedWriter, error) {
	w := &StagedWriter{staging: map[string]string{}}
	for _, root := range roots {
		w.roots = append(w.roots, filepath.Clean(root))
		stale, err := filepath.Glob(filepath.Join(filepath.Dir(root), stagingPrefix+filepath.Base(root)+"-*"))
		if err != nil {
			return nil, err
		}
		for _, dir := range stale {
			if err := os.RemoveAll(dir); err != nil {
				return nil, fmt.Errorf("remove stale staging directory: %w", err)
			}
		}
	}
	return w, nil
}

// root returns the export root containing path and path relative to it.
func (w *StagedWriter) root(path string) (string, string, error) {
	path = filepath.Clean(path)
	for _, root := range w.roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root, rel, nil
		}
	}
	return "", "", fmt.Errorf("export file %s is outside the export roots", path)
}

// WriteFile stages data for path unless the published file already holds it.
func (w *StagedWriter) WriteFile(path string, data []byte) (bool, error) {
	if w.done {
		return false, fmt.Errorf("staged export already finished")
	}
	root, rel, err := w.root(path)
	if err != nil {
		return false, err
	}
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, data) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read existing %s: %w", path, err)
	}
	dir, ok := w.staging[root]
	if !ok {
		if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
			return false, fmt.Errorf("create parent for %s: %w", root, err)
		}
		dir, err = os.MkdirTemp(filepath.Dir(root), stagingPrefix+filepath.Base(root)+"-*")
		if err != nil {
			return false, fmt.Errorf("create staging directory: %w", err)
		}
		w.staging[root] = dir
	}
	staged := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return false, fmt.Errorf("create staging directory: %w", err)
	}
	if err := os.WriteFile(staged, data, 0o644); err != nil {
		return false, fmt.Errorf("stage %s: %w", path, err)
	}
	w.files = append(w.files, stagedFile{staged: staged, target: path})
	return true, nil
}

// PruneDir records a prune applied on Commit.
func (w *StagedWriter) PruneDir(dir string, keep map[string]struct{}) error {
	if w.done {
		return fmt.Errorf("staged export already finished")
	}
	copied := make(map[string]struct{}, len(keep))
	for k := range keep {
		copied[k] = struct{}{}
	}
	w.prunes = append(w.prunes, stagedPrune{dir: dir, keep: copied})
	return nil
}

// Commit publishes every staged file by rename, then applies the prunes and
// removes the staging directories. It is not cancellable. A failure part way
// (e.g. a full disk) can leave some files published; each file is still
// either its old or its new version.
func (w *StagedWriter) Commit() error {
	if w.done {
		return fmt.Errorf("staged export already finished")
	}
	w.done = true
	defer w.cleanup()
	for _, f := range w.files {
		if err := os.MkdirAll(filepath.Dir(f.target), 0o755); err != nil {
			return fmt.Errorf("create parent for %s: %w", f.target, err)
		}
		if err := os.Rename(f.staged, f.target); err != nil {
			return fmt.Errorf("publish %s: %w", f.target, err)
		}
	}
	for _, p := range w.prunes {
		if err := (Writer{}).PruneDir(p.dir, p.keep); err != nil {
			return err
		}
	}
	return nil
}

// Discard drops everything staged. It is safe to call after Commit.
func (w *StagedWriter) Discard() error {
	w.done = true
	return w.cleanup()
}

func (w *StagedWriter) cleanup() error {
	var firstErr error
	for _, root := range slices.Sorted(maps.Keys(w.staging)) {
		if err := os.RemoveAll(w.staging[root]); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(w.staging, root)
	}
	return firstErr
}

// DiffWriter is a read-only export writer: it records which files an export
// would write or prune, and never touches the disk.
type DiffWriter struct {
	changed []string
}

// NewDiffWriter returns an empty DiffWriter.
func NewDiffWriter() *DiffWriter { return &DiffWriter{} }

// WriteFile records path when its content differs from data.
func (d *DiffWriter) WriteFile(path string, data []byte) (bool, error) {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, data) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read existing %s: %w", path, err)
	}
	d.changed = append(d.changed, path)
	return true, nil
}

// PruneDir records the files Writer.PruneDir would remove.
func (d *DiffWriter) PruneDir(dir string, keep map[string]struct{}) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !prunable(path) {
			return nil
		}
		if _, ok := keep[path]; !ok {
			d.changed = append(d.changed, path)
		}
		return nil
	})
}

// Changed returns the sorted paths that would be written or removed.
func (d *DiffWriter) Changed() []string {
	out := slices.Clone(d.changed)
	slices.Sort(out)
	return slices.Compact(out)
}
