package artists

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type CandidateSearcher interface {
	SearchArtistCandidates(context.Context, catalog.Artist) ([]catalog.ArtistCandidate, error)
}

type CatalogAwareCandidateSearcher interface {
	SearchArtistCandidatesWithCatalog(context.Context, catalog.Artist, catalog.EditorialCatalog) ([]catalog.ArtistCandidate, error)
}

type ResolveArtists struct {
	store    CatalogStore
	searcher CandidateSearcher
}

func NewResolveArtists(store CatalogStore, searcher CandidateSearcher) ResolveArtists {
	return ResolveArtists{store: store, searcher: searcher}
}

type ResolveReport struct {
	Entries []ResolveReportEntry
	Errors  []ResolveError
	Applied []ResolvedArtist
	Skipped []ResolveSkip
}

type ResolveReportEntry struct {
	Artist   catalog.Artist
	Matches  []catalog.CandidateMatch
	Decision catalog.EligibilityDecision
}

type ResolvedArtist struct {
	Slug      string                     `json:"slug"`
	Name      string                     `json:"name"`
	SpotifyID string                     `json:"spotify_id"`
	Score     int                        `json:"score"`
	Reason    string                     `json:"reason"`
	Evidence  []catalog.IdentityEvidence `json:"evidence,omitempty"`
}

type ResolveSkip struct {
	Slug   string                    `json:"slug"`
	Name   string                    `json:"name"`
	Status catalog.EligibilityStatus `json:"status"`
	Reason string                    `json:"reason"`
}

type ResolveError struct {
	Slug string
	Err  error
}

type ResolveProgress struct {
	Stage       string
	ArtistSlug  string
	ArtistName  string
	ArtistIndex int
	ArtistTotal int
	MatchCount  int
	Err         error
}

type ApplyResolveOptions struct {
	MinScore       int
	MinScoreGap    int
	EnableResolved bool
	Progress       func(ResolveProgress)
}

func (r ResolveArtists) Run(ctx context.Context, path string) (ResolveReport, error) {
	return r.RunWithProgress(ctx, path, nil)
}

