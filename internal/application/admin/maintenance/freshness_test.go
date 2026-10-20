package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeFreshness struct {
	catalog                          string
	seeded, snapshot, db, export     string
	seededOK, snapOK, dbOK, exportOK bool
	pinned, local                    string
	pinnedOK, localOK                bool
}

func (f fakeFreshness) CatalogDigest() string          { return f.catalog }
func (f fakeFreshness) SeededDigest() (string, bool)   { return f.seeded, f.seededOK }
func (f fakeFreshness) SnapshotDigest() (string, bool) { return f.snapshot, f.snapOK }
func (f fakeFreshness) DatabaseDigest() (string, bool) { return f.db, f.dbOK }
func (f fakeFreshness) ExportDigest() (string, bool)   { return f.export, f.exportOK }
func (f fakeFreshness) PinnedContentSHA() (string, bool) {
	return f.pinned, f.pinnedOK
}
func (f fakeFreshness) LocalContentSHA() (string, bool) { return f.local, f.localOK }

// freshSource is a fully up-to-date source.
func freshSource() fakeFreshness {
	return fakeFreshness{
		catalog: "c", seeded: "c", seededOK: true,
		snapshot: "d", snapOK: true, db: "d", dbOK: true,
		export: "d", exportOK: true,
		pinned: "p", pinnedOK: true, local: "p", localOK: true,
	}
}

func freshnessOf(t *testing.T, src FreshnessSource) Freshness {
	t.Helper()
	f, err := NewService(nil, nil, nil, time.Now, WithFreshness(src)).Freshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAllFreshReportsNothing(t *testing.T) {
	f := freshnessOf(t, freshSource())
	if f.SeedNeeded || f.SnapshotNeeded || f.ExportNeeded || f.RebuildNeeded || f.DiffersFromPublished || f.Offline {
		t.Fatalf("expected all false, got %+v", f)
	}
	if len(f.Reasons) != 0 {
		t.Fatalf("expected no reasons, got %v", f.Reasons)
	}
}

func TestYamlNewerThanSeedNeedsSeed(t *testing.T) {
	src := freshSource()
	src.catalog = "changed"
	f := freshnessOf(t, src)
	if !f.SeedNeeded || f.SnapshotNeeded {
		t.Fatalf("got %+v", f)
	}
	if len(f.Reasons) != 1 || f.Reasons[0] != "Catalog changed since last database seed/sync" {
		t.Fatalf("reasons %v", f.Reasons)
	}
}

func TestSnapshotOlderThanDBNeedsSnapshot(t *testing.T) {
	src := freshSource()
	src.snapshot = "old"
	f := freshnessOf(t, src)
	if !f.SnapshotNeeded || f.SeedNeeded {
		t.Fatalf("got %+v", f)
	}
	src.snapOK = false
	f = freshnessOf(t, src)
	if !f.SnapshotNeeded || f.Reasons[0] != "No snapshot found" {
		t.Fatalf("got %+v", f)
	}
}

func TestExportStaleNeedsExport(t *testing.T) {
	src := freshSource()
	src.export = "old"
	if f := freshnessOf(t, src); !f.ExportNeeded || f.SeedNeeded || f.SnapshotNeeded {
		t.Fatalf("got %+v", f)
	}
	src.exportOK = false
	if f := freshnessOf(t, src); !f.ExportNeeded || f.Reasons[0] != "No generated export found" {
		t.Fatalf("got %+v", f)
	}
}

func TestLocalHashDiffersFromPinnedMeansDiffersFromPublished(t *testing.T) {
	src := freshSource()
	src.local = "other"
	f := freshnessOf(t, src)
	if !f.DiffersFromPublished {
		t.Fatalf("got %+v", f)
	}
	if len(f.Reasons) != 1 || f.Reasons[0] != "Local data differs from the published catalog" {
		t.Fatalf("reasons %v", f.Reasons)
	}
}

func TestMissingPinnedManifestIsNotDrift(t *testing.T) {
	src := freshSource()
	src.pinnedOK = false
	f := freshnessOf(t, src)
	if f.DiffersFromPublished {
		t.Fatalf("got %+v", f)
	}
	if len(f.Reasons) != 1 || f.Reasons[0] != "No published catalog manifest to compare against" {
		t.Fatalf("reasons %v", f.Reasons)
	}
	src = freshSource()
	src.localOK = false
	if f := freshnessOf(t, src); f.DiffersFromPublished || len(f.Reasons) != 1 {
		t.Fatalf("got %+v", f)
	}
}

func TestNoDatabaseReportsOfflineNotStale(t *testing.T) {
	src := freshSource()
	src.dbOK, src.seededOK, src.exportOK = false, false, false
	src.snapOK = false
	f := freshnessOf(t, src)
	if !f.Offline || f.SeedNeeded || f.SnapshotNeeded || f.ExportNeeded || f.RebuildNeeded {
		t.Fatalf("got %+v", f)
	}
	if len(f.Reasons) != 1 || f.Reasons[0] != "No database found" {
		t.Fatalf("reasons %v", f.Reasons)
	}
}

func TestNoDatabaseWithSnapshotNeedsRebuild(t *testing.T) {
	src := freshSource()
	src.dbOK, src.seededOK, src.exportOK = false, false, false
	f := freshnessOf(t, src)
	if !f.Offline || !f.RebuildNeeded || f.SnapshotNeeded {
		t.Fatalf("got %+v", f)
	}
	want := []string{"No database found", "Database can be rebuilt from the snapshot"}
	if strings.Join(f.Reasons, "|") != strings.Join(want, "|") {
		t.Fatalf("reasons %v", f.Reasons)
	}
}

func TestReasonsDeterministicAndPathFree(t *testing.T) {
	src := fakeFreshness{
		catalog: "c", seeded: "x", seededOK: true,
		snapshot: "s", snapOK: true, db: "d", dbOK: true,
		export: "e", exportOK: true,
		pinned: "p", pinnedOK: true, local: "q", localOK: true,
	}
	want := []string{
		"Catalog changed since last database seed/sync",
		"Snapshot is older than the database",
		"Generated export is out of date with the database",
		"Local data differs from the published catalog",
	}
	for i := 0; i < 5; i++ {
		f := freshnessOf(t, src)
		if strings.Join(f.Reasons, "|") != strings.Join(want, "|") {
			t.Fatalf("reasons %v", f.Reasons)
		}
		for _, r := range f.Reasons {
			if strings.Contains(r, "/") && !strings.Contains(r, "seed/sync") {
				t.Fatalf("path-like reason %q", r)
			}
		}
	}
}

func TestFreshnessUnavailableWithoutSource(t *testing.T) {
	_, err := NewService(nil, nil, nil, time.Now).Freshness(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}
