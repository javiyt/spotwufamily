package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type CatalogStore interface {
	Load(context.Context, string) (catalog.EditorialCatalog, error)
}

type Fetcher interface {
	GetArtist(context.Context, string) (catalog.ArtistCandidate, error)
	GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error)
	GetAlbum(context.Context, string) (catalog.Release, error)
	GetAlbumTracks(context.Context, string) ([]catalog.Track, error)
}

type Repository interface {
	SaveConfiguredArtists(context.Context, []catalog.Artist) error
	FindResumableSyncRun(context.Context, SyncRun) (ResumableSyncRun, bool, error)
	BeginSyncRun(context.Context, SyncRun) (int64, error)
	FinishSyncRun(context.Context, int64, string, SyncStats) error
	SaveArtistCatalog(context.Context, int64, catalog.Artist, catalog.ArtistCandidate, []catalog.ReleaseTracks, time.Time) (SyncStats, error)
	SaveArtistSyncCheckpoint(context.Context, int64, catalog.Artist, string, SyncStats, time.Time) error
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type SyncCatalog struct {
	store      CatalogStore
	fetcher    Fetcher
	repository Repository
	clock      Clock
}

func NewSyncCatalog(store CatalogStore, fetcher Fetcher, repository Repository, clock Clock) SyncCatalog {
	if clock == nil {
		clock = SystemClock{}
	}

	return SyncCatalog{store: store, fetcher: fetcher, repository: repository, clock: clock}
}

type Options struct {
	CatalogPath string
	ArtistSlug  string
	Market      string
	Full        bool
	DryRun      bool
	Resume      bool
	Progress    func(ProgressEvent)
}

type SyncRun struct {
	StartedAt time.Time
	Market    string
	Full      bool
}

type ResumableSyncRun struct {
	ID                        int64
	CompletedArtistSpotifyIDs map[string]string
}

type Report struct {
	DryRun           bool
	ArtistsPlanned   int
	ArtistsProcessed int
	ArtistsSkipped   int
	ArtistsResumed   int
	ArtistsFailed    int
	RunID            int64
	ResumedRunID     int64
	Stats            SyncStats
	Errors           []ArtistError
}

type ArtistError struct {
	Slug string
	Err  error
}

type ProgressEvent struct {
	Stage           ProgressStage
	ArtistSlug      string
	ArtistName      string
	SpotifyID       string
	ReleaseID       string
	ReleaseName     string
	ArtistIndex     int
	ArtistTotal     int
	SpotifyIDIndex  int
	SpotifyIDTotal  int
	ReleaseIndex    int
	ReleaseTotal    int
	ConfiguredCount int
	RunID           int64
	Stats           SyncStats
	Err             error
}

type ProgressStage string

const (
	ProgressCatalogLoaded   ProgressStage = "catalog_loaded"
	ProgressConfiguredSaved ProgressStage = "configured_saved"
	ProgressRunResumed      ProgressStage = "run_resumed"
	ProgressRunStarted      ProgressStage = "run_started"
	ProgressArtistStarted   ProgressStage = "artist_started"
	ProgressSpotifyStarted  ProgressStage = "spotify_started"
	ProgressReleasesFetched ProgressStage = "releases_fetched"
	ProgressReleaseStarted  ProgressStage = "release_started"
	ProgressSpotifySaved    ProgressStage = "spotify_saved"
	ProgressArtistFinished  ProgressStage = "artist_finished"
	ProgressArtistFailed    ProgressStage = "artist_failed"
	ProgressRunFinished     ProgressStage = "run_finished"
)

type SyncStats struct {
	ConfiguredArtistsUpserted int
	SpotifyArtistsUpserted    int
	AlbumsUpserted            int
	TracksUpserted            int
	AlbumArtistsUpserted      int
	TrackArtistsUpserted      int
	AlbumTracksUpserted       int
	ArtistAlbumsUpserted      int
	ArtistTracksUpserted      int
	ImagesUpserted            int
	ExternalURLsUpserted      int
	CopyrightsUpserted        int
}

func (s SyncStats) Add(other SyncStats) SyncStats {
	s.ConfiguredArtistsUpserted += other.ConfiguredArtistsUpserted
	s.SpotifyArtistsUpserted += other.SpotifyArtistsUpserted
	s.AlbumsUpserted += other.AlbumsUpserted
	s.TracksUpserted += other.TracksUpserted
	s.AlbumArtistsUpserted += other.AlbumArtistsUpserted
	s.TrackArtistsUpserted += other.TrackArtistsUpserted
	s.AlbumTracksUpserted += other.AlbumTracksUpserted
	s.ArtistAlbumsUpserted += other.ArtistAlbumsUpserted
	s.ArtistTracksUpserted += other.ArtistTracksUpserted
	s.ImagesUpserted += other.ImagesUpserted
	s.ExternalURLsUpserted += other.ExternalURLsUpserted
	s.CopyrightsUpserted += other.CopyrightsUpserted

	return s
}

func (s SyncCatalog) Run(ctx context.Context, options Options) (Report, error) {
	editorialCatalog, err := s.store.Load(ctx, options.CatalogPath)
	if err != nil {
		return Report{}, fmt.Errorf("load artist catalog: %w", err)
	}
	if issues := catalog.ValidateEditorialCatalog(editorialCatalog); len(issues) > 0 {
		return Report{}, fmt.Errorf("artist catalog is invalid: %s", issues[0].Error())
	}

	eligibleArtists := enabledArtists(editorialCatalog.Artists, options.ArtistSlug)
	report := Report{DryRun: options.DryRun, ArtistsPlanned: len(eligibleArtists)}
	progress(options.Progress, ProgressEvent{Stage: ProgressCatalogLoaded, ArtistTotal: len(eligibleArtists), ConfiguredCount: len(editorialCatalog.Artists)})
	if options.ArtistSlug != "" && len(eligibleArtists) == 0 {
		return report, fmt.Errorf("enabled artist %q not found", options.ArtistSlug)
	}
	if options.DryRun {
		return report, nil
	}

	if err := s.repository.SaveConfiguredArtists(ctx, editorialCatalog.Artists); err != nil {
		return report, fmt.Errorf("save configured artists: %w", err)
	}
	report.Stats.ConfiguredArtistsUpserted = len(editorialCatalog.Artists)
	progress(options.Progress, ProgressEvent{Stage: ProgressConfiguredSaved, ConfiguredCount: len(editorialCatalog.Artists)})

	syncRun := SyncRun{
		StartedAt: s.clock.Now(),
		Market:    marketOrDefault(options.Market),
		Full:      options.Full,
	}

	artists := eligibleArtists
	if options.Resume {
		resumableRun, ok, err := s.repository.FindResumableSyncRun(ctx, syncRun)
		if err != nil {
			return report, fmt.Errorf("find resumable sync run: %w", err)
		}
		if ok {
			artists, report.ArtistsResumed = filterResumedArtists(eligibleArtists, resumableRun.CompletedArtistSpotifyIDs)
			report.ResumedRunID = resumableRun.ID
			report.ArtistsPlanned = len(artists)
			progress(options.Progress, ProgressEvent{Stage: ProgressRunResumed, RunID: resumableRun.ID, ArtistTotal: len(artists)})
		}
	}

	runID, err := s.repository.BeginSyncRun(ctx, syncRun)
	if err != nil {
		return report, fmt.Errorf("begin sync run: %w", err)
	}
	report.RunID = runID
	progress(options.Progress, ProgressEvent{Stage: ProgressRunStarted, RunID: runID})

	for index, configuredArtist := range artists {
		event := ProgressEvent{
			Stage:       ProgressArtistStarted,
			ArtistSlug:  configuredArtist.Slug,
			ArtistName:  configuredArtist.Name,
			ArtistIndex: index + 1,
			ArtistTotal: len(artists),
		}
		progress(options.Progress, event)
		artistStats, err := s.syncArtist(ctx, runID, configuredArtist, event, options.Progress)
		if err != nil {
			report.ArtistsFailed++
			report.Errors = append(report.Errors, ArtistError{Slug: configuredArtist.Slug, Err: err})
			event.Stage = ProgressArtistFailed
			event.Err = err
			progress(options.Progress, event)
			continue
		}
		if err := s.repository.SaveArtistSyncCheckpoint(ctx, runID, configuredArtist, "completed", artistStats, s.clock.Now()); err != nil {
			report.ArtistsFailed++
			checkpointErr := fmt.Errorf("save artist sync checkpoint: %w", err)
			report.Errors = append(report.Errors, ArtistError{Slug: configuredArtist.Slug, Err: checkpointErr})
			event.Stage = ProgressArtistFailed
			event.Err = checkpointErr
			progress(options.Progress, event)
			continue
		}
		report.ArtistsProcessed++
		report.Stats = report.Stats.Add(artistStats)
		event.Stage = ProgressArtistFinished
		event.Stats = artistStats
		progress(options.Progress, event)
	}
	report.ArtistsSkipped = len(editorialCatalog.Artists) - len(eligibleArtists)

	status := "success"
	if report.ArtistsFailed > 0 {
		status = "partial"
	}
	if err := s.repository.FinishSyncRun(ctx, runID, status, report.Stats); err != nil {
		return report, fmt.Errorf("finish sync run: %w", err)
	}
	progress(options.Progress, ProgressEvent{Stage: ProgressRunFinished, RunID: runID, Stats: report.Stats})

	if report.ArtistsFailed > 0 {
		return report, fmt.Errorf("%d artist syncs failed", report.ArtistsFailed)
	}

	return report, nil
}

func (s SyncCatalog) syncArtist(ctx context.Context, runID int64, configuredArtist catalog.Artist, baseEvent ProgressEvent, progressFunc func(ProgressEvent)) (SyncStats, error) {
	stats := SyncStats{}
	seenReleases := map[string]struct{}{}
	spotifyIDs := configuredArtist.AllSpotifyIDs()
	for spotifyIndex, spotifyID := range spotifyIDs {
		event := baseEvent
		event.Stage = ProgressSpotifyStarted
		event.SpotifyID = spotifyID
		event.SpotifyIDIndex = spotifyIndex + 1
		event.SpotifyIDTotal = len(spotifyIDs)
		progress(progressFunc, event)

		spotifyArtist, err := s.fetcher.GetArtist(ctx, spotifyID)
		if err != nil {
			return SyncStats{}, fmt.Errorf("get Spotify artist %s: %w", spotifyID, err)
		}

		releases, err := s.fetcher.GetArtistAlbums(ctx, spotifyID, []string{"album", "single", "compilation", "appears_on"})
		if err != nil {
			return SyncStats{}, fmt.Errorf("get Spotify artist albums %s: %w", spotifyID, err)
		}
		event.Stage = ProgressReleasesFetched
		event.ReleaseTotal = len(releases)
		progress(progressFunc, event)

		releaseTracks := make([]catalog.ReleaseTracks, 0, len(releases))
		for releaseIndex, release := range releases {
			if release.SpotifyID == "" {
				continue
			}
			if _, ok := seenReleases[release.SpotifyID]; ok {
				continue
			}
			seenReleases[release.SpotifyID] = struct{}{}

			event.Stage = ProgressReleaseStarted
			event.ReleaseID = release.SpotifyID
			event.ReleaseName = release.Name
			event.ReleaseIndex = releaseIndex + 1
			event.ReleaseTotal = len(releases)
			progress(progressFunc, event)

			fullRelease, err := s.fetcher.GetAlbum(ctx, release.SpotifyID)
			if err != nil {
				return SyncStats{}, fmt.Errorf("get Spotify album %s: %w", release.SpotifyID, err)
			}
			tracks, err := s.fetcher.GetAlbumTracks(ctx, release.SpotifyID)
			if err != nil {
				return SyncStats{}, fmt.Errorf("get Spotify album tracks %s: %w", release.SpotifyID, err)
			}
			releaseTracks = append(releaseTracks, catalog.ReleaseTracks{Release: fullRelease, Tracks: deduplicateTracks(tracks)})
		}

		artistStats, err := s.repository.SaveArtistCatalog(ctx, runID, configuredArtist, spotifyArtist, releaseTracks, s.clock.Now())
		if err != nil {
			return SyncStats{}, fmt.Errorf("save artist catalog %s: %w", spotifyID, err)
		}
		stats = stats.Add(artistStats)
		event.Stage = ProgressSpotifySaved
		event.Stats = artistStats
		progress(progressFunc, event)
	}

	return stats, nil
}

func progress(progressFunc func(ProgressEvent), event ProgressEvent) {
	if progressFunc != nil {
		progressFunc(event)
	}
}

func enabledArtists(artists []catalog.Artist, slug string) []catalog.Artist {
	enabled := make([]catalog.Artist, 0, len(artists))
	for _, artist := range artists {
		if !artist.Enabled || len(artist.AllSpotifyIDs()) == 0 {
			continue
		}
		if slug != "" && artist.Slug != slug {
			continue
		}
		enabled = append(enabled, artist)
	}

	return enabled
}

func filterResumedArtists(artists []catalog.Artist, completed map[string]string) ([]catalog.Artist, int) {
	if len(completed) == 0 {
		return artists, 0
	}

	pending := make([]catalog.Artist, 0, len(artists))
	resumed := 0
	for _, artist := range artists {
		if completed[artist.Slug] == spotifyIDsFingerprint(artist) {
			resumed++
			continue
		}
		pending = append(pending, artist)
	}

	return pending, resumed
}

func SpotifyIDsFingerprint(artist catalog.Artist) string {
	return spotifyIDsFingerprint(artist)
}

func spotifyIDsFingerprint(artist catalog.Artist) string {
	data, err := json.Marshal(artist.AllSpotifyIDs())
	if err != nil {
		return ""
	}

	return string(data)
}

func deduplicateTracks(tracks []catalog.Track) []catalog.Track {
	deduped := make([]catalog.Track, 0, len(tracks))
	seen := map[string]struct{}{}
	for _, track := range tracks {
		if track.SpotifyID == "" {
			continue
		}
		if _, ok := seen[track.SpotifyID]; ok {
			continue
		}
		seen[track.SpotifyID] = struct{}{}
		deduped = append(deduped, track)
	}

	return deduped
}

func marketOrDefault(market string) string {
	market = strings.TrimSpace(market)
	if market == "" {
		return "ES"
	}

	return market
}
