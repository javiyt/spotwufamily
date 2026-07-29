package artists

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type SpotifyArtistFetcher interface {
	GetArtist(context.Context, string) (catalog.ArtistCandidate, error)
}

type RefreshGenres struct {
	store   CatalogStore
	fetcher SpotifyArtistFetcher
}

func NewRefreshGenres(store CatalogStore, fetcher SpotifyArtistFetcher) RefreshGenres {
	return RefreshGenres{store: store, fetcher: fetcher}
}

type RefreshGenresOptions struct {
	CatalogPath string
	DryRun      bool
}

type RefreshGenresReport struct {
	ArtistsWithIDs int
	Updated        int
	Unchanged      int
	WithoutGenres  []string
	WithoutImages  []string
	Errors         []RefreshGenresError
}

type RefreshGenresError struct {
	Slug string
	ID   string
	Err  error
}

func (r RefreshGenres) Run(ctx context.Context, options RefreshGenresOptions) (RefreshGenresReport, error) {
	c, err := r.store.Load(ctx, options.CatalogPath)
	if err != nil {
		return RefreshGenresReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	report := RefreshGenresReport{}
	for index := range c.Artists {
		artist := &c.Artists[index]
		ids := artist.AllSpotifyIDs()
		if len(ids) == 0 {
			continue
		}
		report.ArtistsWithIDs++

		genres := []string{}
		externalURL := ""
		imageURL := ""
		for _, spotifyID := range ids {
			spotifyArtist, err := r.fetcher.GetArtist(ctx, spotifyID)
			if err != nil {
				report.Errors = append(report.Errors, RefreshGenresError{Slug: artist.Slug, ID: spotifyID, Err: err})
				continue
			}
			genres = append(genres, spotifyArtist.Genres...)
			if externalURL == "" {
				externalURL = spotifyArtist.URL
			}
			if imageURL == "" {
				imageURL = spotifyArtist.ImageURL
			}
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
			continue
		}
		artist.Genres = genres
		artist.ExternalURL = externalURL
		artist.ImageURL = imageURL
		report.Updated++
	}

	if len(report.Errors) > 0 {
		return report, fmt.Errorf("refresh genres completed with %d errors", len(report.Errors))
	}
	if !options.DryRun {
		if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
			return report, fmt.Errorf("refreshed catalog is invalid: %s", issues[0].Error())
		}
		if err := r.store.Save(ctx, options.CatalogPath, c); err != nil {
			return report, fmt.Errorf("save artist catalog: %w", err)
		}
	}

	return report, nil
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
