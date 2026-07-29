package artists

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type CandidateSearcher interface {
	SearchArtistCandidates(context.Context, catalog.Artist) ([]catalog.ArtistCandidate, error)
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
	Artist  catalog.Artist
	Matches []catalog.CandidateMatch
}

type ResolvedArtist struct {
	Slug      string
	Name      string
	SpotifyID string
	Score     int
	Reason    string
}

type ResolveSkip struct {
	Slug   string
	Name   string
	Reason string
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

	return r.resolve(ctx, c, progress)
}

func (r ResolveArtists) Apply(ctx context.Context, path string, options ApplyResolveOptions) (ResolveReport, error) {
	c, err := r.store.Load(ctx, path)
	if err != nil {
		return ResolveReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	report, err := r.resolve(ctx, c, options.Progress)
	if err != nil {
		return ResolveReport{}, err
	}
	if len(report.Errors) > 0 {
		return report, nil
	}

	if options.MinScore == 0 {
		options.MinScore = 95
	}
	if options.MinScoreGap == 0 {
		options.MinScoreGap = 10
	}

	updates := map[string]ResolvedArtist{}
	for _, entry := range report.Entries {
		resolved, ok, reason := autoResolvedMatch(entry, options)
		if !ok {
			report.Skipped = append(report.Skipped, ResolveSkip{Slug: entry.Artist.Slug, Name: entry.Artist.Name, Reason: reason})
			continue
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

func (r ResolveArtists) resolve(ctx context.Context, c catalog.EditorialCatalog, progress func(ResolveProgress)) (ResolveReport, error) {
	report := ResolveReport{}
	artistsToResolve := unresolvedArtists(c.Artists)
	for index, artist := range artistsToResolve {
		event := ResolveProgress{Stage: "artist_started", ArtistSlug: artist.Slug, ArtistName: artist.Name, ArtistIndex: index + 1, ArtistTotal: len(artistsToResolve)}
		emitResolveProgress(progress, event)

		candidates, err := r.searcher.SearchArtistCandidates(ctx, artist)
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
		report.Entries = append(report.Entries, ResolveReportEntry{
			Artist:  artist,
			Matches: catalog.RankCandidates(artist, candidates),
		})
	}

	sort.SliceStable(report.Entries, func(i, j int) bool {
		return report.Entries[i].Artist.EditorialOrder < report.Entries[j].Artist.EditorialOrder
	})

	return report, nil
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

func autoResolvedMatch(entry ResolveReportEntry, options ApplyResolveOptions) (ResolvedArtist, bool, string) {
	if len(entry.Matches) == 0 {
		return ResolvedArtist{}, false, "no candidates"
	}

	best := entry.Matches[0]
	if best.Candidate.SpotifyID == "" {
		return ResolvedArtist{}, false, "best candidate has no Spotify ID"
	}
	if best.Score < options.MinScore {
		return ResolvedArtist{}, false, fmt.Sprintf("best score %d is below minimum %d", best.Score, options.MinScore)
	}
	if len(entry.Matches) > 1 && best.Score-entry.Matches[1].Score < options.MinScoreGap {
		return ResolvedArtist{}, false, fmt.Sprintf("ambiguous top candidates: score gap %d is below minimum %d", best.Score-entry.Matches[1].Score, options.MinScoreGap)
	}

	return ResolvedArtist{
		Slug:      entry.Artist.Slug,
		Name:      entry.Artist.Name,
		SpotifyID: best.Candidate.SpotifyID,
		Score:     best.Score,
		Reason:    best.Reason,
	}, true, ""
}

func formatValidationIssues(issues []catalog.ValidationIssue) string {
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		messages = append(messages, issue.Error())
	}
	return strings.Join(messages, "; ")
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

func escapeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}
