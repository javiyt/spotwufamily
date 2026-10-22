package maintenance

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecentOperations(t *testing.T) {
	l := &memLog{}
	a := newFake("fake")
	s, m := logService(t, l, nil, a)
	runFake(t, s, m, Params{"scope": "all"})
	got, err := s.RecentOperations(context.Background(), 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("RecentOperations = %+v, %v", got, err)
	}

	// Without a log there is nothing to list, and no error.
	plain, _, _ := newTestService(t, newFake("x"))
	got, err = plain.RecentOperations(context.Background(), 10)
	if err != nil || got != nil {
		t.Fatalf("no log: got %+v, %v", got, err)
	}
	// A log failure is returned as a safe error, never the raw one.
	l.err = errors.New("open /Users/secret/ops.jsonl: denied")
	broken := &failingLog{err: l.err}
	s2, _ := logService(t, broken, nil, newFake("fake"))
	_, err = s2.RecentOperations(context.Background(), 10)
	if err == nil || strings.Contains(err.Error(), "/Users") {
		t.Fatalf("want a safe error, got %v", err)
	}
}

type failingLog struct{ err error }

func (f *failingLog) Append(context.Context, OperationEntry) error { return f.err }
func (f *failingLog) Recent(context.Context, int) ([]OperationEntry, error) {
	return nil, f.err
}

func TestPreviewActionAndRunActionDelegate(t *testing.T) {
	a := newFake("fake")
	s, m, _ := newTestService(t, a)
	p := Params{"scope": "all"}
	pv, tok, err := s.PreviewAction(context.Background(), "fake", p)
	if err != nil || pv.Action != "fake" || tok == "" {
		t.Fatalf("PreviewAction = %+v, %q, %v", pv, tok, err)
	}
	if _, err := s.RunAction(context.Background(), "fake", p, "bogus", ""); !errors.Is(err, ErrStalePreview) {
		t.Fatalf("bad token: want ErrStalePreview, got %v", err)
	}
	j, err := s.RunAction(context.Background(), "fake", p, tok, "")
	if err != nil || j.Kind != "fake" {
		t.Fatalf("RunAction = %+v, %v", j, err)
	}
	m.Wait(j.ID)
}

func TestRecentOperationsBoundsN(t *testing.T) {
	l := &countingLog{}
	s, _ := logService(t, l, nil, newFake("fake"))
	if got, err := s.RecentOperations(context.Background(), 0); err != nil || got != nil || l.asked != 0 {
		t.Fatalf("n=0 must not read the log: %+v %v asked=%d", got, err, l.asked)
	}
	_, _ = s.RecentOperations(context.Background(), 1_000_000)
	if l.asked != maxRecentOperations {
		t.Fatalf("n must be capped at %d, asked %d", maxRecentOperations, l.asked)
	}
}

type countingLog struct{ asked int }

func (c *countingLog) Append(context.Context, OperationEntry) error { return nil }
func (c *countingLog) Recent(_ context.Context, n int) ([]OperationEntry, error) {
	c.asked = n
	return nil, nil
}

func TestActionComputesOnlyTheNamedAction(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	b.available, b.reason = false, "b is broken"
	counting := &countingAvail{fakeAction: newFake("c")}
	s, _, _ := newTestService(t, a, b, counting)

	info, ok := s.Action("b")
	if !ok || info.Available || info.UnavailableReason != "b is broken" || info.Title != "Fake b" {
		t.Fatalf("Action(b) = %+v, %v", info, ok)
	}
	if _, ok := s.Action("a"); !ok {
		t.Fatal("Action(a) must be found")
	}
	if counting.calls.Load() != 0 {
		t.Fatal("Action must not compute other actions' availability")
	}
	for _, name := range []string{"missing", ""} {
		if info, ok := s.Action(name); ok || info != (ActionInfo{}) {
			t.Fatalf("Action(%q) = %+v, %v; want not found", name, info, ok)
		}
	}
}

type countingAvail struct {
	*fakeAction
	calls atomic.Int32
}

func (c *countingAvail) Available() (bool, string) {
	c.calls.Add(1)
	return true, ""
}

func TestUnknownActionErrorIsExported(t *testing.T) {
	s, _, _ := newTestService(t, newFake("a"))
	if _, _, err := s.Preview(context.Background(), "nope", Params{}); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("want ErrUnknownAction, got %v", err)
	}
}

func TestRecentOperationsCanceledRequestIsNotALogFailure(t *testing.T) {
	var reported []error
	l := &cancelAwareLog{}
	s, _ := logService(t, l, func(err error) { reported = append(reported, err) }, newFake("fake"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.RecentOperations(ctx, 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the cancellation to be visible to the caller, got %v", err)
	}
	if len(reported) != 0 {
		t.Fatalf("a canceled request must not be reported as a log failure: %v", reported)
	}
}

func TestRecentOperationsRealFailureIsReportedAsRead(t *testing.T) {
	var reported []error
	s, _ := logService(t, &failingLog{err: errors.New("permission denied")},
		func(err error) { reported = append(reported, err) }, newFake("fake"))
	_, err := s.RecentOperations(context.Background(), 10)
	var se *SafeError
	if !errors.As(err, &se) || errors.Is(err, context.Canceled) {
		t.Fatalf("want a safe unreadable error, got %v", err)
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "read operation log") {
		t.Fatalf("reported = %v", reported)
	}
}

func TestOperationLogWriteFailureIsReportedAsWrite(t *testing.T) {
	var reported []error
	l := &memLog{err: errors.New("disk full")}
	s, m := logService(t, l, func(err error) { reported = append(reported, err) }, newFake("fake"))
	j := runFake(t, s, m, Params{"scope": "all"})
	if j.ID == "" || len(reported) == 0 || !strings.Contains(reported[0].Error(), "write operation log") {
		t.Fatalf("job %+v reported %v", j, reported)
	}
}

type cancelAwareLog struct{}

func (cancelAwareLog) Append(context.Context, OperationEntry) error { return nil }
func (cancelAwareLog) Recent(ctx context.Context, _ int) ([]OperationEntry, error) {
	return nil, ctx.Err()
}
