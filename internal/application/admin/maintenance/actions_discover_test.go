package maintenance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// recReporter records what a job function reports.
type recReporter struct {
	mu     sync.Mutex
	steps  []string
	done   int
	total  int
	counts map[string]int
}

func (r *recReporter) Step(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, name)
}

func (r *recReporter) Progress(done, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done, r.total = done, total
}

func (r *recReporter) Count(key string, delta int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[key] += delta
}

// runAction builds the action's job spec and runs it synchronously.
func runAction(t *testing.T, a Action, p Params) (jobs.Spec, jobs.Outcome, *recReporter, error) {
	t.Helper()
	spec, err := specFor(t, a, p)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	r := &recReporter{}
	out, err := spec.Run(context.Background(), r)
	return spec, out, r, err
}

// confirmedCtx carries the fingerprint of a fresh preview of a, as
// Service.Run does once the user confirmed it. A failing preview yields a
// plain context so Spec reports its own error.
func confirmedCtx(t *testing.T, a Action, p Params) context.Context {
	t.Helper()
	pv, err := a.Preview(context.Background(), p)
	if err != nil {
		return context.Background()
	}
	return WithVerifiedFingerprint(context.Background(), pv.Fingerprint)
}

// specFor calls Spec as Service.Run does after a confirmed preview.
func specFor(t *testing.T, a Action, p Params) (jobs.Spec, error) {
	t.Helper()
	return a.Spec(confirmedCtx(t, a, p), p)
}

func staticDigest(d string) CatalogDigest {
	return func(context.Context) (string, error) { return d, nil }
}

type fakeDiscover struct {
	mu       sync.Mutex
	report   artists.DiscoverWuFamilyReport
	err      error
	applyErr error    // returned by apply calls only
	calls    []bool   // apply flag of each call
	only     []string // allowlist of the last apply call
	// beforeApply, when set, runs at the start of an apply call.
	beforeApply func()
}

func (f *fakeDiscover) Run(_ context.Context, apply bool, only []string, progress func(artists.DiscoverWuFamilyProgress)) (artists.DiscoverWuFamilyReport, error) {
	if apply && f.beforeApply != nil {
		f.beforeApply()
	}
	f.mu.Lock()
	f.calls = append(f.calls, apply)
	if apply {
		f.only = slices.Clone(only)
	}
	rep := f.report
	err := f.err
	if apply && f.applyErr != nil {
		err = f.applyErr
	}
	f.mu.Unlock()
	if progress != nil {
		progress(artists.DiscoverWuFamilyProgress{Stage: "fetching"})
		progress(artists.DiscoverWuFamilyProgress{Stage: "compared", Found: rep.Found})
	}
	if apply && err == nil {
		for _, c := range rep.New {
			if only == nil || slices.Contains(only, c.Slug) {
				rep.Added = append(rep.Added, c)
			}
		}
	}
	return rep, err
}

func (f *fakeDiscover) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, apply := range f.calls {
		if apply {
			n++
		}
	}
	return n
}

func newDiscoveryReport() artists.DiscoverWuFamilyReport {
	return artists.DiscoverWuFamilyReport{
		Found: 4,
		Existing: []artists.DiscoveryCandidate{
			{Name: "RZA", Slug: "rza", Category: catalog.CategoryCore, Reason: "already in catalog"},
		},
		New: []artists.DiscoveryCandidate{
			{Name: "Killah Priest", Slug: "killah-priest", Category: catalog.CategoryCore, Reason: "new"},
			{Name: "Cappadonna", Slug: "cappadonna", Category: catalog.CategoryCore, Reason: "new"},
		},
		Suppressed: []artists.DiscoveryCandidate{
			{Name: "Sunz of Man", Slug: "sunz-of-man", Category: catalog.CategoryCore, LedgerStatus: catalog.CandidateStatus("rejected")},
		},
	}
}

