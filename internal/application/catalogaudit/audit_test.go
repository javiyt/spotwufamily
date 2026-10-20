package catalogaudit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/catalogaudit"
)

type fakeDB struct {
	snapshot []byte
	err      error
}

func (f fakeDB) Snapshot(context.Context) ([]byte, error) { return f.snapshot, f.err }

func readFrom(files map[string][]byte) catalogaudit.ReadSnapshotFunc {
	return func(path string) ([]byte, error) {
		data, ok := files[path]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		return data, nil
	}
}

type fakeInfo struct{ dir bool }

func (i fakeInfo) Name() string       { return "x" }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return 0 }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.dir }
func (i fakeInfo) Sys() any           { return nil }

// statFrom answers Stat from a set of existing paths (true = directory).
func statFrom(paths map[string]bool) catalogaudit.StatFunc {
	return func(path string) (fs.FileInfo, error) {
		dir, ok := paths[path]
		if !ok {
			return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
		}
		return fakeInfo{dir: dir}, nil
	}
}

const (
	snapshotPath = "/abs/data/catalog.snapshot.sql.gz"
	outputDir    = "/abs/site/data/generated"
	staticDir    = "/abs/site/static"
)

func allArtifacts() map[string]bool {
	paths := map[string]bool{}
	for _, a := range catalogaudit.ExportArtifacts(outputDir, staticDir) {
		paths[a.Path] = false
	}
	return paths
}

func options(db fakeDB, snapshots map[string][]byte, stat map[string]bool) catalogaudit.Options {
	return catalogaudit.Options{
		Database:     db,
		ReadSnapshot: readFrom(snapshots),
		SnapshotPath: snapshotPath,
		Stat:         statFrom(stat),
		OutputDir:    outputDir,
		StaticDir:    staticDir,
	}
}

func findCheck(t *testing.T, r catalogaudit.Report, name string) catalogaudit.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not in %+v", name, r.Checks)
	return catalogaudit.Check{}
}

func TestAuditDetectsStaleSnapshot(t *testing.T) {
	r, err := catalogaudit.Run(context.Background(), options(
		fakeDB{snapshot: []byte("db")},
		map[string][]byte{snapshotPath: []byte("old")},
		allArtifacts(),
	))
	if err != nil {
		t.Fatal(err)
	}
	if r.OK() {
		t.Fatal("stale snapshot must fail the audit")
	}
	c := findCheck(t, r, "snapshot")
	if c.Status != catalogaudit.StatusStale || !strings.Contains(c.Detail, "catalog.snapshot.sql.gz") {
		t.Fatalf("snapshot check %+v", c)
	}
	for _, c := range r.Checks {
		if strings.Contains(c.Detail, "/abs") {
			t.Fatalf("detail leaks an absolute path: %+v", c)
		}
	}

	fresh, err := catalogaudit.Run(context.Background(), options(
		fakeDB{snapshot: []byte("db")},
		map[string][]byte{snapshotPath: []byte("db")},
		allArtifacts(),
	))
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.OK() || findCheck(t, fresh, "snapshot").Status != catalogaudit.StatusOK {
		t.Fatalf("fresh audit %+v", fresh)
	}
}

func TestAuditReportsMissingSnapshotAndArtifacts(t *testing.T) {
	stat := allArtifacts()
	delete(stat, filepath.Join(outputDir, "albums", "index.json"))
	stat[filepath.Join(staticDir, "search-index.json")] = true // a directory
	r, err := catalogaudit.Run(context.Background(), options(fakeDB{snapshot: []byte("db")}, nil, stat))
	if err != nil {
		t.Fatal(err)
	}
	if got := findCheck(t, r, "snapshot").Status; got != catalogaudit.StatusMissing {
		t.Fatalf("snapshot status %q", got)
	}
	if got := findCheck(t, r, "export-artifact generated/albums/index.json").Status; got != catalogaudit.StatusMissing {
		t.Fatalf("albums status %q", got)
	}
	if got := findCheck(t, r, "export-artifact static/search-index.json").Status; got != catalogaudit.StatusInvalid {
		t.Fatalf("search index status %q", got)
	}
	if got := findCheck(t, r, "export-artifact generated/artists/index.json").Status; got != catalogaudit.StatusOK {
		t.Fatalf("artists status %q", got)
	}
}

