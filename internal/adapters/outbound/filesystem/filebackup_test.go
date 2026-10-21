package filesystem

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupFileCopiesWithTimestampedExclusiveName(t *testing.T) {
	src := filepath.Join(t.TempDir(), "artists.yaml")
	content := []byte("version: 1\nartists: []\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backups")
	at := time.Date(2026, 10, 21, 7, 8, 9, 0, time.FixedZone("CEST", 2*3600))

	names := make([]string, 0, 3)
	for range 3 {
		name, err := BackupFile(src, dir, at)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	want := []string{"artists-20261021T050809Z.yaml", "artists-20261021T050809Z-2.yaml", "artists-20261021T050809Z-3.yaml"}
	for i, name := range names {
		if name != want[i] {
			t.Fatalf("names %v, want %v", names, want)
		}
		if strings.ContainsAny(name, `/\`) {
			t.Fatalf("name %q is not path-free", name)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("backup %s differs: %q %v", name, got, err)
		}
		info, _ := os.Stat(filepath.Join(dir, name))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", info.Mode().Perm())
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", info.Mode().Perm(), err)
	}
}

func TestBackupFileMissingSourceWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	if _, err := BackupFile(filepath.Join(t.TempDir(), "missing.yaml"), dir, time.Now()); err == nil {
		t.Fatal("want an error for a missing source")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left %d files behind", len(entries))
	}
}
