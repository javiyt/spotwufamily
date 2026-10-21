package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

type memStore struct {
	mu   sync.Mutex
	jobs []jobs.Job
}

func (s *memStore) Save(_ context.Context, j jobs.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, j)
	return nil
}

func (s *memStore) Load(context.Context) ([]jobs.Job, error) { return nil, nil }

type fakeAction struct {
	name        string
	destructive bool
	available   bool
	reason      string
	fingerprint atomic.Value // string
	run         jobs.Func
	submitted   atomic.Int32
	// specFP is the VerifiedFingerprint Spec received ("" when absent).
	specFP atomic.Value // string
}

func newFake(name string) *fakeAction {
	a := &fakeAction{name: name, available: true}
	a.fingerprint.Store("fp-1")
	return a
}

func (a *fakeAction) Name() string { return a.name }

func (a *fakeAction) Validate(p Params) error { return RejectUnknown(p, "scope") }

func (a *fakeAction) Preview(_ context.Context, p Params) (Preview, error) {
	pv := Preview{
		Title:       "Fake " + a.name,
		Scope:       []ScopeLine{{Label: "Scope", Value: p["scope"]}},
		Destructive: a.destructive,
		Fingerprint: a.fingerprint.Load().(string),
	}
	if a.destructive {
		pv.RequiresTyped = "delete " + p["scope"]
	}
	return pv, nil
}

func (a *fakeAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	fp, _ := VerifiedFingerprint(ctx)
	a.specFP.Store(fp)
	a.submitted.Add(1)
	run := a.run
	if run == nil {
		run = func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}
	}
	return jobs.Spec{Kind: a.name, Resources: []string{"catalog"}, Run: run}, nil
}

func (a *fakeAction) Available() (bool, string) { return a.available, a.reason }

func (a *fakeAction) Describe() Description {
	return Description{Title: "Fake " + a.name, UsesNetwork: true, Destructive: a.destructive}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestService(t *testing.T, actions ...Action) (*Service, *jobs.Manager, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	var n atomic.Int32
	m := jobs.NewManager(&memStore{}, time.Now, func() string { return fmt.Sprintf("job-%d", n.Add(1)) })
	return NewService(m, actions, []byte("test-secret"), clk.now), m, clk
}

func assertSafe(t *testing.T, err error, code string) {
	t.Helper()
	var se *SafeError
	if !errors.As(Safe(err), &se) || se.Code != code {
		t.Fatalf("want safe code %q, got %v (%T)", code, err, err)
	}
}

func TestRunRequiresValidToken(t *testing.T) {
	a := newFake("fake")
	s, m, _ := newTestService(t, a)
	p := Params{"scope": "all"}

	for _, tok := range []Token{"", "garbage", "123.abc", "notanumber.xyz"} {
		_, err := s.Run(context.Background(), "fake", p, tok, "")
		if !errors.Is(err, ErrStalePreview) {
			t.Fatalf("token %q: want ErrStalePreview, got %v", tok, err)
		}
		assertSafe(t, err, "stale_preview")
	}
	if a.submitted.Load() != 0 {
		t.Fatal("job submitted without a valid token")
	}

	_, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	// Tampering with the MAC invalidates the token.
	bad := tok[:len(tok)-2] + "AA"
	if bad == tok {
		bad = tok[:len(tok)-2] + "BB"
	}
	if _, err := s.Run(context.Background(), "fake", p, bad, ""); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("tampered token: want ErrStalePreview, got %v", err)
	}

	j, err := s.Run(context.Background(), "fake", p, tok, "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != "fake" || j.ID == "" {
		t.Fatalf("unexpected job %+v", j)
	}
	m.Wait(j.ID)
}

func TestRunRefusesExpiredToken(t *testing.T) {
	a := newFake("fake")
	s, m, clk := newTestService(t, a)
	p := Params{"scope": "all"}

	_, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(9*time.Minute + 59*time.Second)
	j, err := s.Run(context.Background(), "fake", p, tok, "")
	if err != nil {
		t.Fatalf("token within TTL refused: %v", err)
	}
	m.Wait(j.ID)

	_, tok, err = s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(10*time.Minute + time.Second)
	if _, err := s.Run(context.Background(), "fake", p, tok, ""); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("want ErrStalePreview, got %v", err)
	}
}

func TestRunRefusesWhenFingerprintChanged(t *testing.T) {
	a := newFake("fake")
	s, _, _ := newTestService(t, a)
	p := Params{"scope": "all"}

	_, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	a.fingerprint.Store("fp-2")
	if _, err := s.Run(context.Background(), "fake", p, tok, ""); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("want ErrStalePreview, got %v", err)
	}
	if a.submitted.Load() != 0 {
		t.Fatal("job submitted for a stale preview")
	}
}

func TestTokenBoundToParams(t *testing.T) {
	a := newFake("fake")
	b := newFake("other")
	s, _, _ := newTestService(t, a, b)

	_, tok, err := s.Preview(context.Background(), "fake", Params{"scope": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "fake", Params{"scope": "b"}, tok, ""); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("different params: want ErrStalePreview, got %v", err)
	}
	if _, err := s.Run(context.Background(), "other", Params{"scope": "a"}, tok, ""); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("different action: want ErrStalePreview, got %v", err)
	}
	if a.submitted.Load()+b.submitted.Load() != 0 {
		t.Fatal("job submitted with a token for other params")
	}
}

