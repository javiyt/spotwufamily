package maintenance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

var (
	prCatalogDigest = strings.Repeat("c", 64)
	prManifestSHA   = strings.Repeat("d", 64)
)

// fakePRPort records Open requests and returns canned answers.
type fakePRPort struct {
	mu       sync.Mutex
	prereq   PRPrereq
	checkErr error
	opened   []PRRequest
	result   PRResult
	openErr  error
	// openFn, when set, replaces the canned Open answer.
	openFn func(context.Context, PRRequest) (PRResult, error)
}

func (f *fakePRPort) Check(context.Context) (PRPrereq, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.prereq
	p.FilesPresent = slices.Clone(p.FilesPresent)
	p.FilesChanged = slices.Clone(p.FilesChanged)
	return p, f.checkErr
}

func (f *fakePRPort) Open(ctx context.Context, req PRRequest) (PRResult, error) {
	f.mu.Lock()
	f.opened = append(f.opened, req)
	fn := f.openFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return f.result, f.openErr
}

func (f *fakePRPort) set(fn func(*PRPrereq)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.prereq)
}

func readyPRPort() *fakePRPort {
	return &fakePRPort{
		prereq: PRPrereq{
			GhAuthenticated: true, OriginResolved: true, OriginMatchesRepo: true, Repo: "javiyt/spotwufamily", BaseBranch: "main",
			ManifestMatchesLocal: true, ManifestContentSHA256: prManifestSHA, ReleaseTag: "catalog-20261022T100000Z-abcdef12",
			CatalogDigest: prCatalogDigest,
			FilesPresent:  slices.Clone(ReleasePRFiles),
			FilesChanged:  []string{"data/artists.yaml", "data/catalog.release.json"},
		},
		result: PRResult{URL: "https://github.com/javiyt/spotwufamily/pull/42"},
	}
}

func prDeps(port *fakePRPort, changes []artists.ArtistChange) PRDeps {
	return PRDeps{Port: port, Changes: func(context.Context) ([]artists.ArtistChange, error) { return changes, nil }}
}

func TestReleasePRDescribesItself(t *testing.T) {
	a := NewReleasePR(prDeps(readyPRPort(), nil))
	if a.Name() != "release-pr" {
		t.Fatalf("name %q", a.Name())
	}
	d := a.(Describer).Describe()
	if !d.UsesNetwork || !d.Destructive || d.Title == "" {
		t.Fatalf("describe %+v", d)
	}
	if err := a.Validate(Params{"x": "1"}); err == nil {
		t.Fatal("unknown params are rejected")
	}
	if err := a.Validate(nil); err != nil {
		t.Fatal(err)
	}
}