func (r ResolveArtists) RunWithProgress(ctx context.Context, path string, progress func(ResolveProgress)) (ResolveReport, error) {
	c, err := r.store.Load(ctx, path)
	if err != nil {
		return ResolveReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	return r.resolve(ctx, c, catalog.EligibilityPolicy{IsRejected: c.IsRejectedSpotifyID}, progress)
}

func (r ResolveArtists) Apply(ctx context.Context, path string, options ApplyResolveOptions) (ResolveReport, error) {
	c, err := r.store.Load(ctx, path)
	if err != nil {
		return ResolveReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	// The evidence gate is part of the policy: --apply and --enable-applied
	// both go through the same evaluator as the report and the CLI.
	policy := catalog.EligibilityPolicy{
		MinScore:    options.MinScore,
		MinScoreGap: options.MinScoreGap,
		IsRejected:  c.IsRejectedSpotifyID,
	}
	report, err := r.resolve(ctx, c, policy, options.Progress)
	if err != nil {
		return ResolveReport{}, err
	}
	if len(report.Errors) > 0 {
		return report, nil
	}

	updates := map[string]ResolvedArtist{}
	for _, entry := range report.Entries {
		decision := entry.Decision
		if decision.Status != catalog.EligibilityApproved {
			report.Skipped = append(report.Skipped, ResolveSkip{
				Slug:   entry.Artist.Slug,
				Name:   entry.Artist.Name,
				Status: decision.Status,
				Reason: strings.Join(decision.Reasons, "; "),
			})
			continue
		}
		resolved := ResolvedArtist{
			Slug:      entry.Artist.Slug,
			Name:      entry.Artist.Name,
			SpotifyID: decision.SpotifyID,
			Score:     decision.Score,
			Reason:    entry.Matches[0].Reason,
			Evidence:  decision.Positive,
		}
		updates[entry.Artist.Slug] = resolved
		report.Applied = append(report.Applied, resolved)
	}

	if len(updates) == 0 {
		return report, nil
	}

	for i := range c.Artists {
		update, ok := updates[c.Artists[i].Slug]
		if !ok {
			continue
		}
		c.Artists[i].SpotifyID = update.SpotifyID
		// A name match is a lead, not proof: record it as a candidate so sync
		// waits for a human review.
		c.Artists[i].SpotifyProfiles = append(c.Artists[i].SpotifyProfiles, catalog.SpotifyProfile{
			ID:     update.SpotifyID,
			Status: catalog.ProfileCandidate,
		})
		if options.EnableResolved {
			c.Artists[i].Enabled = true
		}
	}

	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		return report, fmt.Errorf("resolved catalog is invalid: %s", formatValidationIssues(issues))
	}
	if err := r.store.Save(ctx, path, c); err != nil {
		return report, fmt.Errorf("save resolved artist catalog: %w", err)
	}

	return report, nil
}

func (r ResolveArtists) resolve(ctx context.Context, c catalog.EditorialCatalog, policy catalog.EligibilityPolicy, progress func(ResolveProgress)) (ResolveReport, error) {
	report := ResolveReport{}
	artistsToResolve := unresolvedArtists(c.Artists)
	for index, artist := range artistsToResolve {
		event := ResolveProgress{Stage: "artist_started", ArtistSlug: artist.Slug, ArtistName: artist.Name, ArtistIndex: index + 1, ArtistTotal: len(artistsToResolve)}
		emitResolveProgress(progress, event)

		candidates, err := searchArtistCandidates(ctx, r.searcher, artist, c)
		if err != nil {
			report.Errors = append(report.Errors, ResolveError{Slug: artist.Slug, Err: err})
			event.Stage = "artist_failed"
			event.Err = err
			emitResolveProgress(progress, event)
			continue
		}

		event.Stage = "artist_finished"
		event.MatchCount = len(candidates)
		emitResolveProgress(progress, event)
		matches := catalog.RankCandidates(artist, candidates)
		report.Entries = append(report.Entries, ResolveReportEntry{
			Artist:   artist,
			Matches:  matches,
			Decision: catalog.EvaluateEligibility(artist, matches, policy),
		})
	}

	sort.SliceStable(report.Entries, func(i, j int) bool {
		return report.Entries[i].Artist.EditorialOrder < report.Entries[j].Artist.EditorialOrder
	})

	return report, nil
}

func searchArtistCandidates(ctx context.Context, searcher CandidateSearcher, artist catalog.Artist, c catalog.EditorialCatalog) ([]catalog.ArtistCandidate, error) {
	if contextual, ok := searcher.(CatalogAwareCandidateSearcher); ok {
		return contextual.SearchArtistCandidatesWithCatalog(ctx, artist, c)
	}
	return searcher.SearchArtistCandidates(ctx, artist)
}

func unresolvedArtists(artists []catalog.Artist) []catalog.Artist {
	unresolved := make([]catalog.Artist, 0, len(artists))
	for _, artist := range artists {
		if len(artist.AllSpotifyIDs()) > 0 {
			continue
		}
		unresolved = append(unresolved, artist)
	}
	return unresolved
}

func emitResolveProgress(progress func(ResolveProgress), event ResolveProgress) {
	if progress != nil {
		progress(event)
	}
}

func formatValidationIssues(issues []catalog.ValidationIssue) string {
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		messages = append(messages, issue.Error())
	}
	return strings.Join(messages, "; ")
}

type resolveReportJSON struct {
	Entries []resolveEntryJSON `json:"entries"`
	Applied []ResolvedArtist   `json:"applied"`
	Skipped []ResolveSkip      `json:"skipped"`
}

type resolveEntryJSON struct {
	Slug     string                      `json:"slug"`
	Name     string                      `json:"name"`
	Decision catalog.EligibilityDecision `json:"decision"`
}

// FormatResolveReportJSON renders the gate decisions and their evidence in a
// machine-readable form.
func FormatResolveReportJSON(report ResolveReport) ([]byte, error) {
	out := resolveReportJSON{Entries: []resolveEntryJSON{}, Applied: report.Applied, Skipped: report.Skipped}
	if out.Applied == nil {
		out.Applied = []ResolvedArtist{}
	}
	if out.Skipped == nil {
		out.Skipped = []ResolveSkip{}
	}
	for _, entry := range report.Entries {
		out.Entries = append(out.Entries, resolveEntryJSON{Slug: entry.Artist.Slug, Name: entry.Artist.Name, Decision: entry.Decision})
	}
	return json.MarshalIndent(out, "", "  ")
}

