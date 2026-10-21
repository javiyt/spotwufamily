package maintenance

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

func TestCSVDeterministic(t *testing.T) {
	a := jobs.Report{Name: "r", Columns: []string{"a", "b"}, Rows: [][]string{{"2", "x"}, {"1", "z"}, {"1", "y"}}}
	b := jobs.Report{Name: "r", Columns: []string{"a", "b"}, Rows: [][]string{{"1", "y"}, {"1", "z"}, {"2", "x"}}}
	var b1, b2 bytes.Buffer
	if err := WriteCSV(&b1, a); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSV(&b2, b); err != nil {
		t.Fatal(err)
	}
	if b1.String() != b2.String() {
		t.Fatalf("differs:\n%s\n%s", b1.String(), b2.String())
	}
	if a.Rows[0][0] != "2" {
		t.Fatal("caller slice mutated")
	}
}

func TestCSVEscapesFormulaInjection(t *testing.T) {
	r := jobs.Report{Columns: []string{"c"}, Rows: [][]string{{"=1+1"}, {"+1"}, {"-1"}, {"@x"}, {"ok"}}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'=1+1", "'+1", "'-1", "'@x", "\nok\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %q in %q", want, buf.String())
		}
	}
}

func TestCSVEscapesTabCarriageReturnAndFullWidth(t *testing.T) {
	for _, cell := range []string{"\t=1+1", "\r=1+1", "\uFF1D1+1", "\uFF0B1", "\uFF0D1", "\uFF201"} {
		if got := guard(cell); got != "'"+cell {
			t.Errorf("guard(%q) = %q, want quote prefix", cell, got)
		}
	}
	for _, cell := range []string{"ok", "", "é=1", "1-2"} {
		if got := guard(cell); got != cell {
			t.Errorf("guard(%q) = %q, want unchanged", cell, got)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	r := jobs.Report{Name: "n", Columns: []string{"b", "a"}, Rows: [][]string{{"2", "1"}}}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"n\",\n  \"columns\": [\n    \"b\",\n    \"a\"\n  ],\n  \"rows\": [\n    {\n      \"b\": \"2\",\n      \"a\": \"1\"\n    }\n  ]\n}\n"
	if buf.String() != want {
		t.Fatalf("got %q", buf.String())
	}
}

func TestSafeStripsPathsAndSQL(t *testing.T) {
	err := Safe(errors.New("open /Users/a/data.db: no such table x; SELECT * FROM x"))
	var s interface{ Safe() string }
	if !errors.As(err, &s) {
		t.Fatal("not safe")
	}
	msg := s.Safe()
	for _, bad := range []string{"/", "\\", "SQL", "SELECT", "table", "open"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("%q contains %q", msg, bad)
		}
	}
	if se := err.(*SafeError); se.Code != "internal" {
		t.Fatalf("code %q", se.Code)
	}
}

type waitErr struct{}

func (waitErr) Error() string             { return "429 from https://api/x" }
func (waitErr) RetryAfter() time.Duration { return 90 * time.Second }

func TestSafeKeepsRetryAfter(t *testing.T) {
	err := Safe(&catalogsync.PartialSyncError{Err: waitErr{}})
	se := err.(*SafeError)
	if se.Code != "rate_limited" || !strings.Contains(se.Message, "90") || strings.Contains(se.Message, "/") {
		t.Fatalf("%+v", se)
	}
	se = Safe(fmt.Errorf("w: %w", waitErr{})).(*SafeError)
	if se.Code != "rate_limited" {
		t.Fatalf("%+v", se)
	}
}

func TestSafeMapsKnown(t *testing.T) {
	cases := map[string]error{
		"conflict":              &jobs.ConflictError{HolderID: "j1"},
		"validation":            &admin.ValidationError{},
		"credentials_missing":   ErrCredentialsMissing,
		"stale_preview":         ErrStalePreview,
		"confirmation_required": ErrConfirmationRequired,
		"unavailable":           artists.ErrSourceUnavailable,
	}
	for code, in := range cases {
		se, ok := Safe(fmt.Errorf("x: %w", in)).(*SafeError)
		if !ok || se.Code != code {
			t.Errorf("%s: got %+v", code, se)
		}
		if strings.ContainsAny(se.Message, "/\\") {
			t.Errorf("%s message %q", code, se.Message)
		}
	}
	if Safe(nil) != nil {
		t.Fatal("nil")
	}
}