func TestCanonicalParamsUnambiguous(t *testing.T) {
	x := canonicalParams(Params{"a": "1&b=2"})
	y := canonicalParams(Params{"a": "1", "b": "2"})
	if x == y {
		t.Fatalf("canonical encodings collide: %q", x)
	}
	if canonicalParams(Params{"b": "2", "a": "1"}) != y {
		t.Fatal("canonical encoding depends on map order")
	}
}

func TestDestructiveRequiresTypedPhrase(t *testing.T) {
	a := newFake("fake")
	a.destructive = true
	s, m, _ := newTestService(t, a)
	p := Params{"scope": "all"}

	pv, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	if pv.RequiresTyped != "delete all" || pv.Action != "fake" {
		t.Fatalf("unexpected preview %+v", pv)
	}
	for _, typed := range []string{"", "delete al", "Delete all", "delete all "} {
		_, err := s.Run(context.Background(), "fake", p, tok, typed)
		if !errors.Is(err, ErrConfirmationRequired) {
			t.Fatalf("typed %q: want ErrConfirmationRequired, got %v", typed, err)
		}
		assertSafe(t, err, "confirmation_required")
	}
	if a.submitted.Load() != 0 {
		t.Fatal("job submitted without confirmation")
	}
	j, err := s.Run(context.Background(), "fake", p, tok, "delete all")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
}

func TestUnavailableActionRefused(t *testing.T) {
	a := newFake("fake")
	s, _, _ := newTestService(t, a)
	p := Params{"scope": "all"}

	_, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	a.available, a.reason = false, "Spotify credentials are not configured."

	if _, _, err := s.Preview(context.Background(), "fake", p); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("preview: want ErrUnavailable, got %v", err)
	}
	if _, err := s.Run(context.Background(), "fake", p, tok, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("run: want ErrUnavailable, got %v", err)
	}
	if a.submitted.Load() != 0 {
		t.Fatal("unavailable action submitted")
	}
}

func TestUnknownParamRejected(t *testing.T) {
	a := newFake("fake")
	s, _, _ := newTestService(t, a)

	_, tok, err := s.Preview(context.Background(), "fake", Params{"scope": "all"})
	if err != nil {
		t.Fatal(err)
	}
	bad := Params{"scope": "all", "path": "/etc/passwd"}
	if _, _, err := s.Preview(context.Background(), "fake", bad); err == nil {
		t.Fatal("preview accepted unknown param")
	} else {
		assertSafe(t, err, "validation")
	}
	_, err = s.Run(context.Background(), "fake", bad, tok, "")
	assertSafe(t, err, "validation")

	if _, _, err := s.Preview(context.Background(), "nope", Params{}); err == nil {
		t.Fatal("unknown action accepted")
	} else {
		assertSafe(t, err, "not_found")
	}
}

func TestConflictErrorPassesThrough(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	a := newFake("fake")
	a.run = func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
		close(started)
		<-release
		return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
	}
	b := newFake("other")
	s, m, _ := newTestService(t, a, b)
	p := Params{"scope": "all"}

	_, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Run(context.Background(), "fake", p, tok, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	_, tok2, err := s.Preview(context.Background(), "other", p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Run(context.Background(), "other", p, tok2, "")
	var ce *jobs.ConflictError
	if !errors.As(err, &ce) || ce.HolderID != first.ID {
		t.Fatalf("want *jobs.ConflictError held by %s, got %v (%T)", first.ID, err, err)
	}
	close(release)
	m.Wait(first.ID)
}

func TestDuplicateActionNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate action name")
		}
	}()
	newTestService(t, newFake("fake"), newFake("fake"))
}

func TestActionsListsAvailability(t *testing.T) {
	a := newFake("b-first")
	b := newFake("a-second")
	b.available, b.reason = false, "Not configured."
	b.destructive = true
	s, _, _ := newTestService(t, a, b)

	got := s.Actions()
	if len(got) != 2 || got[0].Name != "b-first" || got[1].Name != "a-second" {
		t.Fatalf("not in registration order: %+v", got)
	}
	if !got[0].Available || got[0].UnavailableReason != "" {
		t.Fatalf("first should be available: %+v", got[0])
	}
	if got[1].Available || got[1].UnavailableReason != "Not configured." {
		t.Fatalf("second should be unavailable with reason: %+v", got[1])
	}
	if got[1].Title != "Fake a-second" || !got[1].UsesNetwork || !got[1].Destructive || got[0].Destructive {
		t.Fatalf("description not reported: %+v", got)
	}
}

func TestRunPassesVerifiedFingerprintToSpec(t *testing.T) {
	a := newFake("fake")
	a.destructive = true
	s, m, _ := newTestService(t, a)
	p := Params{"scope": "all"}
	pv, tok, err := s.Preview(context.Background(), "fake", p)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Run(context.Background(), "fake", p, tok, "delete all")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(j.ID)
	if got, _ := a.specFP.Load().(string); got != pv.Fingerprint || got != "fp-1" {
		t.Fatalf("Spec saw verified fingerprint %q, want the previewed %q", got, pv.Fingerprint)
	}
	if _, ok := VerifiedFingerprint(context.Background()); ok {
		t.Fatal("a plain context carries no verified fingerprint")
	}
	if _, ok := VerifiedFingerprint(WithVerifiedFingerprint(context.Background(), "")); ok {
		t.Fatal("an empty fingerprint is not a verified one")
	}
}