func FormatResolveReportMarkdown(report ResolveReport) []byte {
	var buf bytes.Buffer
	buf.WriteString("# Artist Resolution Report\n\n")
	buf.WriteString(fmt.Sprintf("Artists needing review: %d\n\n", len(report.Entries)))
	if len(report.Applied) > 0 {
		buf.WriteString(fmt.Sprintf("Applied automatically: %d\n\n", len(report.Applied)))
		buf.WriteString("| Artist | Spotify ID | Score | Reason |\n")
		buf.WriteString("| --- | --- | ---: | --- |\n")
		for _, applied := range report.Applied {
			buf.WriteString(fmt.Sprintf("| %s | `%s` | %d | %s |\n", escapeMarkdown(applied.Name), applied.SpotifyID, applied.Score, escapeMarkdown(applied.Reason)))
		}
		buf.WriteString("\n")
	}
	if len(report.Skipped) > 0 {
		buf.WriteString(fmt.Sprintf("Skipped automatic updates: %d\n\n", len(report.Skipped)))
		buf.WriteString("| Artist | Reason |\n")
		buf.WriteString("| --- | --- |\n")
		for _, skipped := range report.Skipped {
			buf.WriteString(fmt.Sprintf("| %s | %s |\n", escapeMarkdown(skipped.Name), escapeMarkdown(skipped.Reason)))
		}
		buf.WriteString("\n")
	}

	for _, entry := range report.Entries {
		buf.WriteString(fmt.Sprintf("## %s\n\n", entry.Artist.Name))
		buf.WriteString(fmt.Sprintf("- Slug: `%s`\n", entry.Artist.Slug))
		buf.WriteString(fmt.Sprintf("- Category: `%s`\n", entry.Artist.Category))
		if len(entry.Artist.Aliases) > 0 {
			buf.WriteString(fmt.Sprintf("- Aliases: %s\n", strings.Join(entry.Artist.Aliases, ", ")))
		}
		buf.WriteString(fmt.Sprintf("- Decision: `%s`", entry.Decision.Status))
		if entry.Decision.Evaluated != "" {
			buf.WriteString(fmt.Sprintf(" (evaluated `%s`)", entry.Decision.Evaluated))
		}
		buf.WriteString("\n")
		for _, reason := range entry.Decision.Reasons {
			buf.WriteString(fmt.Sprintf("  - %s\n", escapeMarkdown(reason)))
		}
		writeEvidenceMarkdown(&buf, "Positive evidence", entry.Decision.Positive)
		writeEvidenceMarkdown(&buf, "Negative evidence", entry.Decision.Negative)
		writeEvidenceMarkdown(&buf, "Warnings", entry.Decision.Warnings)
		if len(entry.Matches) == 0 {
			buf.WriteString("- Candidates: none\n\n")
			continue
		}

		buf.WriteString("\n| Score | Confidence | Name | Spotify ID | Popularity | Followers | Genres |\n")
		buf.WriteString("| ---: | --- | --- | --- | ---: | ---: | --- |\n")
		for _, match := range entry.Matches {
			candidate := match.Candidate
			buf.WriteString(fmt.Sprintf(
				"| %d | %s | %s | `%s` | %d | %d | %s |\n",
				match.Score,
				match.Confidence,
				escapeMarkdown(candidate.Name),
				candidate.SpotifyID,
				candidate.Popularity,
				candidate.Followers,
				escapeMarkdown(strings.Join(candidate.Genres, ", ")),
			))
		}
		buf.WriteString("\n")
	}

	if len(report.Errors) > 0 {
		buf.WriteString("## Errors\n\n")
		for _, item := range report.Errors {
			buf.WriteString(fmt.Sprintf("- `%s`: %v\n", item.Slug, item.Err))
		}
	}

	return buf.Bytes()
}

func writeEvidenceMarkdown(buf *bytes.Buffer, title string, items []catalog.IdentityEvidence) {
	if len(items) == 0 {
		return
	}
	buf.WriteString(fmt.Sprintf("- %s:\n", title))
	for _, item := range items {
		buf.WriteString("  - " + FormatIdentityEvidence(item) + "\n")
	}
}

// FormatIdentityEvidence renders one evidence item as a single readable line
// with its source URL, matching release and evaluated ID.
func FormatIdentityEvidence(item catalog.IdentityEvidence) string {
	parts := []string{string(item.Kind)}
	if item.Strong {
		parts = append(parts, "strong")
	}
	if item.ReleaseName != "" {
		release := item.ReleaseName
		if item.ReleaseYear != "" {
			release += " (" + item.ReleaseYear + ")"
		}
		parts = append(parts, "release: "+release)
	}
	if item.Detail != "" {
		parts = append(parts, item.Detail)
	}
	if item.SubjectID != "" {
		parts = append(parts, "id: "+item.SubjectID)
	}
	if item.URL != "" {
		parts = append(parts, item.URL)
	}
	return escapeMarkdown(strings.Join(parts, " | "))
}

func escapeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}
