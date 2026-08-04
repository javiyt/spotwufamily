package catalogexport

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const FormatVersion = 1

type Reader interface {
	LoadExportCatalog(context.Context) (Catalog, error)
}

type Writer interface {
	WriteFile(path string, data []byte) (bool, error)
	PruneDir(dir string, keep map[string]struct{}) error
}

type ExportCatalog struct {
	reader Reader
	writer Writer
}

func NewExportCatalog(reader Reader, writer Writer) ExportCatalog {
	return ExportCatalog{reader: reader, writer: writer}
}

type Options struct {
	OutputDir  string
	StaticDir  string
	ContentDir string
}

type Report struct {
	FilesWritten int
	FilesKept    int
	Artists      int
	Albums       int
	Tracks       int
}

type Catalog struct {
	Artists []Artist
	Albums  []Album
	Tracks  []Track
	Stats   Stats
}

type Stats struct {
	Artists       int `json:"artists"`
	Groups        int `json:"groups"`
	Albums        int `json:"albums"`
	Tracks        int `json:"tracks"`
	Appearances   int `json:"appearances"`
	LastSyncRunID int `json:"last_sync_run_id,omitempty"`
}

type Artist struct {
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	PublicName    string   `json:"public_name,omitempty"`
	SpotifyID     string   `json:"spotify_id,omitempty"`
	SpotifyIDs    []string `json:"spotify_ids,omitempty"`
	Category      string   `json:"category"`
	Aliases       []string `json:"aliases"`
	Enabled       bool     `json:"enabled"`
	ReleaseCount  int      `json:"release_count"`
	TrackCount    int      `json:"track_count"`
	SpotifyURL    string   `json:"spotify_url,omitempty"`
	ImageURL      string   `json:"image_url,omitempty"`
	EditorialRank int      `json:"editorial_order,omitempty"`
}

type Album struct {
	SpotifyID   string        `json:"spotify_id"`
	Name        string        `json:"name"`
	AlbumType   string        `json:"album_type"`
	ReleaseDate string        `json:"release_date,omitempty"`
	Label       string        `json:"label,omitempty"`
	TotalTracks int           `json:"total_tracks"`
	SpotifyURL  string        `json:"spotify_url,omitempty"`
	ImageURL    string        `json:"image_url,omitempty"`
	Artists     []Credit      `json:"artists"`
	Related     []GroupCredit `json:"related_artists,omitempty"`
	Tracks      []AlbumTrack  `json:"tracks,omitempty"`
	Copyrights  []Copyright   `json:"copyrights,omitempty"`
}

type Track struct {
	SpotifyID    string        `json:"spotify_id"`
	Name         string        `json:"name"`
	DurationMS   int           `json:"duration_ms"`
	Explicit     bool          `json:"explicit"`
	ISRC         string        `json:"isrc,omitempty"`
	SpotifyURL   string        `json:"spotify_url,omitempty"`
	PreviewURL   string        `json:"preview_url,omitempty"`
	Artists      []Credit      `json:"artists"`
	Albums       []Credit      `json:"albums,omitempty"`
	ReleaseYears []string      `json:"release_years,omitempty"`
	Groups       []GroupCredit `json:"groups,omitempty"`
}

type AlbumTrack struct {
	SpotifyID   string   `json:"spotify_id"`
	Name        string   `json:"name"`
	DiscNumber  int      `json:"disc_number"`
	TrackNumber int      `json:"track_number"`
	DurationMS  int      `json:"duration_ms"`
	Explicit    bool     `json:"explicit"`
	Artists     []Credit `json:"artists"`
}

type Credit struct {
	SpotifyID string `json:"spotify_id,omitempty"`
	Name      string `json:"name"`
}

type GroupCredit struct {
	Slug      string `json:"slug"`
	SpotifyID string `json:"spotify_id,omitempty"`
	Name      string `json:"name"`
	Category  string `json:"category"`
}

type Copyright struct {
	Text string `json:"text"`
	Type string `json:"type"`
}

