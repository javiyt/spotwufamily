package artists

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type SpotifyArtistFetcher interface {
	GetArtist(context.Context, string) (catalog.ArtistCandidate, error)
}

type MetadataRefreshCheckpointStore interface {
	LastArtistMetadataRefresh(context.Context, string) (time.Time, bool, error)
	SaveArtistMetadataRefresh(context.Context, string, []string, time.Time) error
}

type RefreshGenres struct {
	store   CatalogStore
	fetcher SpotifyArtistFetcher
}

const DefaultRefreshGenresTTL = 7 * 24 * time.Hour

func NewRefreshGenres(store CatalogStore, fetcher SpotifyArtistFetcher) RefreshGenres {
	return RefreshGenres{store: store, fetcher: fetcher}
}

type RefreshGenresOptions struct {
	CatalogPath     string
	DryRun          bool
	Force           bool
	RefreshTTL      time.Duration
	CheckpointStore MetadataRefreshCheckpointStore
	Progress        func(RefreshGenresProgress)
}

type RefreshGenresReport struct {
	ArtistsWithIDs int
	Updated        int
	Unchanged      int
	SkippedRecent  int
	WithoutGenres  []string
	WithoutImages  []string
	Errors         []RefreshGenresError
}

type RefreshGenresError struct {
	Slug string
	ID   string
	Err  error
}

type RefreshGenresProgress struct {
	Stage          string
	ArtistSlug     string
	ArtistName     string
	SpotifyID      string
	ArtistIndex    int
	ArtistTotal    int
	SpotifyIDIndex int
	SpotifyIDTotal int
	Skipped        bool
	LastRefresh    time.Time
	RefreshTTL     time.Duration
	Updated        bool
	Unchanged      bool
	Err            error
}

