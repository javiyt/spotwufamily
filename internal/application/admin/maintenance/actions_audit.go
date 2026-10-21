package maintenance

import (
	"context"
	"fmt"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// AuditPort runs the read-only Spotify profile audit. Offline it answers from
// the local cache only and must need neither credentials nor database writes.
type AuditPort interface {
	Run(ctx context.Context, artist string, offline bool, progress func(done, total int)) (artists.ProfileAuditReport, error)
}

// AuditDeps are the ports the audit-profiles action needs.
type AuditDeps struct {
	Runner      AuditPort
	Digest      CatalogDigest
	Credentials CredentialsChecker
}

var auditColumns = []string{"slug", "spotify_id", "status", "verdict", "reasons"}

type auditAction struct{ deps AuditDeps }

// NewAuditProfiles returns the audit-profiles action. Params: artist
// (optional slug) and offline (true|false, default false). Online mode needs
// Spotify credentials; offline mode is always available.
func NewAuditProfiles(d AuditDeps) Action { return &auditAction{deps: d} }

func (a *auditAction) Name() string { return "audit-profiles" }

func (a *auditAction) Describe() Description {
	return Description{Title: "Audit Spotify profiles", UsesNetwork: true}
}

func (a *auditAction) Available() (bool, string) { return true, "" }

func (a *auditAction) Validate(p Params) error {
	if err := RejectUnknown(p, "artist", "offline"); err != nil {
		return err
	}
	if err := validBool(p, "offline"); err != nil {
		return err
	}
	if s := p["artist"]; s != "" && !validSlug(s) {
		return &SafeError{Code: "validation", Message: "The artist must be a catalog slug."}
	}
	return nil
}

// checkCredentials refuses online audits without Spotify credentials.
func (a *auditAction) checkCredentials(p Params) error {
	if !boolParam(p, "offline") && !a.deps.Credentials() {
		return ErrCredentialsMissing
	}
	return nil
}

func (a *auditAction) Preview(ctx context.Context, p Params) (Preview, error) {
	if err := a.checkCredentials(p); err != nil {
		return Preview{}, err
	}
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return Preview{}, err
	}
	offline := boolParam(p, "offline")
	artist := p["artist"]
	scope := "all artists with Spotify IDs"
	if artist != "" {
		scope = artist
	}
	sources := "Spotify and MusicBrainz (network)"
	if offline {
		sources = "local cache only (offline)"
	}
	return Preview{
		Title: "Audit Spotify profiles",
		Scope: []ScopeLine{
			{Label: "Artists", Value: scope},
			{Label: "Sources", Value: sources},
			{Label: "Writes", Value: "none (read-only report)"},
		},
		UsesNetwork: !offline,
		Fingerprint: fingerprint(a.Name(), digest, artist, yesNo(offline)),
	}, nil
}

func (a *auditAction) Spec(_ context.Context, p Params) (jobs.Spec, error) {
	if err := a.checkCredentials(p); err != nil {
		return jobs.Spec{}, err
	}
	artist, offline := p["artist"], boolParam(p, "offline")
	return jobs.Spec{
		Kind: a.Name(),
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			r.Step("auditing")
			report, err := a.deps.Runner.Run(ctx, artist, offline, r.Progress)
			if err != nil {
				if ctx.Err() != nil {
					return jobs.Outcome{}, ctx.Err()
				}
				return jobs.Outcome{}, Safe(err)
			}
			return auditOutcome(report, r), nil
		},
	}, nil
}

func auditOutcome(report artists.ProfileAuditReport, r jobs.Reporter) jobs.Outcome {
	rep := &jobs.Report{Name: "audit-profiles", Columns: auditColumns}
	for _, p := range report.Profiles {
		rep.Rows = append(rep.Rows, []string{
			p.ArtistSlug, p.SpotifyID, string(p.ReviewStatus), string(p.Classification), auditReasons(p),
		})
	}
	SortRows(rep)

	s := report.Summary
	counts := []struct {
		key string
		n   int
	}{
		{"verified", s.Verified}, {"suspicious", s.Suspicious}, {"ambiguous", s.Ambiguous},
		{"unavailable", s.Unavailable}, {"insufficient_evidence", s.InsufficientEvidence},
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		r.Count(c.key, c.n)
		parts = append(parts, fmt.Sprintf("%d %s", c.n, strings.ReplaceAll(c.key, "_", " ")))
	}
	summary := fmt.Sprintf("Audited %d profiles: %s.", s.Total, strings.Join(parts, ", "))
	if report.Offline {
		summary += " Offline: answered from the local cache only."
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: rep}}
}

// auditReasons lists the findings, or the explanation when there are none.
func auditReasons(p artists.ProfileAuditResult) string {
	if len(p.Findings) == 0 {
		return p.Explanation
	}
	parts := make([]string, 0, len(p.Findings))
	for _, f := range p.Findings {
		parts = append(parts, string(f.Code)+": "+f.Detail)
	}
	return strings.Join(parts, "; ")
}
