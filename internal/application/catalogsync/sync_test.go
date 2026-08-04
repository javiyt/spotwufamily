package catalogsync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestSyncCatalogDryRunDoesNotCallFetcherOrRepository(t *testing.T) {
	store := fakeStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk")},
	}}
	usecase := catalogsync.NewSyncCatalog(store, nil, nil, fixedClock{})

	report, err := usecase.Run(context.Background(), catalogsync.Options{
		CatalogPath: "ignored",
		DryRun:      true,
	})

	require.NoError(t, err)
	require.True(t, report.DryRun)
	require.Equal(t, 1, report.ArtistsPlanned)
	require.Zero(t, report.ArtistsProcessed)
}

func TestSyncCatalogProcessesEnabledArtistsAndDeduplicates(t *testing.T) {
	store := fakeStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			func() catalog.Artist {
				artist := enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk")
				artist.SpotifyIDs = []string{"0H8YCcvC3MPLKnbDRasGiG"}
				return artist
			}(),
			{Slug: "disabled", Name: "Disabled", Category: catalog.CategoryCollaborator, Enabled: false},
		},
	}}
	fetcher := &fakeFetcher{
		artist: catalog.ArtistCandidate{Name: "Wu-Tang Clan", SpotifyID: "34EP7KEpOjXcM2TCat1ISk"},
		releases: []catalog.Release{
			{SpotifyID: "album-1", Name: "Album One"},
			{SpotifyID: "album-1", Name: "Album One Duplicate"},
		},
		albums: map[string]catalog.Release{
			"album-1": {
				SpotifyID: "album-1",
				Name:      "Album One",
				AlbumType: "album",
				Artists:   []catalog.ArtistCandidate{{SpotifyID: "34EP7KEpOjXcM2TCat1ISk", Name: "Wu-Tang Clan"}},
			},
		},
		tracks: map[string][]catalog.Track{
			"album-1": {
				{SpotifyID: "track-1", Name: "Track One", Artists: []catalog.ArtistCandidate{{SpotifyID: "34EP7KEpOjXcM2TCat1ISk", Name: "Wu-Tang Clan"}}},
				{SpotifyID: "track-1", Name: "Track One Duplicate"},
			},
		},
	}
	repository := &fakeRepository{}
	usecase := catalogsync.NewSyncCatalog(store, fetcher, repository, fixedClock{})

	report, err := usecase.Run(context.Background(), catalogsync.Options{CatalogPath: "ignored", Market: "ES"})

	require.NoError(t, err)
	require.Equal(t, int64(42), report.RunID)
	require.Equal(t, 1, report.ArtistsProcessed)
	require.Equal(t, 1, report.ArtistsSkipped)
	require.Equal(t, 1, report.Stats.AlbumsUpserted)
	require.Equal(t, 1, report.Stats.TracksUpserted)
	require.Len(t, repository.savedReleases, 1)
	require.Len(t, repository.savedReleases[0].Tracks, 1)
	require.Equal(t, []string{"album", "single", "compilation", "appears_on"}, fetcher.groups)
	require.Equal(t, []string{"34EP7KEpOjXcM2TCat1ISk", "0H8YCcvC3MPLKnbDRasGiG"}, fetcher.artistIDs)
}

func TestSyncCatalogReportsProgress(t *testing.T) {
	store := fakeStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk")},
	}}
	fetcher := &fakeFetcher{
		artist:   catalog.ArtistCandidate{Name: "Wu-Tang Clan", SpotifyID: "34EP7KEpOjXcM2TCat1ISk"},
		releases: []catalog.Release{{SpotifyID: "album-1", Name: "Album One"}},
		albums: map[string]catalog.Release{
			"album-1": {SpotifyID: "album-1", Name: "Album One", AlbumType: "album"},
		},
		tracks: map[string][]catalog.Track{
			"album-1": {{SpotifyID: "track-1", Name: "Track One"}},
		},
	}
	var events []catalogsync.ProgressEvent
	usecase := catalogsync.NewSyncCatalog(store, fetcher, &fakeRepository{}, fixedClock{})

	report, err := usecase.Run(context.Background(), catalogsync.Options{
		CatalogPath: "ignored",
		Progress: func(event catalogsync.ProgressEvent) {
			events = append(events, event)
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, report.ArtistsProcessed)
	requireProgressStages(t, events,
		catalogsync.ProgressCatalogLoaded,
		catalogsync.ProgressConfiguredSaved,
		catalogsync.ProgressRunStarted,
		catalogsync.ProgressArtistStarted,
		catalogsync.ProgressSpotifyStarted,
		catalogsync.ProgressReleasesFetched,
		catalogsync.ProgressReleaseStarted,
		catalogsync.ProgressSpotifySaved,
		catalogsync.ProgressArtistFinished,
		catalogsync.ProgressRunFinished,
	)
	require.Equal(t, "album-1", events[6].ReleaseID)
	require.Equal(t, 1, events[8].Stats.AlbumsUpserted)
}

func TestSyncCatalogResumesFromPartialRun(t *testing.T) {
	store := fakeStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{
			enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk"),
			func() catalog.Artist {
				artist := enabledArtist("gravediggaz", "0CH4f9m2L3TRaA5oErU2p0")
				artist.Name = "Gravediggaz"
				return artist
			}(),
		},
	}}
	fetcher := &fakeFetcher{
		artist:   catalog.ArtistCandidate{Name: "Gravediggaz", SpotifyID: "0CH4f9m2L3TRaA5oErU2p0"},
		releases: []catalog.Release{{SpotifyID: "album-1", Name: "Album One"}},
		albums: map[string]catalog.Release{
			"album-1": {SpotifyID: "album-1", Name: "Album One", AlbumType: "album"},
		},
		tracks: map[string][]catalog.Track{
			"album-1": {{SpotifyID: "track-1", Name: "Track One"}},
		},
	}
	repository := &fakeRepository{
		resumableRun: catalogsync.ResumableSyncRun{
			ID: 7,
			CompletedArtistSpotifyIDs: map[string]string{
				"wu-tang-clan": catalogsync.SpotifyIDsFingerprint(enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk")),
			},
		},
	}
	usecase := catalogsync.NewSyncCatalog(store, fetcher, repository, fixedClock{})

	report, err := usecase.Run(context.Background(), catalogsync.Options{CatalogPath: "ignored", Market: "ES", Resume: true})

	require.NoError(t, err)
	require.Equal(t, int64(7), report.ResumedRunID)
	require.Equal(t, 1, report.ArtistsResumed)
	require.Equal(t, 1, report.ArtistsPlanned)
	require.Equal(t, 1, report.ArtistsProcessed)
	require.Equal(t, []string{"0CH4f9m2L3TRaA5oErU2p0"}, fetcher.artistIDs)
	require.Equal(t, []string{"gravediggaz"}, repository.checkpointedArtists)
}