type Summary struct {
	FormatVersion int      `json:"format_version"`
	Stats         Stats    `json:"stats"`
	RecentAlbums  []Album  `json:"recent_albums"`
	Featured      []Artist `json:"featured_artists"`
}

type Index[T any] struct {
	FormatVersion int `json:"format_version"`
	Items         []T `json:"items"`
}

type SearchIndex struct {
	FormatVersion int          `json:"format_version"`
	Items         []SearchItem `json:"items"`
}

type SearchItem struct {
	Type     string   `json:"type"`
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	URL      string   `json:"url"`
	Terms    []string `json:"terms"`
}

func (e ExportCatalog) Run(ctx context.Context, options Options) (Report, error) {
	if options.OutputDir == "" {
		return Report{}, fmt.Errorf("output directory is required")
	}
	if options.StaticDir == "" {
		return Report{}, fmt.Errorf("static directory is required")
	}
	if options.ContentDir == "" {
		return Report{}, fmt.Errorf("content directory is required")
	}

	catalog, err := e.reader.LoadExportCatalog(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load export catalog: %w", err)
	}
	sortCatalog(&catalog)

	report := Report{Artists: len(catalog.Artists), Albums: len(catalog.Albums), Tracks: len(catalog.Tracks)}
	keepGenerated := map[string]struct{}{}
	keepStatic := map[string]struct{}{}
	keepContent := map[string]struct{}{}

	write := func(path string, data []byte, keep map[string]struct{}) error {
		keep[path] = struct{}{}
		changed, err := e.writer.WriteFile(path, data)
		if err != nil {
			return err
		}
		if changed {
			report.FilesWritten++
		} else {
			report.FilesKept++
		}
		return nil
	}

	if err := write(filepath.Join(options.OutputDir, "catalog-summary.json"), mustJSON(summary(catalog)), keepGenerated); err != nil {
		return report, err
	}
	if err := write(filepath.Join(options.ContentDir, "_index.md"), generatedSectionPage("Generated catalog pages"), keepContent); err != nil {
		return report, err
	}
	if err := write(filepath.Join(options.ContentDir, "artists", "_index.md"), generatedSectionPage("Generated artist pages"), keepContent); err != nil {
		return report, err
	}
	if err := write(filepath.Join(options.ContentDir, "albums", "_index.md"), generatedSectionPage("Generated album pages"), keepContent); err != nil {
		return report, err
	}
	if err := write(filepath.Join(options.OutputDir, "artists", "index.json"), mustJSON(Index[Artist]{FormatVersion: FormatVersion, Items: catalog.Artists}), keepGenerated); err != nil {
		return report, err
	}
	for _, artist := range catalog.Artists {
		if err := write(filepath.Join(options.OutputDir, "artists", artist.Slug+".json"), mustJSON(Index[Artist]{FormatVersion: FormatVersion, Items: []Artist{artist}}), keepGenerated); err != nil {
			return report, err
		}
		if err := write(filepath.Join(options.ContentDir, "artists", artist.Slug+".md"), detailPage("artist", artist.Name, "/artists/"+artist.Slug+"/"), keepContent); err != nil {
			return report, err
		}
	}
	if err := write(filepath.Join(options.OutputDir, "albums", "index.json"), mustJSON(Index[Album]{FormatVersion: FormatVersion, Items: albumIndex(catalog.Albums)}), keepGenerated); err != nil {
		return report, err
	}
	for _, album := range catalog.Albums {
		if err := write(filepath.Join(options.OutputDir, "albums", album.SpotifyID+".json"), mustJSON(album), keepGenerated); err != nil {
			return report, err
		}
		if err := write(filepath.Join(options.ContentDir, "albums", album.SpotifyID+".md"), detailPage("album", album.Name, "/albums/"+album.SpotifyID+"/"), keepContent); err != nil {
			return report, err
		}
	}
	if err := write(filepath.Join(options.OutputDir, "tracks", "index.json"), mustJSON(Index[Track]{FormatVersion: FormatVersion, Items: trackIndex(catalog)}), keepGenerated); err != nil {
		return report, err
	}
	for _, track := range catalog.Tracks {
		if err := write(filepath.Join(options.OutputDir, "tracks", track.SpotifyID+".json"), mustJSON(track), keepGenerated); err != nil {
			return report, err
		}
	}
	searchPath := filepath.Join(options.StaticDir, "search-index.json")
	if err := write(searchPath, mustJSON(searchIndex(catalog)), keepStatic); err != nil {
		return report, err
	}

	if err := e.writer.PruneDir(options.OutputDir, keepGenerated); err != nil {
		return report, err
	}
	if err := e.writer.PruneDir(options.StaticDir, keepStatic); err != nil {
		return report, err
	}
	if err := e.writer.PruneDir(options.ContentDir, keepContent); err != nil {
		return report, err
	}

	return report, nil
}

