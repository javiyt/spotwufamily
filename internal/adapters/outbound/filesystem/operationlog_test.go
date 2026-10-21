package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

func entry(i int) maintenance.OperationEntry {
	return maintenance.OperationEntry{At: time.Date(2026, 10, 20, 12, i, 0, 0, time.UTC), Kind: "confirmed",
		Action: "act", JobID: string(rune('a' + i)), Outcome: "submitted", Summary: "s"}
}

func TestRecentNewestFirst(t *testing.T) {
	l := NewOperationLog(filepath.Join(t.TempDir(), "sub", "c.operations.jsonl"))
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := l.Append(ctx, entry(i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Recent(ctx, 3)
	if err != nil || len(got) != 3 || got[0].JobID != "e" || got[2].JobID != "c" || !got[0].At.Equal(entry(4).At) {
		t.Fatalf("got %+v err %v", got, err)
	}
	if got, _ := l.Recent(ctx, 0); len(got) != 0 {
		t.Fatalf("n=0: %+v", got)
	}
}

func TestOperationLogMissingFile(t *testing.T) {
	got, err := NewOperationLog(filepath.Join(t.TempDir(), "none.jsonl")).Recent(context.Background(), 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOperationLogModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "o.jsonl")
	if err := NewOperationLog(path).Append(context.Background(), entry(0)); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	di, _ := os.Stat(filepath.Dir(path))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode(), di.Mode())
	}
}

func TestOperationLogCapAndCorruptMiddle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.jsonl")
	l := NewOperationLog(path)
	ctx := context.Background()
	_ = l.Append(ctx, entry(0))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("{garbage\n")
	_ = f.Close()
	for i := 1; i < 260; i++ {
		_ = l.Append(ctx, maintenance.OperationEntry{Kind: "finished", Action: "a", JobID: "j"})
	}
	got, err := l.Recent(ctx, 10000)
	if err != nil || len(got) != 200 {
		t.Fatalf("len %d err %v", len(got), err)
	}
	small := filepath.Join(t.TempDir(), "s.jsonl")
	s := NewOperationLog(small)
	_ = s.Append(ctx, entry(0))
	_ = os.WriteFile(small, append(mustRead(t, small), []byte("{bad\n")...), 0o600)
	_ = s.Append(ctx, entry(1))
	got, _ = s.Recent(ctx, 10)
	if len(got) != 2 || got[0].JobID != "b" || got[1].JobID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOperationLogAppendAfterTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.jsonl")
	l := NewOperationLog(path)
	ctx := context.Background()
	_ = l.Append(ctx, entry(0))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"at":"2026-`)
	_ = f.Close()
	if err := l.Append(ctx, entry(1)); err != nil {
		t.Fatal(err)
	}
	got, _ := l.Recent(ctx, 10)
	if len(got) != 2 || got[0].JobID != "b" || strings.Contains(string(mustRead(t, path)), `2026-{`) {
		t.Fatalf("got %+v", got)
	}
}
