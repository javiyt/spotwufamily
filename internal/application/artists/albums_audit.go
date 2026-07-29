package artists

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type SpotifyAlbumFetcher interface {
	GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error)
}

type MusicBrainzReleaseGroupSearcher interface {
	SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]MusicBrainzReleaseGroup, error)
}

type MusicBrainzReleaseGroup struct {
	ID               string
	Title            string
	FirstReleaseDate string
	URL              string
}

type AuditAlbums struct {
	store       CatalogStore
	spotify     SpotifyAlbumFetcher
	musicBrainz MusicBrainzReleaseGroupSearcher
}

func NewAuditAlbums(store CatalogStore, spotify SpotifyAlbumFetcher, musicBrainz MusicBrainzReleaseGroupSearcher) AuditAlbums {
	return AuditAlbums{store: store, spotify: spotify, musicBrainz: musicBrainz}
}

type AuditAlbumsOptions struct {
	CatalogPath string
	ArtistSlug  string
}

type AuditAlbumsReport struct {
	Artists []AuditAlbumsArtistReport
	Errors  []AuditAlbumsError
}

type AuditAlbumsArtistReport struct {
	Artist            catalog.Artist
	SpotifyAlbums     []AuditedAlbum
	MusicBrainzAlbums []AuditedAlbum
	Matched           []AlbumMatch
	MissingSpotify    []AuditedAlbum
	SuspiciousSpotify []AuditedAlbum
}

type AuditedAlbum struct {
	ID       string
	Title    string
	Date     string
	Year     string
	URL      string
	SourceID string
}

type AlbumMatch struct {
	Spotify     AuditedAlbum
	MusicBrainz AuditedAlbum
}

type AuditAlbumsError struct {
	Slug string
	Err  error
}

func (a AuditAlbums) Run(ctx context.Context, options AuditAlbumsOptions) (AuditAlbumsReport, error) {
	c, err := a.store.Load(ctx, options.CatalogPath)
	if err != nil {
		return AuditAlbumsReport{}, fmt.Errorf("load artist catalog: %w", err)
	}
	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		return AuditAlbumsReport{}, fmt.Errorf("artist catalog is invalid: %s", issues[0].Error())
	}

	report := AuditAlbumsReport{}
	for _, artist := range c.Artists {
		if options.ArtistSlug != "" && artist.Slug != options.ArtistSlug {
			continue
		}
		if len(artist.AllSpotifyIDs()) == 0 {
			continue
		}

		artistReport, err := a.auditArtist(ctx, artist)
		if err != nil {
			report.Errors = append(report.Errors, AuditAlbumsError{Slug: artist.Slug, Err: err})
			continue
		}
		report.Artists = append(report.Artists, artistReport)
	}
	if options.ArtistSlug != "" && len(report.Artists) == 0 && len(report.Errors) == 0 {
		return report, fmt.Errorf("artist %q with Spotify IDs not found", options.ArtistSlug)
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("%d artist album audits failed", len(report.Errors))
	}

	return report, nil
}

func (a AuditAlbums) auditArtist(ctx context.Context, artist catalog.Artist) (AuditAlbumsArtistReport, error) {
	spotifyAlbums, err := a.spotifyAlbums(ctx, artist)
	if err != nil {
		return AuditAlbumsArtistReport{}, err
	}
	musicBrainzReleaseGroups, err := a.musicBrainz.SearchArtistAlbumReleaseGroups(ctx, artist)
	if err != nil {
		return AuditAlbumsArtistReport{}, fmt.Errorf("search MusicBrainz release groups: %w", err)
	}

	mbAlbums := make([]AuditedAlbum, 0, len(musicBrainzReleaseGroups))
	for _, releaseGroup := range musicBrainzReleaseGroups {
		mbAlbums = append(mbAlbums, AuditedAlbum{
			ID:    releaseGroup.ID,
			Title: releaseGroup.Title,
			Date:  releaseGroup.FirstReleaseDate,
			Year:  yearFromDate(releaseGroup.FirstReleaseDate),
			URL:   releaseGroup.URL,
		})
	}

	matched, missingSpotify, suspiciousSpotify := compareAlbums(spotifyAlbums, mbAlbums)
	return AuditAlbumsArtistReport{
		Artist:            artist,
		SpotifyAlbums:     spotifyAlbums,
		MusicBrainzAlbums: mbAlbums,
		Matched:           matched,
		MissingSpotify:    missingSpotify,
		SuspiciousSpotify: suspiciousSpotify,
	}, nil
}