func TestReleasePRPreview(t *testing.T) {
	port := readyPRPort()
	changes := []artists.ArtistChange{
		{Slug: "rza", Kinds: []artists.ChangeKind{artists.ChangeVerified}},
		{Slug: "gza", Kinds: []artists.ChangeKind{artists.ChangeIDsAdded, artists.ChangeMetadata}},
	}
	a := NewReleasePR(prDeps(port, changes))
	pv, err := a.Preview(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.RequiresTyped != "open pull request" || !pv.Destructive || !pv.UsesNetwork || pv.Fingerprint == "" {
		t.Fatalf("preview %+v", pv)
	}
	want := map[string]string{
		"Repository":  "javiyt/spotwufamily",
		"Base branch": "main",
		"Branch":      "catalog-update-cccccccccccc-dddddddddddd",
		"Files":       "data/artists.yaml, data/artists.yaml.reviews.jsonl, data/catalog.release.json",
		"Title":       "Update catalog: gza, rza",
	}
	got := map[string]string{}
	for _, l := range pv.Scope {
		got[l.Label] = l.Value
		if strings.Contains(l.Value, "/Users") || strings.Contains(l.Value, "@") {
			t.Fatalf("scope line carries a path or account: %+v", l)
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("scope %q = %q, want %q (all %v)", k, got[k], v, got)
		}
	}
}

func TestReleasePRFilesOmitMissing(t *testing.T) {
	port := readyPRPort()
	port.set(func(p *PRPrereq) { p.FilesPresent = []string{"data/artists.yaml", "data/catalog.release.json"} })
	a := NewReleasePR(prDeps(port, nil))
	_, out, _, err := runAction(t, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != jobs.StatusSucceeded {
		t.Fatalf("outcome %+v", out)
	}
	if got := port.opened[0].Files; !slices.Equal(got, []string{"data/artists.yaml", "data/catalog.release.json"}) {
		t.Fatalf("files %v", got)
	}
	if port.opened[0].Title != "Update catalog data" {
		t.Fatalf("empty change list gives the generic title, got %q", port.opened[0].Title)
	}
}

func TestReleasePRTitleAndBody(t *testing.T) {
	changes := []artists.ArtistChange{
		{Slug: "rza", Kinds: []artists.ChangeKind{artists.ChangeVerified}},
		{Slug: "gza", Kinds: []artists.ChangeKind{artists.ChangeIDsAdded, artists.ChangeMetadata}},
		{Slug: "method-man", Kinds: []artists.ChangeKind{artists.ChangeAdded}},
		{Slug: "raekwon", Kinds: []artists.ChangeKind{artists.ChangeRelationships}},
	}
	port := readyPRPort()
	a := NewReleasePR(prDeps(port, changes))
	if _, _, _, err := runAction(t, a, nil); err != nil {
		t.Fatal(err)
	}
	req := port.opened[0]
	if req.Title != "Update catalog: gza, method-man, raekwon, …" {
		t.Fatalf("title %q", req.Title)
	}
	for _, want := range []string{"gza: ids_added, metadata", "rza: verified", "catalog-20261022T100000Z-abcdef12", "Pages"} {
		if !strings.Contains(req.Body, want) {
			t.Fatalf("body lacks %q:\n%s", want, req.Body)
		}
	}
	if req.Branch != "catalog-update-cccccccccccc-dddddddddddd" {
		t.Fatalf("branch %q", req.Branch)
	}
	if err := ValidatePRRequest(req); err != nil {
		t.Fatalf("generated request is valid: %v", err)
	}
}

func TestReleasePRAvailability(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PRPrereq)
		reason string
	}{
		{"gh", func(p *PRPrereq) { p.GhAuthenticated = false }, ghNotAuthenticatedReason},
		{"origin", func(p *PRPrereq) { p.OriginResolved = false }, prOriginReason},
		{"origin-repo", func(p *PRPrereq) { p.OriginMatchesRepo = false }, prOriginRepoReason},
		{"base", func(p *PRPrereq) { p.BaseBranch = "../main" }, prOriginReason},
		{"manifest", func(p *PRPrereq) { p.ManifestMatchesLocal = false }, prManifestReason},
		{"identity", func(p *PRPrereq) { p.IdentityMissing = true }, prIdentityReason},
		{"origin-newer", func(p *PRPrereq) { p.OriginNewer = true }, prOriginNewerMsg},
		{"nothing", func(p *PRPrereq) { p.FilesChanged = []string{"data/artists.yaml.reviews.jsonl"} }, prNothingReason},
	}
	seen := map[string]bool{}
	distinct := 0
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			port := readyPRPort()
			port.set(c.mutate)
			ok, reason := NewReleasePR(prDeps(port, nil)).Available()
			if ok || reason != c.reason {
				t.Fatalf("available=%v reason=%q, want %q", ok, reason, c.reason)
			}
			if strings.Contains(reason, "/") && c.name != "gh" {
				t.Fatalf("reason carries a path: %q", reason)
			}
		})
		if !seen[c.reason] {
			distinct++
		}
		seen[c.reason] = true
	}
	if distinct != len(cases)-1 { // "base" shares the origin reason
		t.Fatal("reasons are distinct")
	}
	ok, reason := NewReleasePR(prDeps(readyPRPort(), nil)).Available()
	if !ok || reason != "" {
		t.Fatalf("ready: %v %q", ok, reason)
	}
	port := readyPRPort()
	port.checkErr = errors.New("boom /Users/secret")
	ok, reason = NewReleasePR(prDeps(port, nil)).Available()
	if ok || strings.Contains(reason, "/Users") {
		t.Fatalf("check error: %v %q", ok, reason)
	}
}