func TestDiscoverPreviewDoesNotWrite(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	a := NewDiscoverPreview(DiscoverDeps{Runner: f, Digest: staticDigest("d1")})

	if a.Name() != "discover-preview" {
		t.Fatalf("name %q", a.Name())
	}
	if ok, _ := a.Available(); !ok {
		t.Fatal("discovery needs no credentials")
	}
	if err := a.Validate(Params{}); err != nil {
		t.Fatal(err)
	}
	pv, err := a.Preview(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.UsesNetwork || pv.Destructive || pv.RequiresTyped != "" || pv.Fingerprint == "" {
		t.Fatalf("unexpected preview %+v", pv)
	}

	spec, out, r, err := runAction(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Resources, []string{"catalog-yaml"}) || spec.Kind != "discover-preview" {
		t.Fatalf("spec %+v", spec)
	}
	if f.writes() != 0 {
		t.Fatalf("dry run applied %d times", f.writes())
	}
	if out.Status != jobs.StatusSucceeded || out.Result.Report == nil {
		t.Fatalf("outcome %+v", out)
	}
	if got := len(out.Result.Report.Rows); got != 4 {
		t.Fatalf("want 4 rows (1 existing, 2 new, 1 suppressed), got %d", got)
	}
	if r.counts["new"] != 2 || r.counts["added"] != 0 || r.counts["found"] != 4 {
		t.Fatalf("counts %v", r.counts)
	}
	if !slices.Contains(r.steps, "fetching") {
		t.Fatalf("steps %v", r.steps)
	}
}

func TestDiscoverImportAppliesAndReportsCounts(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: staticDigest("d1")})
	if a.Name() != "discover-import" {
		t.Fatalf("name %q", a.Name())
	}

	pv, err := a.Preview(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if f.writes() != 0 || len(f.calls) != 1 {
		t.Fatalf("preview must be a single dry run, calls %v", f.calls)
	}
	if pv.Destructive || !pv.UsesNetwork || pv.Fingerprint == "" {
		t.Fatalf("unexpected preview %+v", pv)
	}
	if !scopeHas(pv, "2") {
		t.Fatalf("scope should mention the 2 new candidates: %+v", pv.Scope)
	}

	spec, out, r, err := runAction(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Resources, []string{"catalog-yaml"}) {
		t.Fatalf("resources %v", spec.Resources)
	}
	if f.writes() != 1 {
		t.Fatalf("want one applied run, got %d", f.writes())
	}
	if out.Status != jobs.StatusSucceeded {
		t.Fatalf("outcome %+v", out)
	}
	want := map[string]int{"found": 4, "existing": 1, "new": 2, "added": 2, "suppressed": 1}
	for k, v := range want {
		if r.counts[k] != v {
			t.Fatalf("count %s: want %d, got %v", k, v, r.counts)
		}
	}
	added := 0
	for _, row := range out.Result.Report.Rows {
		if row[0] == "added" {
			added++
		}
		if row[0] == "new" {
			t.Fatalf("applied report should list added, not new: %v", row)
		}
	}
	if added != 2 || !strings.Contains(out.Result.Summary, "2") {
		t.Fatalf("added rows %d, summary %q", added, out.Result.Summary)
	}
}

func TestDiscoverImportFingerprintTracksCandidatesAndCatalog(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	digest := "d1"
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: func(context.Context) (string, error) { return digest, nil }})

	first, err := a.Preview(context.Background(), Params{})
	if err != nil {
		t.Fatal(err)
	}
	same, _ := a.Preview(context.Background(), Params{})
	if first.Fingerprint != same.Fingerprint {
		t.Fatal("fingerprint must be stable for the same state")
	}

	f.report.New = f.report.New[:1]
	fewer, _ := a.Preview(context.Background(), Params{})
	if fewer.Fingerprint == first.Fingerprint {
		t.Fatal("a changed candidate set must change the fingerprint")
	}

	digest = "d2"
	changed, _ := a.Preview(context.Background(), Params{})
	if changed.Fingerprint == fewer.Fingerprint {
		t.Fatal("a changed catalog must change the fingerprint")
	}
}