func (a AuditAlbums) spotifyAlbums(ctx context.Context, artist catalog.Artist) ([]AuditedAlbum, error) {
	seen := map[string]struct{}{}
	var albums []AuditedAlbum
	for _, spotifyID := range artist.AllSpotifyIDs() {
		releases, err := a.spotify.GetArtistAlbums(ctx, spotifyID, []string{"album"})
		if err != nil {
			return nil, fmt.Errorf("get Spotify albums %s: %w", spotifyID, err)
		}
		for _, release := range releases {
			if release.SpotifyID == "" {
				continue
			}
			if _, ok := seen[release.SpotifyID]; ok {
				continue
			}
			seen[release.SpotifyID] = struct{}{}
			albums = append(albums, AuditedAlbum{
				ID:       release.SpotifyID,
				Title:    release.Name,
				Date:     release.ReleaseDate,
				Year:     yearFromDate(release.ReleaseDate),
				URL:      release.URL,
				SourceID: spotifyID,
			})
		}
	}
	sortAlbums(albums)
	return albums, nil
}

func compareAlbums(spotifyAlbums, musicBrainzAlbums []AuditedAlbum) ([]AlbumMatch, []AuditedAlbum, []AuditedAlbum) {
	spotifyByKey := map[string][]AuditedAlbum{}
	spotifyByTitle := map[string][]AuditedAlbum{}
	for _, album := range spotifyAlbums {
		key := albumKey(album)
		if key == "" {
			continue
		}
		spotifyByKey[key] = append(spotifyByKey[key], album)
		title := normalizeAlbumTitle(album.Title)
		if title != "" {
			spotifyByTitle[title] = append(spotifyByTitle[title], album)
		}
	}

	matchedSpotifyIDs := map[string]struct{}{}
	var matched []AlbumMatch
	var missingSpotify []AuditedAlbum
	for _, mbAlbum := range musicBrainzAlbums {
		key := albumKey(mbAlbum)
		spotifyAlbum, ok := firstUnusedAlbum(spotifyByKey[key], matchedSpotifyIDs)
		if !ok {
			spotifyAlbum, ok = firstUnusedUniqueAlbum(spotifyByTitle[normalizeAlbumTitle(mbAlbum.Title)], matchedSpotifyIDs)
		}
		if !ok {
			missingSpotify = append(missingSpotify, mbAlbum)
			continue
		}
		matchedSpotifyIDs[spotifyAlbum.ID] = struct{}{}
		matched = append(matched, AlbumMatch{Spotify: spotifyAlbum, MusicBrainz: mbAlbum})
	}

	var suspiciousSpotify []AuditedAlbum
	for _, album := range spotifyAlbums {
		if _, ok := matchedSpotifyIDs[album.ID]; ok {
			continue
		}
		suspiciousSpotify = append(suspiciousSpotify, album)
	}

	sort.Slice(matched, func(i, j int) bool {
		return albumLess(matched[i].MusicBrainz, matched[j].MusicBrainz)
	})
	sortAlbums(missingSpotify)
	sortAlbums(suspiciousSpotify)
	return matched, missingSpotify, suspiciousSpotify
}

func firstUnusedAlbum(albums []AuditedAlbum, used map[string]struct{}) (AuditedAlbum, bool) {
	for _, album := range albums {
		if _, ok := used[album.ID]; ok {
			continue
		}
		return album, true
	}
	return AuditedAlbum{}, false
}

func firstUnusedUniqueAlbum(albums []AuditedAlbum, used map[string]struct{}) (AuditedAlbum, bool) {
	var candidate AuditedAlbum
	found := false
	for _, album := range albums {
		if _, ok := used[album.ID]; ok {
			continue
		}
		if found {
			return AuditedAlbum{}, false
		}
		candidate = album
		found = true
	}
	return candidate, found
}