func (r RefreshGenres) Run(ctx context.Context, options RefreshGenresOptions) (RefreshGenresReport, error) {
	c, err := r.store.Load(ctx, options.CatalogPath)
	if err != nil {
		return RefreshGenresReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	report := RefreshGenresReport{}
	refreshTTL := options.RefreshTTL
	if refreshTTL == 0 {
		refreshTTL = DefaultRefreshGenresTTL
	}
	artistsWithIDs := countArtistsWithSpotifyIDs(c.Artists)
	for index := range c.Artists {
		artist := &c.Artists[index]
		ids := artist.AllSpotifyIDs()
		if len(ids) == 0 {
			continue
		}
		report.ArtistsWithIDs++
		artistIndex := report.ArtistsWithIDs
		if !options.Force && options.CheckpointStore != nil {
			lastRefresh, ok, err := options.CheckpointStore.LastArtistMetadataRefresh(ctx, artist.Slug)
			if err != nil {
				return report, err
			}
			if ok && time.Since(lastRefresh) < refreshTTL {
				report.SkippedRecent++
				emitRefreshGenresProgress(options.Progress, RefreshGenresProgress{
					Stage:       "artist_skipped",
					ArtistSlug:  artist.Slug,
					ArtistName:  artist.Name,
					ArtistIndex: artistIndex,
					ArtistTotal: artistsWithIDs,
					Skipped:     true,
					LastRefresh: lastRefresh,
					RefreshTTL:  refreshTTL,
				})
				continue
			}
		}
		emitRefreshGenresProgress(options.Progress, RefreshGenresProgress{
			Stage:       "artist_started",
			ArtistSlug:  artist.Slug,
			ArtistName:  artist.Name,
			ArtistIndex: artistIndex,
			ArtistTotal: artistsWithIDs,
		})

		genres := []string{}
		externalURL := ""
		imageURL := ""
		artistFailed := false
		for spotifyIndex, spotifyID := range ids {
			progress := RefreshGenresProgress{
				Stage:          "spotify_started",
				ArtistSlug:     artist.Slug,
				ArtistName:     artist.Name,
				SpotifyID:      spotifyID,
				ArtistIndex:    artistIndex,
				ArtistTotal:    artistsWithIDs,
				SpotifyIDIndex: spotifyIndex + 1,
				SpotifyIDTotal: len(ids),
			}
			emitRefreshGenresProgress(options.Progress, progress)
			spotifyArtist, err := r.fetcher.GetArtist(ctx, spotifyID)
			if err != nil {
				report.Errors = append(report.Errors, RefreshGenresError{Slug: artist.Slug, ID: spotifyID, Err: err})
				artistFailed = true
				progress.Stage = "spotify_failed"
				progress.Err = err
				emitRefreshGenresProgress(options.Progress, progress)
				continue
			}
			progress.Stage = "spotify_finished"
			emitRefreshGenresProgress(options.Progress, progress)
			genres = append(genres, spotifyArtist.Genres...)
			if externalURL == "" {
				externalURL = spotifyArtist.URL
			}
			if imageURL == "" {
				imageURL = spotifyArtist.ImageURL
			}
		}
		if artistFailed {
			continue
		}
		genres = normalizeGenreList(genres)
		if len(genres) == 0 {
			report.WithoutGenres = append(report.WithoutGenres, artist.Slug)
		}
		if imageURL == "" {
			report.WithoutImages = append(report.WithoutImages, artist.Slug)
		}
		if equalStringSlices(artist.Genres, genres) && artist.ExternalURL == externalURL && artist.ImageURL == imageURL {
			report.Unchanged++
			if !options.DryRun && options.CheckpointStore != nil {
				if err := options.CheckpointStore.SaveArtistMetadataRefresh(ctx, artist.Slug, ids, time.Now().UTC()); err != nil {
					return report, err
				}
			}
			emitRefreshGenresProgress(options.Progress, RefreshGenresProgress{
				Stage:       "artist_unchanged",
				ArtistSlug:  artist.Slug,
				ArtistName:  artist.Name,
				ArtistIndex: artistIndex,
				ArtistTotal: artistsWithIDs,
				Unchanged:   true,
			})
			continue
		}
		artist.Genres = genres
		artist.ExternalURL = externalURL
		artist.ImageURL = imageURL
		report.Updated++
		if !options.DryRun {
			if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
				return report, fmt.Errorf("refreshed catalog is invalid: %s", issues[0].Error())
			}
			if err := r.store.Save(ctx, options.CatalogPath, c); err != nil {
				return report, fmt.Errorf("save artist catalog: %w", err)
			}
			if options.CheckpointStore != nil {
				if err := options.CheckpointStore.SaveArtistMetadataRefresh(ctx, artist.Slug, ids, time.Now().UTC()); err != nil {
					return report, err
				}
			}
		}
		emitRefreshGenresProgress(options.Progress, RefreshGenresProgress{
			Stage:       "artist_updated",
			ArtistSlug:  artist.Slug,
			ArtistName:  artist.Name,
			ArtistIndex: artistIndex,
			ArtistTotal: artistsWithIDs,
			Updated:     true,
		})
	}

	if len(report.Errors) > 0 {
		return report, fmt.Errorf("refresh genres completed with %d errors", len(report.Errors))
	}
	if !options.DryRun {
		if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
			return report, fmt.Errorf("refreshed catalog is invalid: %s", issues[0].Error())
		}
	}

	return report, nil
}

func emitRefreshGenresProgress(progress func(RefreshGenresProgress), event RefreshGenresProgress) {
	if progress != nil {
		progress(event)
	}
}

func countArtistsWithSpotifyIDs(artists []catalog.Artist) int {
	total := 0
	for _, artist := range artists {
		if len(artist.AllSpotifyIDs()) > 0 {
			total++
		}
	}
	return total
}

func normalizeGenreList(genres []string) []string {
	seen := map[string]string{}
	for _, genre := range genres {
		genre = strings.TrimSpace(genre)
		key := strings.ToLower(genre)
		if genre == "" || key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = genre
	}
	normalized := make([]string, 0, len(seen))
	for _, genre := range seen {
		normalized = append(normalized, genre)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return strings.ToLower(normalized[i]) < strings.ToLower(normalized[j])
	})
	return normalized
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