func TestSyncCatalogReturnsPartialFailure(t *testing.T) {
	store := fakeStore{catalog: catalog.EditorialCatalog{
		Version: 1,
		Artists: []catalog.Artist{enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk")},
	}}
	usecase := catalogsync.NewSyncCatalog(store, &fakeFetcher{err: errors.New("spotify down")}, &fakeRepository{}, fixedClock{})

	report, err := usecase.Run(context.Background(), catalogsync.Options{CatalogPath: "ignored"})

	require.ErrorContains(t, err, "1 artist syncs failed")
	require.Equal(t, 1, report.ArtistsFailed)
	require.Len(t, report.Errors, 1)
}

func requireProgressStages(t *testing.T, events []catalogsync.ProgressEvent, stages ...catalogsync.ProgressStage) {
	t.Helper()

	require.Len(t, events, len(stages))
	for index, stage := range stages {
		require.Equal(t, stage, events[index].Stage)
	}
}

type fakeStore struct {
	catalog catalog.EditorialCatalog
}

func (f fakeStore) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return f.catalog, nil
}

type fakeFetcher struct {
	artist    catalog.ArtistCandidate
	releases  []catalog.Release
	albums    map[string]catalog.Release
	tracks    map[string][]catalog.Track
	groups    []string
	artistIDs []string
	err       error
}

func (f *fakeFetcher) GetArtist(_ context.Context, spotifyID string) (catalog.ArtistCandidate, error) {
	if f.err != nil {
		return catalog.ArtistCandidate{}, f.err
	}
	f.artistIDs = append(f.artistIDs, spotifyID)

	return f.artist, nil
}

func (f *fakeFetcher) GetArtistAlbums(_ context.Context, _ string, groups []string) ([]catalog.Release, error) {
	f.groups = append([]string(nil), groups...)
	return f.releases, nil
}

func (f *fakeFetcher) GetAlbum(_ context.Context, spotifyID string) (catalog.Release, error) {
	return f.albums[spotifyID], nil
}

func (f *fakeFetcher) GetAlbumTracks(_ context.Context, spotifyID string) ([]catalog.Track, error) {
	return f.tracks[spotifyID], nil
}

type fakeRepository struct {
	stats               catalogsync.SyncStats
	savedReleases       []catalog.ReleaseTracks
	resumableRun        catalogsync.ResumableSyncRun
	checkpointedArtists []string
}

func (f *fakeRepository) SaveConfiguredArtists(context.Context, []catalog.Artist) error {
	return nil
}

func (f *fakeRepository) BeginSyncRun(context.Context, catalogsync.SyncRun) (int64, error) {
	return 42, nil
}

func (f *fakeRepository) FindResumableSyncRun(context.Context, catalogsync.SyncRun) (catalogsync.ResumableSyncRun, bool, error) {
	return f.resumableRun, f.resumableRun.ID > 0, nil
}

func (f *fakeRepository) FinishSyncRun(context.Context, int64, string, catalogsync.SyncStats) error {
	return nil
}

func (f *fakeRepository) SaveArtistSyncCheckpoint(_ context.Context, _ int64, artist catalog.Artist, _ string, _ catalogsync.SyncStats, _ time.Time) error {
	f.checkpointedArtists = append(f.checkpointedArtists, artist.Slug)
	return nil
}

func (f *fakeRepository) SaveArtistCatalog(_ context.Context, _ int64, _ catalog.Artist, _ catalog.ArtistCandidate, releases []catalog.ReleaseTracks, _ time.Time) (catalogsync.SyncStats, error) {
	if len(releases) == 0 {
		return catalogsync.SyncStats{}, nil
	}
	f.savedReleases = append(f.savedReleases, releases...)
	return catalogsync.SyncStats{AlbumsUpserted: len(releases), TracksUpserted: len(releases[0].Tracks)}, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time {
	return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
}

func enabledArtist(slug, spotifyID string) catalog.Artist {
	return catalog.Artist{
		Slug:      slug,
		Name:      "Wu-Tang Clan",
		SpotifyID: spotifyID,
		Category:  catalog.CategoryCore,
		Roles:     []catalog.Category{catalog.CategoryCore},
		Enabled:   true,
	}
}
