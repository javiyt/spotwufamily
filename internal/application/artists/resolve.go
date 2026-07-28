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
}

type ResolveReportEntry struct {
	Artist  catalog.Artist
	Matches []catalog.CandidateMatch
}

type ResolveError struct {
	Slug string
	Err  error
}

func (r ResolveArtists) Run(ctx context.Context, path string) (ResolveReport, error) {
	c, err := r.store.Load(ctx, path)
	if err != nil {
		return ResolveReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	report := ResolveReport{}
	for _, artist := range c.Artists {
		if artist.SpotifyID != "" {
			continue
		}

		candidates, err := r.searcher.SearchArtistCandidates(ctx, artist)
		if err != nil {
			report.Errors = append(report.Errors, ResolveError{Slug: artist.Slug, Err: err})
			continue
		}

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

func FormatResolveReportMarkdown(report ResolveReport) []byte {
	var buf bytes.Buffer
	buf.WriteString("# Artist Resolution Report\n\n")
	buf.WriteString(fmt.Sprintf("Artists needing review: %d\n\n", len(report.Entries)))

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
