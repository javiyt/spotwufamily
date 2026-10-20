package artists

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// FormatProfileAuditJSON renders the report deterministically.
func FormatProfileAuditJSON(report ProfileAuditReport) ([]byte, error) {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// FormatProfileAuditMarkdown renders one section per artist and one block per
// profile. Negative and missing evidence are always listed separately.
func FormatProfileAuditMarkdown(report ProfileAuditReport) []byte {
	var buf bytes.Buffer
	buf.WriteString("# Spotify Profile Audit\n\n")
	mode := "online"
	if report.Offline {
		mode = "offline (cache only)"
	}
	fmt.Fprintf(&buf, "Checked on %s (%s).\n\n", report.CheckedOn, mode)
	s := report.Summary
	fmt.Fprintf(&buf, "Summary: total=%d verified=%d suspicious=%d ambiguous=%d unavailable=%d insufficient_evidence=%d\n\n",
		s.Total, s.Verified, s.Suspicious, s.Ambiguous, s.Unavailable, s.InsufficientEvidence)
	if len(report.Profiles) == 0 {
		buf.WriteString("No profiles audited.\n")
	}

	lastSlug := ""
	for _, p := range report.Profiles {
		if p.ArtistSlug != lastSlug {
			state := "enabled"
			if !p.ArtistEnabled {
				state = "disabled"
			}
			fmt.Fprintf(&buf, "## %s (%s, %s)\n\n", escapeMarkdown(p.ArtistName), p.ArtistSlug, state)
			lastSlug = p.ArtistSlug
		}
		writeProfileMarkdown(&buf, p)
	}
	if len(report.RejectedProfiles) > 0 {
		buf.WriteString("## Rejected profiles (history)\n\n")
		for _, r := range report.RejectedProfiles {
			fmt.Fprintf(&buf, "### `%s` - %s (%s) - **rejected**\n\n", r.SpotifyID, escapeMarkdown(r.ArtistName), r.ArtistSlug)
			fmt.Fprintf(&buf, "- URI: `%s`\n- Reviewed: %s by %s\n", r.URI, r.ReviewedAt, r.Reviewer)
			for _, e := range r.Evidence {
				fmt.Fprintf(&buf, "- Evidence `%s` %s: %s\n", e.Type, e.URL, escapeMarkdown(e.Rationale))
			}
			buf.WriteString("\n")
		}
	}
	return buf.Bytes()
}

func writeProfileMarkdown(buf *bytes.Buffer, p ProfileAuditResult) {
	fmt.Fprintf(buf, "### `%s` (%s) - **%s** [%s]\n\n", p.SpotifyID, p.Role, p.Classification, p.Severity)
	fmt.Fprintf(buf, "- URI: `%s`\n", p.URI)
	if p.ProfileName != "" {
		fmt.Fprintf(buf, "- Spotify name: %s\n", escapeMarkdown(p.ProfileName))
	}
	fmt.Fprintf(buf, "- Spotify genres: %s | configured genres: %s\n", joinOrNone(p.Genres), joinOrNone(p.ConfiguredGenres))
	fmt.Fprintf(buf, "- Review status: %s | Spotify source: %s | checked: %s\n", p.ReviewStatus, p.SpotifyStatus, p.CheckedOn)
	fmt.Fprintf(buf, "- Explanation: %s\n", p.Explanation)
	fmt.Fprintf(buf, "- MusicBrainz: %s, release_groups=%d matched=%d unmatched=%d\n",
		p.MusicBrainz.Status, p.MusicBrainz.ReleaseGroups, p.MusicBrainz.Matched, p.MusicBrainz.Unmatched)

	fmt.Fprintf(buf, "- Releases (%d total):", p.ReleaseCount)
	if len(p.Releases) == 0 {
		buf.WriteString(" none\n")
	} else {
		buf.WriteString("\n")
		for _, r := range p.Releases {
			fmt.Fprintf(buf, "  - %s (%s)\n", escapeMarkdown(r.Name), r.Date)
		}
	}
	if len(p.TrackSamples) > 0 {
		buf.WriteString("- Track samples:\n")
		for _, t := range p.TrackSamples {
			fmt.Fprintf(buf, "  - %s\n", escapeMarkdown(t))
		}
	}

	buf.WriteString("\n**Positive evidence**\n\n")
	writeEvidence(buf, p.Evidence, catalog.EvidencePositive)
	buf.WriteString("\n**Negative evidence**\n\n")
	writeEvidence(buf, p.Evidence, catalog.EvidenceNegative)
	buf.WriteString("\n**Warnings**\n\n")
	writeEvidence(buf, p.Evidence, catalog.EvidenceWarning)
	buf.WriteString("\n**Missing evidence**\n\n")
	if len(p.MissingEvidence) == 0 {
		buf.WriteString("None.\n")
	}
	for _, m := range p.MissingEvidence {
		fmt.Fprintf(buf, "- %s\n", m)
	}
	buf.WriteString("\n**Findings**\n\n")
	if len(p.Findings) == 0 {
		buf.WriteString("None.\n")
	}
	for _, f := range p.Findings {
		fmt.Fprintf(buf, "- `%s` [%s]: %s\n", f.Code, f.Severity, f.Detail)
	}
	buf.WriteString("\n")
}

func writeEvidence(buf *bytes.Buffer, evidence []catalog.IdentityEvidence, polarity catalog.EvidencePolarity) {
	written := false
	for _, e := range evidence {
		if e.Polarity != polarity {
			continue
		}
		written = true
		fmt.Fprintf(buf, "- `%s`: %s\n", e.Kind, e.Detail)
	}
	if !written {
		buf.WriteString("None.\n")
	}
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// ErrApplyBlocked means the audit result does not allow changing the catalog.
var ErrApplyBlocked = errors.New("applying the profile audit is blocked")

type ApplyProfileAuditInput struct {
	Report    ProfileAuditReport
	Slug      string
	SpotifyID string
	DryRun    bool
}

// ApplyProfileAudit is the optional, manual follow-up of an audit. It only
// moves a suspicious profile to needs_review (it never rejects or verifies, as
// those stay human decisions) and refuses ambiguous or any other result.
type ApplyProfileAudit struct {
	store CatalogStore
}

func NewApplyProfileAudit(store CatalogStore) ApplyProfileAudit {
	return ApplyProfileAudit{store: store}
}

func (a ApplyProfileAudit) Run(ctx context.Context, path string, in ApplyProfileAuditInput) (ReviewProfileResult, error) {
	var found *ProfileAuditResult
	for i := range in.Report.Profiles {
		p := &in.Report.Profiles[i]
		if p.ArtistSlug == in.Slug && p.SpotifyID == in.SpotifyID {
			found = p
			break
		}
	}
	if found == nil {
		return ReviewProfileResult{}, fmt.Errorf("%w: %s/%s is not in the report", ErrApplyBlocked, in.Slug, in.SpotifyID)
	}
	if found.Classification != ClassSuspicious {
		return ReviewProfileResult{}, fmt.Errorf("%w: %s/%s is %s; only suspicious profiles can be applied", ErrApplyBlocked, in.Slug, in.SpotifyID, found.Classification)
	}

	checked, err := time.Parse(time.DateOnly, in.Report.CheckedOn)
	if err != nil {
		return ReviewProfileResult{}, fmt.Errorf("%w: report has no valid check date", ErrApplyBlocked)
	}
	return NewReviewProfile(a.store).Run(ctx, path, ReviewProfileInput{
		Slug:       in.Slug,
		SpotifyID:  in.SpotifyID,
		Status:     catalog.ProfileNeedsReview,
		ReviewedAt: checked,
		DryRun:     in.DryRun,
	})
}
