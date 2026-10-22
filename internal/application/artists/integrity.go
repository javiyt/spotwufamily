package artists

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// IntegritySeverity ranks a finding of the offline catalog integrity gate.
type IntegritySeverity string

const (
	// IntegrityError fails the gate: the catalog is invalid or would sync an
	// identity that was rejected.
	IntegrityError IntegritySeverity = "error"
	// IntegrityWarning is reported but does not fail the gate unless strict:
	// intentionally pending candidates and weak but valid evidence.
	IntegrityWarning IntegritySeverity = "warning"
	// IntegrityInfo documents decisions that block sync on purpose.
	IntegrityInfo IntegritySeverity = "info"
)

// IntegrityCode is a stable, machine-readable finding category.
type IntegrityCode string

const (
	IntegritySchema               IntegrityCode = "schema"
	IntegrityRejectedEnabled      IntegrityCode = "rejected_enabled_mapping"
	IntegrityBlockedSyncIdentity  IntegrityCode = "blocked_sync_identity"
	IntegrityPendingProfile       IntegrityCode = "pending_profile"
	IntegrityUnreviewedSync       IntegrityCode = "unreviewed_sync_identity"
	IntegrityCircularEvidence     IntegrityCode = "circular_evidence"
	IntegrityNoSyncableProfile    IntegrityCode = "enabled_without_syncable_profile"
	IntegrityRelationshipEvidence IntegrityCode = "relationship_without_evidence"
	IntegrityRelationshipArchived IntegrityCode = "relationship_archived_artist"
	IntegrityPolicyAllowsLegacy   IntegrityCode = "policy_allows_unreviewed"
)

// IntegrityFinding is one categorised problem or note about the catalog.
type IntegrityFinding struct {
	Severity IntegritySeverity `json:"severity"`
	Code     IntegrityCode     `json:"code"`
	// Artist is the slug the finding concerns; empty for catalog-wide findings.
	Artist string `json:"artist,omitempty"`
	// Subject is the Spotify ID, relationship key or schema field concerned.
	Subject string `json:"subject,omitempty"`
	Message string `json:"message"`
}

// IntegritySummary counts findings per severity.
type IntegritySummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// IntegrityReport is the deterministic result of AuditIntegrity.
type IntegrityReport struct {
	Catalog  string             `json:"catalog"`
	Summary  IntegritySummary   `json:"summary"`
	Findings []IntegrityFinding `json:"findings"`
}

// Failed applies the CI gate policy: errors always fail; warnings only when
// strict is set.
func (r IntegrityReport) Failed(strict bool) bool {
	return r.Summary.Errors > 0 || (strict && r.Summary.Warnings > 0)
}

// AuditIntegrity validates the editorial catalog offline. It never touches
// the network, the database or the YAML file.
type AuditIntegrity struct {
	store CatalogStore
}

func NewAuditIntegrity(store CatalogStore) AuditIntegrity {
	return AuditIntegrity{store: store}
}

func (a AuditIntegrity) Run(ctx context.Context, path string) (IntegrityReport, error) {
	c, err := a.store.Load(ctx, path)
	if err != nil {
		return IntegrityReport{}, fmt.Errorf("load artist catalog: %w", err)
	}
	report := CheckIntegrity(c)
	report.Catalog = path
	return report, nil
}

// CheckIntegrity evaluates an in-memory catalog.
func CheckIntegrity(c catalog.EditorialCatalog) IntegrityReport {
	var findings []IntegrityFinding
	add := func(severity IntegritySeverity, code IntegrityCode, artist, subject, format string, args ...any) {
		findings = append(findings, IntegrityFinding{
			Severity: severity, Code: code, Artist: artist, Subject: subject, Message: fmt.Sprintf(format, args...),
		})
	}

	for _, issue := range catalog.ValidateEditorialCatalog(c) {
		add(IntegrityError, IntegritySchema, "", issue.Field, "%s", issue.Message)
	}

	policy := c.ProfileSyncPolicy
	if policy == catalog.SyncAllowUnreviewed {
		add(IntegrityWarning, IntegrityPolicyAllowsLegacy, "", "profile_sync_policy",
			"profile_sync_policy allow_unreviewed ingests profiles without review; migrate to verified_only once profiles are reviewed")
	}

	rejected := map[string]string{}
	for _, artist := range c.Artists {
		for _, profile := range artist.SpotifyProfiles {
			if profile.Status == catalog.ProfileRejected {
				rejected[profile.ID] = artist.Slug
			}
		}
	}

	for _, artist := range c.Artists {
		checkArtistIntegrity(artist, policy, rejected, add)
	}
	checkRelationshipIntegrity(c, add)

	sortIntegrityFindings(findings)
	report := IntegrityReport{Findings: findings}
	if report.Findings == nil {
		report.Findings = []IntegrityFinding{}
	}
	for _, f := range findings {
		switch f.Severity {
		case IntegrityError:
			report.Summary.Errors++
		case IntegrityWarning:
			report.Summary.Warnings++
		default:
			report.Summary.Infos++
		}
	}
	return report
}

