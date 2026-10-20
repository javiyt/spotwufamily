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

type auditCall struct {
	artist  string
	offline bool
}

type fakeAudit struct {
	mu     sync.Mutex
	report artists.ProfileAuditReport
	err    error
	calls  []auditCall
}

func (f *fakeAudit) Run(_ context.Context, artist string, offline bool, progress func(done, total int)) (artists.ProfileAuditReport, error) {
	f.mu.Lock()
	f.calls = append(f.calls, auditCall{artist: artist, offline: offline})
	f.mu.Unlock()
	if progress != nil {
		n := len(f.report.Profiles)
		for i := range n {
			progress(i+1, n)
		}
	}
	return f.report, f.err
}

func credentials(present bool) CredentialsChecker { return func() bool { return present } }

func auditReport() artists.ProfileAuditReport {
	return artists.ProfileAuditReport{
		Offline: true,
		Summary: artists.ProfileAuditSummary{Total: 3, Verified: 1, Suspicious: 1, Unavailable: 1},
		Profiles: []artists.ProfileAuditResult{
			{
				ArtistSlug: "rza", SpotifyID: "bbbbbbbbbbbbbbbbbbbbbb", ReviewStatus: catalog.ProfileStatus("verified"),
				Classification: artists.ClassSuspicious,
				Findings: []artists.ProfileFinding{
					{Code: artists.FindingGenreMismatch, Severity: artists.SeverityWarning, Detail: "no hip hop genres"},
					{Code: artists.FindingUnrelatedAlbums, Severity: artists.SeverityError, Detail: "3 albums"},
				},
			},
			{
				ArtistSlug: "gza", SpotifyID: "cccccccccccccccccccccc", ReviewStatus: catalog.ProfileStatus("unreviewed"),
				Classification: artists.ClassUnavailable, Explanation: "Spotify profile not cached",
			},
			{
				ArtistSlug: "rza", SpotifyID: "aaaaaaaaaaaaaaaaaaaaaa", ReviewStatus: catalog.ProfileStatus("verified"),
				Classification: artists.ClassVerified,
			},
		},
	}
}

func TestAuditProfilesOfflineNeedsNoCredentials(t *testing.T) {
	f := &fakeAudit{report: auditReport()}
	a := NewAuditProfiles(AuditDeps{Runner: f, Digest: staticDigest("d"), Credentials: credentials(false)})

	if a.Name() != "audit-profiles" {
		t.Fatalf("name %q", a.Name())
	}
	if ok, reason := a.Available(); !ok {
		t.Fatalf("offline audit must stay available without credentials: %s", reason)
	}
	p := Params{"offline": "true"}
	if err := a.Validate(p); err != nil {
		t.Fatal(err)
	}
	pv, err := a.Preview(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if pv.UsesNetwork || pv.Destructive || pv.RequiresTyped != "" {
		t.Fatalf("offline audit is local and read-only: %+v", pv)
	}

	spec, out, r, err := runAction(t, a, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Resources) != 0 {
		t.Fatalf("read-only audit should hold no resources: %v", spec.Resources)
	}
	if out.Status != jobs.StatusSucceeded || len(f.calls) != 1 || !f.calls[0].offline {
		t.Fatalf("outcome %+v calls %+v", out, f.calls)
	}
	if r.done != 3 || r.total != 3 || r.counts["verified"] != 1 || r.counts["suspicious"] != 1 {
		t.Fatalf("progress %d/%d counts %v", r.done, r.total, r.counts)
	}

	// Online mode needs Spotify.
	if _, err := a.Preview(context.Background(), Params{}); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("online preview without credentials: want ErrCredentialsMissing, got %v", err)
	}
	if _, err := specFor(t, a, Params{"offline": "false"}); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("online spec without credentials: want ErrCredentialsMissing, got %v", err)
	}
}

func TestAuditProfilesOnlineUsesNetwork(t *testing.T) {
	f := &fakeAudit{report: auditReport()}
	a := NewAuditProfiles(AuditDeps{Runner: f, Digest: staticDigest("d"), Credentials: credentials(true)})

	pv, err := a.Preview(context.Background(), Params{"artist": "rza"})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.UsesNetwork {
		t.Fatal("online audit uses Spotify and MusicBrainz")
	}
	offline, _ := a.Preview(context.Background(), Params{"artist": "rza", "offline": "true"})
	if offline.Fingerprint == pv.Fingerprint {
		t.Fatal("fingerprint must depend on params")
	}
	if _, _, _, err := runAction(t, a, Params{"artist": "rza"}); err != nil {
		t.Fatal(err)
	}
	if f.calls[0] != (auditCall{artist: "rza", offline: false}) {
		t.Fatalf("call %+v", f.calls[0])
	}
}

func TestAuditReportRowsSorted(t *testing.T) {
	f := &fakeAudit{report: auditReport()}
	a := NewAuditProfiles(AuditDeps{Runner: f, Digest: staticDigest("d"), Credentials: credentials(false)})

	_, out, _, err := runAction(t, a, Params{"offline": "true"})
	if err != nil {
		t.Fatal(err)
	}
	rep := out.Result.Report
	if rep == nil {
		t.Fatal("no report")
	}
	if !slices.Equal(rep.Columns, []string{"slug", "spotify_id", "status", "verdict", "reasons"}) {
		t.Fatalf("columns %v", rep.Columns)
	}
	var keys []string
	for _, row := range rep.Rows {
		keys = append(keys, row[0]+"/"+row[1])
	}
	want := []string{"gza/cccccccccccccccccccccc", "rza/aaaaaaaaaaaaaaaaaaaaaa", "rza/bbbbbbbbbbbbbbbbbbbbbb"}
	if !slices.Equal(keys, want) {
		t.Fatalf("rows not sorted: %v", keys)
	}
	if rep.Rows[2][2] != "verified" || rep.Rows[2][3] != "suspicious" {
		t.Fatalf("status/verdict %v", rep.Rows[2])
	}
	if !strings.Contains(rep.Rows[2][4], "genre_mismatch") || !strings.Contains(rep.Rows[2][4], "unrelated_albums") {
		t.Fatalf("reasons %q", rep.Rows[2][4])
	}
	if rep.Rows[0][4] != "Spotify profile not cached" {
		t.Fatalf("explanation used when there are no findings: %q", rep.Rows[0][4])
	}
}

func TestAuditProfilesValidatesParams(t *testing.T) {
	a := NewAuditProfiles(AuditDeps{Runner: &fakeAudit{}, Digest: staticDigest("d"), Credentials: credentials(true)})
	for _, p := range []Params{
		{"offline": "maybe"},
		{"artist": "../etc/passwd"},
		{"artist": "RZA"},
		{"force": "true"},
	} {
		err := a.Validate(p)
		var se *SafeError
		if !errors.As(err, &se) || se.Code != "validation" {
			t.Fatalf("%v: want validation error, got %v", p, err)
		}
	}
	for _, p := range []Params{{}, {"artist": "method-man", "offline": "false"}, {"artist": ""}} {
		if err := a.Validate(p); err != nil {
			t.Fatalf("%v: %v", p, err)
		}
	}
}