func TestAuditRowsSortedAndDeterministic(t *testing.T) {
	opts := options(fakeDB{err: errors.New("boom /abs/secret")}, nil, allArtifacts())
	opts.ExportDrift = func(context.Context) ([]string, error) {
		return []string{"generated/tracks/b.json", "generated/albums/a.json"}, nil
	}
	first, err := catalogaudit.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := catalogaudit.Run(context.Background(), opts)
	var names []string
	for _, c := range first.Checks {
		names = append(names, c.Name)
	}
	if !slices.IsSorted(names) {
		t.Fatalf("checks not sorted: %v", names)
	}
	if !slices.Equal(first.Checks, second.Checks) {
		t.Fatal("audit is not deterministic")
	}
	snap := findCheck(t, first, "snapshot")
	if snap.Status != catalogaudit.StatusError || strings.Contains(snap.Detail, "secret") {
		t.Fatalf("snapshot error must be generic: %+v", snap)
	}
	drift := findCheck(t, first, "export-fresh")
	if drift.Status != catalogaudit.StatusStale || drift.Detail != "2 files differ from the database (generated/albums/a.json, generated/tracks/b.json); run export" {
		t.Fatalf("drift %+v", drift)
	}
}

func TestAuditCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := catalogaudit.Run(ctx, options(fakeDB{}, nil, nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// The CLI prints these errors verbatim, so their wording is part of its output.
func TestCheckErrorsKeepCLIWording(t *testing.T) {
	ctx := context.Background()
	err := catalogaudit.CheckSnapshot(ctx, fakeDB{snapshot: []byte("a")}, readFrom(map[string][]byte{"s.sql": []byte("b")}), "s.sql")
	if err == nil || err.Error() != "snapshot is out of date: s.sql" {
		t.Fatalf("stale: %v", err)
	}
	err = catalogaudit.CheckSnapshot(ctx, fakeDB{err: errors.New("boom")}, readFrom(nil), "s.sql")
	if err == nil || err.Error() != "generate snapshot: boom" {
		t.Fatalf("generate: %v", err)
	}
	err = catalogaudit.CheckSnapshot(ctx, fakeDB{}, func(string) ([]byte, error) { return nil, errors.New("nope") }, "s.sql")
	if err == nil || err.Error() != "read snapshot s.sql: nope" {
		t.Fatalf("read: %v", err)
	}
	if err := catalogaudit.CheckSnapshot(ctx, fakeDB{snapshot: []byte("a")}, readFrom(map[string][]byte{"s.sql": []byte("a")}), "s.sql"); err != nil {
		t.Fatal(err)
	}

	artifacts := catalogaudit.ExportArtifacts("out", "static")
	stat := map[string]bool{}
	for _, a := range artifacts {
		stat[a.Path] = false
	}
	if err := catalogaudit.CheckExportArtifacts(statFrom(stat), artifacts); err != nil {
		t.Fatal(err)
	}
	delete(stat, filepath.Join("out", "catalog-summary.json"))
	err = catalogaudit.CheckExportArtifacts(statFrom(stat), artifacts)
	if err == nil || err.Error() != "missing export artifact out/catalog-summary.json: stat out/catalog-summary.json: file does not exist" {
		t.Fatalf("missing: %v", err)
	}
	stat[filepath.Join("out", "catalog-summary.json")] = true
	err = catalogaudit.CheckExportArtifacts(statFrom(stat), artifacts)
	if err == nil || err.Error() != "export artifact is a directory: out/catalog-summary.json" {
		t.Fatalf("dir: %v", err)
	}
	if !errors.Is(catalogaudit.CheckExportArtifacts(func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }, artifacts), os.ErrNotExist) {
		t.Fatal("stat error must be wrapped")
	}
}