func TestDiscoverActionsRejectUnknownParams(t *testing.T) {
	deps := DiscoverDeps{Runner: &fakeDiscover{}, Digest: staticDigest("d")}
	for _, a := range []Action{NewDiscoverPreview(deps), NewDiscoverImport(deps)} {
		err := a.Validate(Params{"apply": "true"})
		var se *SafeError
		if !errors.As(err, &se) || se.Code != "validation" {
			t.Fatalf("%s: want validation error, got %v", a.Name(), err)
		}
	}
}

func TestDiscoverFailureIsSafe(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport(), applyErr: errors.New("open /home/secret/artists.yaml: permission denied")}
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: staticDigest("d")})

	_, _, _, err := runAction(t, a, Params{})
	var se *SafeError
	if !errors.As(err, &se) || strings.Contains(se.Error(), "secret") {
		t.Fatalf("want a safe error, got %v", err)
	}
}

func scopeHas(pv Preview, substr string) bool {
	for _, l := range pv.Scope {
		if strings.Contains(l.Value, substr) {
			return true
		}
	}
	return false
}

func TestDiscoverImportSpecRequiresVerifiedFingerprint(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: staticDigest("d1")})
	if _, err := a.Spec(context.Background(), Params{}); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("an unconfirmed import must fail closed, got %v", err)
	}
	if _, err := NewDiscoverPreview(DiscoverDeps{Runner: f, Digest: staticDigest("d1")}).Spec(context.Background(), Params{}); err != nil {
		t.Fatalf("the dry run needs no confirmation: %v", err)
	}
}

func TestDiscoverImportRefusesChangedCandidatesAfterConfirm(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: staticDigest("d1")})
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	f.report.New = append(f.report.New, artists.DiscoveryCandidate{Name: "Shyheim", Slug: "shyheim", Category: catalog.CategoryCore})
	_, err = spec.Run(context.Background(), &recReporter{})
	var se *SafeError
	if !errors.As(Safe(err), &se) || se.Code != "stale_preview" {
		t.Fatalf("want stale preview, got %v", err)
	}
	if f.writes() != 0 {
		t.Fatalf("a stale import must write nothing, applied %d times", f.writes())
	}
}

func TestDiscoverImportRefusesChangedCatalogAfterConfirm(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	digest := "d1"
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: func(context.Context) (string, error) { return digest, nil }})
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	digest = "d2"
	if _, err := spec.Run(context.Background(), &recReporter{}); !errors.Is(err, ErrStalePreview) && Safe(err).(*SafeError).Code != "stale_preview" {
		t.Fatalf("want stale preview, got %v", err)
	}
	if f.writes() != 0 {
		t.Fatal("a stale import must write nothing")
	}
}

func TestDiscoverImportAppliesOnlyThePreviewedSlugs(t *testing.T) {
	f := &fakeDiscover{report: newDiscoveryReport()}
	a := NewDiscoverImport(DiscoverDeps{Runner: f, Digest: staticDigest("d1")})
	spec, err := specFor(t, a, Params{})
	if err != nil {
		t.Fatal(err)
	}
	// Wikipedia changes between the in-job check and the import.
	f.beforeApply = func() {
		f.mu.Lock()
		f.report.New = append(f.report.New, artists.DiscoveryCandidate{Name: "Shyheim", Slug: "shyheim", Category: catalog.CategoryCore})
		f.mu.Unlock()
	}
	out, err := spec.Run(context.Background(), &recReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.only, []string{"cappadonna", "killah-priest"}) {
		t.Fatalf("allowlist %v", f.only)
	}
	for _, row := range out.Result.Report.Rows {
		if row[0] == "added" && row[1] == "shyheim" {
			t.Fatal("a candidate that was not previewed was imported")
		}
	}
}