func TestReleasePRSpecRefusesUnready(t *testing.T) {
	port := readyPRPort()
	a := NewReleasePR(prDeps(port, nil))
	ctx := confirmedCtx(t, a, nil)
	port.set(func(p *PRPrereq) { p.ManifestMatchesLocal = false })
	if _, err := a.Spec(ctx, nil); err == nil {
		t.Fatal("an unpinned manifest is refused")
	}
	if _, err := a.Spec(context.Background(), nil); err == nil {
		t.Fatal("spec without a confirmed preview fails closed")
	}
	if len(port.opened) != 0 {
		t.Fatal("nothing opened")
	}
}

func TestReleasePRAbortsWhenCatalogChangedAfterConfirm(t *testing.T) {
	port := readyPRPort()
	a := NewReleasePR(prDeps(port, nil))
	spec, err := specFor(t, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	port.set(func(p *PRPrereq) { p.CatalogDigest = strings.Repeat("e", 64) })
	_, err = spec.Run(context.Background(), &recReporter{})
	assertSafe(t, err, "stale_preview")
	if len(port.opened) != 0 {
		t.Fatal("nothing is committed after the catalog changed")
	}
}

func TestReleasePROpenedAndReused(t *testing.T) {
	port := readyPRPort()
	a := NewReleasePR(prDeps(port, nil))
	spec, out, _, err := runAction(t, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Resources, ResourceCatalogYAML) || !slices.Contains(spec.Resources, ResourceRelease) {
		t.Fatalf("resources %v", spec.Resources)
	}
	if !strings.Contains(out.Result.Summary, "https://github.com/javiyt/spotwufamily/pull/42") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
	port.result.Reused = true
	_, out, _, err = runAction(t, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Result.Summary, "already open") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestReleasePRNothingToOpenSucceeds(t *testing.T) {
	port := readyPRPort()
	port.openErr = ErrPRNothingToOpen
	_, out, _, err := runAction(t, NewReleasePR(prDeps(port, nil)), nil)
	if err != nil || out.Status != jobs.StatusSucceeded || !strings.Contains(out.Result.Summary, "Nothing to open") {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestReleasePRErrorsAreFixedMessages(t *testing.T) {
	for _, sentinel := range []error{ErrPROriginNewer, ErrPRBranchDiverged, ErrGitIdentity, ErrPRGit, ErrPRInvalidRequest} {
		port := readyPRPort()
		port.openErr = errors.Join(sentinel, errors.New("stderr: /Users/javi/repo token ghp_x user@example.com"))
		_, _, _, err := runAction(t, NewReleasePR(prDeps(port, nil)), nil)
		var se *SafeError
		if !errors.As(err, &se) {
			t.Fatalf("%v: not a SafeError: %v", sentinel, err)
		}
		if strings.Contains(se.Message, "/Users") || strings.Contains(se.Message, "@") || strings.Contains(se.Message, "ghp_") {
			t.Fatalf("leaky message %q", se.Message)
		}
	}
	port := readyPRPort()
	port.openErr = ErrPROriginNewer
	_, _, _, err := runAction(t, NewReleasePR(prDeps(port, nil)), nil)
	if err == nil || err.Error() != "origin has newer changes to catalog data; update your checkout first" {
		t.Fatalf("got %v", err)
	}
}

func TestReleasePRPushedButNotOpenedReportsAndIsRerunnable(t *testing.T) {
	port := readyPRPort()
	ctx, cancel := context.WithCancel(context.Background())
	port.openFn = func(context.Context, PRRequest) (PRResult, error) {
		cancel()
		return PRResult{}, errors.Join(ErrPRNotOpened, context.Canceled)
	}
	a := NewReleasePR(prDeps(port, nil))
	spec, err := a.Spec(confirmedCtx(t, a, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := spec.Run(ctx, &recReporter{})
	if err == nil {
		t.Fatal("a cancelled run reports an error")
	}
	if !strings.Contains(out.Result.Summary, "catalog-update-cccccccccccc-dddddddddddd was pushed") ||
		!strings.Contains(out.Result.Summary, "not opened") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
	port.openFn = nil
	_, out, _, err = runAction(t, a, nil)
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("rerun: %+v %v", out, err)
	}
}

func TestValidatePRRequest(t *testing.T) {
	ok := PRRequest{Branch: "catalog-update-cccccccccccc-dddddddddddd", Title: "Update catalog data", Body: "body",
		Files: []string{"data/artists.yaml"}}
	if err := ValidatePRRequest(ok); err != nil {
		t.Fatal(err)
	}
	bad := []PRRequest{
		{Branch: "main", Title: "t", Body: "b", Files: []string{"data/artists.yaml"}},
		{Branch: ok.Branch, Title: "t", Body: "b", Files: []string{"data/artists.yaml", "go.mod"}},
		{Branch: ok.Branch, Title: "t", Body: "b", Files: []string{"../data/artists.yaml"}},
		{Branch: ok.Branch, Title: "t", Body: "b", Files: []string{"data/artists.yaml", "data/artists.yaml"}},
		{Branch: ok.Branch, Title: "t", Body: "b", Files: nil},
		{Branch: ok.Branch, Title: "a\nb", Body: "b", Files: []string{"data/artists.yaml"}},
		{Branch: ok.Branch, Title: "", Body: "b", Files: []string{"data/artists.yaml"}},
		{Branch: ok.Branch, Title: "t", Body: "b", Files: []string{"data/artists.yaml.reviews.jsonl"}},
	}
	for i, r := range bad {
		if err := ValidatePRRequest(r); !errors.Is(err, ErrPRInvalidRequest) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
}

func TestReleasePRCheckErrorsAreSanitised(t *testing.T) {
	port := readyPRPort()
	port.checkErr = errors.New("open /Users/javi/secret/data/artists.yaml: denied")
	a := NewReleasePR(prDeps(port, nil))
	_, err := a.Preview(context.Background(), nil)
	var se *SafeError
	if !errors.As(err, &se) || strings.Contains(se.Message, "/Users") {
		t.Fatalf("preview: %v", err)
	}
	_, err = a.Spec(WithVerifiedFingerprint(context.Background(), "x"), nil)
	if !errors.As(err, &se) || strings.Contains(se.Message, "/Users") {
		t.Fatalf("spec: %v", err)
	}
}

func TestReleasePRBaseBranchPattern(t *testing.T) {
	for _, b := range []string{"main", "release/2026", "v1.2"} {
		if !validPRBase(b) {
			t.Fatalf("%q rejected", b)
		}
	}
	for _, b := range []string{"", "-main", "/main", "a..b", "../main", "main branch", ".hidden"} {
		if validPRBase(b) {
			t.Fatalf("%q accepted", b)
		}
	}
}

func TestReleasePRRefusesACatalogOtherThanTheConfirmedOne(t *testing.T) {
	port := readyPRPort()
	a := NewReleasePR(prDeps(port, nil))
	ctx := WithConfirmedCatalogDigest(confirmedCtx(t, a, Params{}), "0123456789abcdef0123")
	spec, err := a.Spec(ctx, Params{})
	if err == nil {
		_, err = spec.Run(ctx, &recReporter{})
	}
	if err == nil || (!errors.Is(err, ErrStalePreview) && !strings.Contains(err.Error(), "catalog changed")) {
		t.Fatalf("mismatched confirmed digest: %v", err)
	}
	if len(port.opened) != 0 {
		t.Fatal("pull request opened for another catalog")
	}
}

func TestReleasePRReportsTheBlockedReasonBeforeTheConfirmedDigest(t *testing.T) {
	port := readyPRPort()
	port.set(func(p *PRPrereq) { p.OriginResolved = false; p.CatalogDigest = "" })
	a := NewReleasePR(prDeps(port, nil)).(*releasePRAction)
	ctx := WithConfirmedCatalogDigest(context.Background(), prCatalogDigest)
	p, _ := port.Check(ctx)
	_, err := a.verify(ctx, p, a.fingerprint(p, a.request(ctx, p)))
	if err == nil || !strings.Contains(err.Error(), prOriginReason) {
		t.Fatalf("verify with an unresolved origin: %v", err)
	}
}

// blockingPRPort holds its first Check until released, then answers with
// the prerequisites current at that moment's start.
type blockingPRPort struct {
	*fakePRPort
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingPRPort) Check(ctx context.Context) (PRPrereq, error) {
	p, err := b.fakePRPort.Check(ctx)
	if b.calls.Add(1) == 1 {
		close(b.started)
		<-b.release
	}
	return p, err
}

func TestReleasePRForcedRecheckIgnoresAStaleInFlightCheck(t *testing.T) {
	inner := readyPRPort()
	inner.set(func(p *PRPrereq) { p.ManifestMatchesLocal = false })
	port := &blockingPRPort{fakePRPort: inner, started: make(chan struct{}), release: make(chan struct{})}
	a := NewReleasePR(PRDeps{Port: port}).(*releasePRAction)

	_, _ = a.AvailableNow() // starts the pre-publish background check
	<-port.started
	inner.set(func(p *PRPrereq) { p.ManifestMatchesLocal = true }) // the publish pinned the manifest
	a.forceRecheck()
	close(port.release) // the stale check stores its result after the recheck request
	// Wait until the in-flight check has stored.
	a.flight.Lock()
	a.flight.Unlock()
	if ok, reason := a.Available(); !ok {
		t.Fatalf("a stale pre-publish check was reused after forceRecheck: %q", reason)
	}
	if port.calls.Load() < 2 {
		t.Fatalf("checks %d: the forced recheck did not run", port.calls.Load())
	}
}

func TestUpdateWebsiteStopsBeforePublishingWhenThePRCannotBeOpened(t *testing.T) {
	h := newHarness(t, newWorld(baseCatalog(), idEdited()), nil)
	port := readyPRPort()
	port.set(func(p *PRPrereq) { p.OriginNewer = true })
	pr := NewReleasePR(PRDeps{Port: port})
	actions := []Action{}
	for _, n := range stageActionNames {
		if n == "release-pr" {
			continue
		}
		actions = append(actions, h.stages[n])
	}
	actions = append(actions, &prAvailableAnyway{Action: pr})
	h.s = NewService(h.m, actions, []byte("secret"), time.Now, WithPending(h.w.deps()))
	j := h.runToEnd(t)
	if j.Status == jobs.StatusSucceeded || h.stages["release-publish"].runs.Load() != 0 {
		t.Fatalf("status %s, publishes %d", j.Status, h.stages["release-publish"].runs.Load())
	}
	if !strings.Contains(j.Message, prOriginNewerMsg) {
		t.Fatalf("message %q", j.Message)
	}
}

// prAvailableAnyway hides release-pr's cached availability, as when the
// last fetch predates origin's change, so only the pre-publish check sees it.
type prAvailableAnyway struct{ Action }

func (a *prAvailableAnyway) Available() (bool, string)    { return true, "" }
func (a *prAvailableAnyway) AvailableNow() (bool, string) { return true, "" }
func (a *prAvailableAnyway) checkBeforePublish(ctx context.Context) error {
	return a.Action.(prePublishChecker).checkBeforePublish(ctx)
}