func detailPage(layout, title, url string) []byte {
	return []byte(fmt.Sprintf("---\ntitle: %q\nlayout: %q\nurl: %q\nbuild:\n  list: never\n---\n", title, layout, url))
}

func generatedSectionPage(title string) []byte {
	return []byte(fmt.Sprintf("---\ntitle: %q\nbuild:\n  render: never\n  list: never\n---\n", title))
}

func sortCatalog(catalog *Catalog) {
	sort.SliceStable(catalog.Artists, func(i, j int) bool {
		if catalog.Artists[i].EditorialRank != catalog.Artists[j].EditorialRank {
			return catalog.Artists[i].EditorialRank < catalog.Artists[j].EditorialRank
		}
		return catalog.Artists[i].Name < catalog.Artists[j].Name
	})
	sort.SliceStable(catalog.Albums, func(i, j int) bool {
		if catalog.Albums[i].ReleaseDate != catalog.Albums[j].ReleaseDate {
			return catalog.Albums[i].ReleaseDate > catalog.Albums[j].ReleaseDate
		}
		return catalog.Albums[i].Name < catalog.Albums[j].Name
	})
	sort.SliceStable(catalog.Tracks, func(i, j int) bool {
		return catalog.Tracks[i].Name < catalog.Tracks[j].Name
	})
}

func summary(catalog Catalog) Summary {
	recentLimit := min(10, len(catalog.Albums))
	featuredLimit := min(12, len(catalog.Artists))

	return Summary{
		FormatVersion: FormatVersion,
		Stats:         catalog.Stats,
		RecentAlbums:  albumIndex(catalog.Albums[:recentLimit]),
		Featured:      catalog.Artists[:featuredLimit],
	}
}

func albumIndex(albums []Album) []Album {
	items := make([]Album, 0, len(albums))
	for _, album := range albums {
		album.Tracks = nil
		album.Copyrights = nil
		items = append(items, album)
	}

	return items
}

func trackIndex(catalog Catalog) []Track {
	yearsByTrack := trackReleaseYears(catalog.Albums)
	groupsByTrack := trackGroups(catalog.Artists, catalog.Tracks)
	items := make([]Track, 0, len(catalog.Tracks))
	for _, track := range catalog.Tracks {
		track.Albums = nil
		track.ReleaseYears = yearsByTrack[track.SpotifyID]
		track.Groups = groupsByTrack[track.SpotifyID]
		items = append(items, track)
	}

	return items
}

func trackReleaseYears(albums []Album) map[string][]string {
	years := map[string]map[string]struct{}{}
	for _, album := range albums {
		if len(album.ReleaseDate) < 4 {
			continue
		}
		year := album.ReleaseDate[:4]
		for _, track := range album.Tracks {
			if _, ok := years[track.SpotifyID]; !ok {
				years[track.SpotifyID] = map[string]struct{}{}
			}
			years[track.SpotifyID][year] = struct{}{}
		}
	}

	result := map[string][]string{}
	for trackID, trackYears := range years {
		values := make([]string, 0, len(trackYears))
		for year := range trackYears {
			values = append(values, year)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(values)))
		result[trackID] = values
	}

	return result
}

