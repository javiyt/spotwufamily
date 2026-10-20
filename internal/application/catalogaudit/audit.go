// Package catalogaudit holds the read-only repository checks shared by the
// CLI audit and the admin audit: snapshot freshness and the presence of the
// export artifacts. The CLI additionally checks the generated git diff and
// builds the site; those stay CLI/CI-only.
package catalogaudit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// SnapshotSource generates the snapshot the database would write now.
type SnapshotSource interface {
	Snapshot(ctx context.Context) ([]byte, error)
}

// ReadSnapshotFunc reads (and decompresses) the snapshot file at path.
type ReadSnapshotFunc func(path string) ([]byte, error)

// StatFunc stats a path, like os.Stat.
type StatFunc func(path string) (fs.FileInfo, error)

// ErrSnapshotStale reports that the snapshot file differs from the database.
var ErrSnapshotStale = errors.New("snapshot is out of date")

// CheckSnapshot compares the snapshot file at path with the snapshot the
// database generates now.
func CheckSnapshot(ctx context.Context, db SnapshotSource, read ReadSnapshotFunc, path string) error {
	generated, err := db.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("generate snapshot: %w", err)
	}
	current, err := read(path)
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", path, err)
	}
	if !bytes.Equal(generated, current) {
		return fmt.Errorf("%w: %s", ErrSnapshotStale, path)
	}
	return nil
}

// Artifact is an export output the site needs. Label names it without the
// directories above the export roots.
type Artifact struct {
	Path  string
	Label string
	Dir   bool
}

// ExportArtifacts lists the export outputs that must exist after an export.
func ExportArtifacts(outputDir, staticDir string) []Artifact {
	artifact := func(root string, parts ...string) Artifact {
		rel := filepath.Join(parts...)
		return Artifact{
			Path:  filepath.Join(root, rel),
			Label: filepath.ToSlash(filepath.Join(filepath.Base(root), rel)),
		}
	}
	return []Artifact{
		artifact(outputDir, "catalog-summary.json"),
		artifact(outputDir, "artists", "index.json"),
		artifact(outputDir, "albums", "index.json"),
		artifact(outputDir, "tracks", "index.json"),
		artifact(staticDir, "search-index.json"),
	}
}

// CheckExportArtifacts returns the first artifact that is missing or of the
// wrong kind.
func CheckExportArtifacts(stat StatFunc, artifacts []Artifact) error {
	for _, item := range artifacts {
		if err := checkArtifact(stat, item); err != nil {
			return err
		}
	}
	return nil
}

// wrongKindError reports an artifact that is a file where a directory is
// expected, or the other way round.
type wrongKindError struct{ msg string }

func (e *wrongKindError) Error() string { return e.msg }

func checkArtifact(stat StatFunc, item Artifact) error {
	info, err := stat(item.Path)
	if err != nil {
		return fmt.Errorf("missing export artifact %s: %w", item.Path, err)
	}
	if item.Dir && !info.IsDir() {
		return &wrongKindError{"export artifact is not a directory: " + item.Path}
	}
	if !item.Dir && info.IsDir() {
		return &wrongKindError{"export artifact is a directory: " + item.Path}
	}
	return nil
}

// Check statuses.
const (
	StatusOK      = "ok"
	StatusStale   = "stale"
	StatusMissing = "missing"
	StatusInvalid = "invalid"
	StatusError   = "error"
)

// Check is one audit result. Detail never contains absolute paths or raw
// errors.
type Check struct {
	Name   string
	Status string
	Detail string
}

// Report lists the checks sorted by name.
type Report struct {
	Checks []Check
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Status != StatusOK {
			return false
		}
	}
	return true
}

// Failed counts the checks that did not pass.
func (r Report) Failed() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status != StatusOK {
			n++
		}
	}
	return n
}

// Options are the ports and locations Run audits.
type Options struct {
	Database     SnapshotSource
	ReadSnapshot ReadSnapshotFunc
	SnapshotPath string
	Stat         StatFunc
	OutputDir    string
	StaticDir    string
	// ExportDrift, when set, returns the labels of the export files that
	// differ from what an export of the database would write now.
	ExportDrift func(ctx context.Context) ([]string, error)
}

// driftListLimit bounds the file labels listed in the export-fresh detail.
const driftListLimit = 5

// Run performs every check without writing anything. Failed checks are
// reported in the Report; only cancellation returns an error.
func Run(ctx context.Context, o Options) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	var checks []Check
	checks = append(checks, snapshotCheck(ctx, o))
	for _, item := range ExportArtifacts(o.OutputDir, o.StaticDir) {
		checks = append(checks, artifactCheck(o.Stat, item))
	}
	if o.ExportDrift != nil {
		checks = append(checks, driftCheck(ctx, o.ExportDrift))
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	slices.SortFunc(checks, func(a, b Check) int { return strings.Compare(a.Name, b.Name) })
	return Report{Checks: checks}, nil
}

func snapshotCheck(ctx context.Context, o Options) Check {
	name := filepath.Base(o.SnapshotPath)
	c := Check{Name: "snapshot"}
	err := CheckSnapshot(ctx, o.Database, o.ReadSnapshot, o.SnapshotPath)
	switch {
	case err == nil:
		c.Status, c.Detail = StatusOK, name+" matches the database"
	case errors.Is(err, ErrSnapshotStale):
		c.Status, c.Detail = StatusStale, name+" does not match the database; run db-snapshot"
	case errors.Is(err, fs.ErrNotExist):
		c.Status, c.Detail = StatusMissing, name+" not found; run db-snapshot"
	default:
		c.Status, c.Detail = StatusError, name+" could not be compared with the database"
	}
	return c
}

func artifactCheck(stat StatFunc, item Artifact) Check {
	c := Check{Name: "export-artifact " + item.Label}
	err := checkArtifact(stat, item)
	var wrongKind *wrongKindError
	switch {
	case err == nil:
		c.Status, c.Detail = StatusOK, "present"
	case errors.As(err, &wrongKind):
		kind := "a file"
		if item.Dir {
			kind = "a directory"
		}
		c.Status, c.Detail = StatusInvalid, "expected "+kind
	case errors.Is(err, fs.ErrNotExist):
		c.Status, c.Detail = StatusMissing, "not found; run export"
	default:
		c.Status, c.Detail = StatusError, "could not be checked"
	}
	return c
}

func driftCheck(ctx context.Context, drift func(context.Context) ([]string, error)) Check {
	c := Check{Name: "export-fresh"}
	files, err := drift(ctx)
	switch {
	case err != nil:
		c.Status, c.Detail = StatusError, "the export could not be compared with the database"
	case len(files) == 0:
		c.Status, c.Detail = StatusOK, "matches the database"
	default:
		files = slices.Clone(files)
		slices.Sort(files)
		listed := files
		if len(listed) > driftListLimit {
			listed = listed[:driftListLimit]
		}
		list := strings.Join(listed, ", ")
		if len(files) > driftListLimit {
			list += ", …"
		}
		noun := "files differ"
		if len(files) == 1 {
			noun = "file differs"
		}
		c.Status = StatusStale
		c.Detail = strconv.Itoa(len(files)) + " " + noun + " from the database (" + list + "); run export"
	}
	return c
}