func FormatAuditAlbumsMarkdown(report AuditAlbumsReport) []byte {
	var buf bytes.Buffer
	buf.WriteString("# Artist Album Audit\n\n")
	if len(report.Artists) == 0 {
		buf.WriteString("No artists audited.\n")
	}
	for _, artistReport := range report.Artists {
		buf.WriteString(fmt.Sprintf("## %s (%s)\n\n", artistReport.Artist.Name, artistReport.Artist.Slug))
		buf.WriteString("Spotify IDs:\n")
		for _, spotifyID := range artistReport.Artist.AllSpotifyIDs() {
			buf.WriteString(fmt.Sprintf("- `%s` | https://open.spotify.com/artist/%s\n", spotifyID, spotifyID))
		}
		buf.WriteString("\n")
		buf.WriteString(fmt.Sprintf("Summary: matched=%d missing_from_spotify=%d suspicious_spotify=%d spotify_albums=%d musicbrainz_albums=%d\n\n",
			len(artistReport.Matched),
			len(artistReport.MissingSpotify),
			len(artistReport.SuspiciousSpotify),
			len(artistReport.SpotifyAlbums),
			len(artistReport.MusicBrainzAlbums),
		))
		writeAlbumMatches(&buf, "Matched", artistReport.Matched)
		writeAlbums(&buf, "Missing From Spotify", artistReport.MissingSpotify)
		writeAlbums(&buf, "Suspicious Spotify Albums", artistReport.SuspiciousSpotify)
	}
	if len(report.Errors) > 0 {
		buf.WriteString("## Errors\n\n")
		for _, item := range report.Errors {
			buf.WriteString(fmt.Sprintf("- `%s`: %v\n", item.Slug, item.Err))
		}
	}
	return buf.Bytes()
}

func writeAlbumMatches(buf *bytes.Buffer, title string, matches []AlbumMatch) {
	buf.WriteString(fmt.Sprintf("### %s\n\n", title))
	if len(matches) == 0 {
		buf.WriteString("None.\n\n")
		return
	}
	buf.WriteString("| MusicBrainz | Spotify |\n")
	buf.WriteString("| --- | --- |\n")
	for _, match := range matches {
		buf.WriteString(fmt.Sprintf("| %s | %s |\n", formatAlbum(match.MusicBrainz), formatAlbum(match.Spotify)))
	}
	buf.WriteString("\n")
}

func writeAlbums(buf *bytes.Buffer, title string, albums []AuditedAlbum) {
	buf.WriteString(fmt.Sprintf("### %s\n\n", title))
	if len(albums) == 0 {
		buf.WriteString("None.\n\n")
		return
	}
	for _, album := range albums {
		buf.WriteString(fmt.Sprintf("- %s\n", formatAlbum(album)))
	}
	buf.WriteString("\n")
}

func formatAlbum(album AuditedAlbum) string {
	title := escapeMarkdown(album.Title)
	if album.Year != "" {
		title += " (" + album.Year + ")"
	}
	if album.URL != "" {
		title += " - " + album.URL
	}
	if album.SourceID != "" {
		title += " [`" + album.SourceID + "`]"
	}
	return title
}

func albumKey(album AuditedAlbum) string {
	title := normalizeAlbumTitle(album.Title)
	if title == "" {
		return ""
	}
	if album.Year == "" {
		return title
	}
	return title + "|" + album.Year
}

var albumQualifierPattern = regexp.MustCompile(`(?i)\b(deluxe|expanded|remaster(?:ed)?|anniversary|edition|explicit|clean|bonus|digital|reissue|version|instrumentals?)\b`)
var nonAlphaNumericPattern = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeAlbumTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = strings.ReplaceAll(title, "&", " and ")
	title = albumQualifierPattern.ReplaceAllString(title, " ")
	title = nonAlphaNumericPattern.ReplaceAllString(title, " ")
	return strings.Join(strings.Fields(title), " ")
}

func yearFromDate(date string) string {
	if len(date) < 4 {
		return ""
	}
	year := date[:4]
	if _, err := strconv.Atoi(year); err != nil {
		return ""
	}
	return year
}

func sortAlbums(albums []AuditedAlbum) {
	sort.Slice(albums, func(i, j int) bool {
		return albumLess(albums[i], albums[j])
	})
}

func albumLess(left, right AuditedAlbum) bool {
	if left.Year != right.Year {
		return left.Year < right.Year
	}
	if normalizeAlbumTitle(left.Title) != normalizeAlbumTitle(right.Title) {
		return normalizeAlbumTitle(left.Title) < normalizeAlbumTitle(right.Title)
	}
	return left.ID < right.ID
}
