package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

func TestJournalLoadReturnsLatestPerID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "jobs.jsonl")
	j := NewJobJournal(path)
	ctx := context.Background()
	for _, s := range []jobs.Job{
		{ID: "a", Status: jobs.StatusQueued},
		{ID: "b", Status: jobs.StatusRunning},
		{ID: "a", Status: jobs.StatusSucceeded, Counts: map[string]int{"n": 2}},
	} {
		if err := j.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewJobJournal(path).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[0].Status != jobs.StatusSucceeded || got[0].Counts["n"] != 2 || got[1].ID != "b" {
		t.Fatalf("got %+v", got)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode())
	}
}

func TestJournalIgnoresTruncatedLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	j := NewJobJournal(path)
	ctx := context.Background()
	if err := j.Save(ctx, jobs.Job{ID: "a", Status: jobs.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"ID":"a","Status":"succ`)
	_ = f.Close()
	got, err := j.Load(ctx)
	if err != nil || len(got) != 1 || got[0].Status != jobs.StatusRunning {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestJournalLoadMissingFile(t *testing.T) {
	got, err := NewJobJournal(filepath.Join(t.TempDir(), "none.jsonl")).Load(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
}