func trackGroups(artists []Artist, tracks []Track) map[string][]GroupCredit {
	matchers := configuredArtistMatchers(artists)
	result := map[string][]GroupCredit{}
	for _, track := range tracks {
		matches := []GroupCredit{}
		seen := map[string]struct{}{}
		for _, artist := range track.Artists {
			keys := []string{artist.SpotifyID, artist.Name}
			for _, matcher := range matchers {
				if _, ok := seen[matcher.group.Slug]; ok {
					continue
				}
				if matcher.matches(keys) {
					seen[matcher.group.Slug] = struct{}{}
					matches = append(matches, matcher.group)
				}
			}
		}
		if len(matches) > 0 {
			result[track.SpotifyID] = matches
		}
	}

	return result
}

type artistMatcher struct {
	group GroupCredit
	keys  map[string]struct{}
}

func configuredArtistMatchers(artists []Artist) []artistMatcher {
	matchers := make([]artistMatcher, 0, len(artists))
	for _, artist := range artists {
		keys := map[string]struct{}{}
		addMatcherKey(keys, artist.SpotifyID)
		for _, spotifyID := range artist.SpotifyIDs {
			addMatcherKey(keys, spotifyID)
		}
		addMatcherKey(keys, artist.Name)
		addMatcherKey(keys, artist.PublicName)
		for _, alias := range artist.Aliases {
			addMatcherKey(keys, alias)
		}
		if len(keys) == 0 {
			continue
		}
		matchers = append(matchers, artistMatcher{
			group: GroupCredit{Slug: artist.Slug, SpotifyID: artist.SpotifyID, Name: artist.Name, Category: artist.Category},
			keys:  keys,
		})
	}

	return matchers
}

func (m artistMatcher) matches(values []string) bool {
	for _, value := range values {
		if _, ok := m.keys[matcherKey(value)]; ok {
			return true
		}
	}

	return false
}

func addMatcherKey(keys map[string]struct{}, value string) {
	value = matcherKey(value)
	if value == "" {
		return
	}
	keys[value] = struct{}{}
}

func matcherKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func searchIndex(catalog Catalog) SearchIndex {
	items := make([]SearchItem, 0, len(catalog.Artists)+len(catalog.Albums)+len(catalog.Tracks))
	for _, artist := range catalog.Artists {
		items = append(items, SearchItem{
			Type:     "artist",
			ID:       artist.Slug,
			Title:    artist.Name,
			Subtitle: artist.Category,
			URL:      "/artists/" + artist.Slug + "/",
			Terms:    normalizeTerms(append([]string{artist.Name, artist.PublicName}, artist.Aliases...)),
		})
	}
	for _, album := range catalog.Albums {
		items = append(items, SearchItem{
			Type:     "album",
			ID:       album.SpotifyID,
			Title:    album.Name,
			Subtitle: album.ReleaseDate,
			URL:      "/albums/" + album.SpotifyID + "/",
			Terms:    normalizeTerms([]string{album.Name, joinedCredits(album.Artists)}),
		})
	}
	for _, track := range catalog.Tracks {
		items = append(items, SearchItem{
			Type:     "track",
			ID:       track.SpotifyID,
			Title:    track.Name,
			Subtitle: joinedCredits(track.Artists),
			URL:      "/tracks/#" + track.SpotifyID,
			Terms:    normalizeTerms([]string{track.Name, track.ISRC, joinedCredits(track.Artists)}),
		})
	}

	return SearchIndex{FormatVersion: FormatVersion, Items: items}
}

func joinedCredits(credits []Credit) string {
	names := make([]string, 0, len(credits))
	for _, credit := range credits {
		names = append(names, credit.Name)
	}

	return strings.Join(names, " ")
}

func normalizeTerms(values []string) []string {
	seen := map[string]struct{}{}
	terms := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.Join(strings.Fields(value), " "))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		terms = append(terms, value)
	}
	sort.Strings(terms)

	return terms
}

func mustJSON(value any) []byte {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}

	return append(data, '\n')
}