type integrityAdder func(IntegritySeverity, IntegrityCode, string, string, string, ...any)

func checkArtistIntegrity(artist catalog.Artist, policy catalog.ProfileSyncPolicy, rejected map[string]string, add integrityAdder) {
	for _, id := range artist.AllSpotifyIDs() {
		status := artist.StatusOf(id)
		if owner, isRejected := rejected[id]; isRejected && artist.Enabled {
			add(IntegrityError, IntegrityRejectedEnabled, artist.Slug, id,
				"enabled artist links Spotify ID rejected by %q", owner)
			continue
		}
		switch status {
		case catalog.ProfileCandidate, catalog.ProfileNeedsReview:
			add(IntegrityWarning, IntegrityPendingProfile, artist.Slug, id,
				"linked profile is %s and is not synced until verified", status)
		case catalog.ProfileUnreviewed:
			if artist.Enabled && catalog.ProfileSyncAllowed(status, policy) {
				add(IntegrityWarning, IntegrityUnreviewedSync, artist.Slug, id,
					"unreviewed profile is synced only because of profile_sync_policy allow_unreviewed")
			} else if artist.Enabled {
				add(IntegrityWarning, IntegrityPendingProfile, artist.Slug, id,
					"linked profile is unreviewed and is not synced until verified")
			}
		}
	}

	for _, profile := range artist.SpotifyProfiles {
		switch profile.Status {
		case catalog.ProfileRejected:
			add(IntegrityInfo, IntegrityBlockedSyncIdentity, artist.Slug, profile.ID,
				"rejected profile permanently blocks this Spotify ID from sync")
		case catalog.ProfileCandidate, catalog.ProfileNeedsReview:
			if !artist.HasSpotifyID(profile.ID) {
				add(IntegrityWarning, IntegrityPendingProfile, artist.Slug, profile.ID,
					"profile is %s and awaiting review", profile.Status)
			}
		case catalog.ProfileVerified:
			if onlySpotifyProfileEvidence(profile.Evidence) {
				add(IntegrityWarning, IntegrityCircularEvidence, artist.Slug, profile.ID,
					"verified only by a Spotify profile page, which is the claim itself; add MusicBrainz, Wikidata or another source")
			}
		}
	}

	if artist.Enabled && len(artist.SyncableSpotifyIDs(policy)) == 0 {
		add(IntegrityWarning, IntegrityNoSyncableProfile, artist.Slug, "",
			"enabled artist has no profile that sync may ingest")
	}
}

// onlySpotifyProfileEvidence reports evidence that is independent in form but
// circular: the Spotify page cannot confirm its own mapping.
func onlySpotifyProfileEvidence(evidence []catalog.Evidence) bool {
	found := false
	for _, item := range evidence {
		if !item.Independent() {
			continue
		}
		if item.Type != catalog.EvidenceSpotifyProfile {
			return false
		}
		found = true
	}
	return found
}

func checkRelationshipIntegrity(c catalog.EditorialCatalog, add integrityAdder) {
	archived := map[string]bool{}
	for _, artist := range c.Artists {
		archived[artist.Slug] = artist.Archived
	}
	for _, rel := range c.Relationships {
		key := rel.Key().String()
		if rel.Status != catalog.RelationshipRejected && len(rel.Evidence) == 0 {
			add(IntegrityWarning, IntegrityRelationshipEvidence, rel.From, key,
				"%s relationship has no provenance", rel.Status)
		}
		if rel.Status == catalog.RelationshipVerified && (archived[rel.From] || archived[rel.To]) {
			add(IntegrityWarning, IntegrityRelationshipArchived, rel.From, key,
				"verified relationship involves an archived artist")
		}
	}
}

func sortIntegrityFindings(findings []IntegrityFinding) {
	rank := map[IntegritySeverity]int{IntegrityError: 0, IntegrityWarning: 1, IntegrityInfo: 2}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Artist != b.Artist {
			return a.Artist < b.Artist
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Message < b.Message
	})
}

// FormatIntegrityJSON renders the report deterministically.
func FormatIntegrityJSON(report IntegrityReport) ([]byte, error) {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// FormatIntegrityText renders one line per finding plus a summary.
func FormatIntegrityText(report IntegrityReport) []byte {
	var b strings.Builder
	for _, f := range report.Findings {
		fmt.Fprintf(&b, "%s [%s]", f.Severity, f.Code)
		if f.Artist != "" {
			fmt.Fprintf(&b, " %s", f.Artist)
		}
		if f.Subject != "" {
			fmt.Fprintf(&b, " %s", f.Subject)
		}
		fmt.Fprintf(&b, ": %s\n", f.Message)
	}
	fmt.Fprintf(&b, "integrity: %d errors, %d warnings, %d infos\n", report.Summary.Errors, report.Summary.Warnings, report.Summary.Infos)
	return []byte(b.String())
}
