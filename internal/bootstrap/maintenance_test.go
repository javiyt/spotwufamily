package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const maintenanceCatalogYAML = `
version: 1
profile_sync_policy: allow_unreviewed
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
`

type maintenancePaths struct {
	catalog, db, snapshot string
}

func newMaintenancePaths(t *testing.T) maintenancePaths {
	t.Helper()
	dir := t.TempDir()
	paths := maintenancePaths{
		catalog:  filepath.Join(dir, "artists.yaml"),
		db:       filepath.Join(dir, "catalog.db"),
		snapshot: filepath.Join(dir, "catalog.snapshot.sql.gz"),
	}
	require.NoError(t, os.WriteFile(paths.catalog, []byte(maintenanceCatalogYAML), 0o644))
	return paths
}

func envMap(values map[string]string) EnvLookup {
	return func(key string) string { return values[key] }
}

var credentials = envMap(map[string]string{"SPOTIFY_CLIENT_ID": "id", "SPOTIFY_CLIENT_SECRET": "secret"})

// rateLimitAbort mimics the Spotify adapter's 429 abort: catalogsync stops
// the run and reports a partial sync when it sees AbortSync.
type rateLimitAbort struct{}

func (rateLimitAbort) Error() string   { return "spotify rate limit: retry after 1h" }
func (rateLimitAbort) AbortSync() bool { return true }

type abortingFetcher struct{}

func (abortingFetcher) GetArtist(context.Context, string) (catalog.ArtistCandidate, error) {
	return catalog.ArtistCandidate{}, rateLimitAbort{}
}

func (abortingFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return nil, rateLimitAbort{}
}

func (abortingFetcher) GetAlbum(context.Context, string) (catalog.Release, error) {
	return catalog.Release{}, rateLimitAbort{}
}

func (abortingFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return nil, rateLimitAbort{}
}

func TestSyncRunnerWritesPartialSnapshotOnRateLimit(t *testing.T) {
	paths := newMaintenancePaths(t)
	runner, closeFn, err := NewSyncRunner(context.Background(), paths.catalog, paths.db, paths.snapshot, envMap(nil), withSyncFetcher(abortingFetcher{}))
	require.NoError(t, err)
	defer closeFn()

	var stages []catalogsync.ProgressStage
	result, err := runner.Run(context.Background(), catalogsync.Options{
		Progress: func(event catalogsync.ProgressEvent) { stages = append(stages, event.Stage) },
	})

	var partial *catalogsync.PartialSyncError
	require.ErrorAs(t, err, &partial, "the sync error is returned unchanged")
	require.ErrorAs(t, partial.Err, new(rateLimitAbort))
	require.Positive(t, result.Report.RunID)
	require.True(t, result.PartialSnapshot)
	require.NoError(t, result.SnapshotErr)
	require.FileExists(t, paths.snapshot)
	require.Contains(t, stages, catalogsync.ProgressRunStarted, "progress is passed through to the use case")

	require.NotEmpty(t, result.Report.Errors, "the rate-limited artist is reported")
	_, err = runner.PendingReconciliations(context.Background())
	require.NoError(t, err, "the runner exposes pending reconciliations for the caller's warning")
}

func TestSyncRunnerReportsPartialSnapshotFailureSeparately(t *testing.T) {
	paths := newMaintenancePaths(t)
	blocked := filepath.Join(filepath.Dir(paths.snapshot), "not-a-dir")
	require.NoError(t, os.WriteFile(blocked, nil, 0o644))
	runner, closeFn, err := NewSyncRunner(context.Background(), paths.catalog, paths.db, filepath.Join(blocked, "snapshot.sql.gz"), envMap(nil), withSyncFetcher(abortingFetcher{}))
	require.NoError(t, err)
	defer closeFn()

	result, err := runner.Run(context.Background(), catalogsync.Options{})

	require.ErrorAs(t, err, new(*catalogsync.PartialSyncError))
	require.True(t, result.PartialSnapshot)
	require.Error(t, result.SnapshotErr)
}

func TestSyncRunnerDryRunNeedsNoCredentialsOrDatabase(t *testing.T) {
	paths := newMaintenancePaths(t)
	runner, closeFn, err := NewSyncRunner(context.Background(), paths.catalog, paths.db, paths.snapshot, envMap(nil), WithDryRun(true))
	require.NoError(t, err)
	defer closeFn()

	result, err := runner.Run(context.Background(), catalogsync.Options{DryRun: true})

	require.NoError(t, err)
	require.True(t, result.Report.DryRun)
	require.Equal(t, 1, result.Report.ArtistsPlanned)
	require.NoFileExists(t, paths.db)
	require.NoFileExists(t, paths.snapshot)
}

func TestNewSyncRunnerWithoutCredentialsReturnsTypedError(t *testing.T) {
	paths := newMaintenancePaths(t)

	_, closeFn, err := NewSyncRunner(context.Background(), paths.catalog, paths.db, paths.snapshot, envMap(map[string]string{"SPOTIFY_CLIENT_ID": "id"}))

	require.ErrorIs(t, err, ErrSpotifyCredentialsMissing)
	require.Equal(t, "SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required", err.Error())
	require.NotNil(t, closeFn)
	closeFn()
	require.FileExists(t, paths.db, "like the CLI, the database is migrated before credentials are checked")
}

func TestNewSyncRunnerWithCredentialsMakesNoNetworkCalls(t *testing.T) {
	paths := newMaintenancePaths(t)

	runner, closeFn, err := NewSyncRunner(context.Background(), paths.catalog, paths.db, paths.snapshot, credentials)

	require.NoError(t, err)
	require.NotNil(t, runner)
	closeFn()
	require.NoFileExists(t, paths.snapshot)
}

func TestNewRefreshRunnerWithoutCredentialsReturnsTypedErrorBeforeOpeningDatabase(t *testing.T) {
	paths := newMaintenancePaths(t)

	_, closeFn, err := NewRefreshRunner(context.Background(), paths.catalog, paths.db, envMap(nil))

	require.ErrorIs(t, err, ErrSpotifyCredentialsMissing)
	require.NotNil(t, closeFn)
	closeFn()
	require.NoFileExists(t, paths.db)
}

type stubArtistFetcher struct{}

func (stubArtistFetcher) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	return catalog.ArtistCandidate{SpotifyID: id, Name: "Wu-Tang Clan", Genres: []string{"hip hop"}}, nil
}

func TestRefreshRunnerFillsCatalogPathAndCheckpointStore(t *testing.T) {
	paths := newMaintenancePaths(t)
	runner, closeFn, err := NewRefreshRunner(context.Background(), paths.catalog, paths.db, envMap(nil), withArtistFetcher(stubArtistFetcher{}))
	require.NoError(t, err)
	defer closeFn()

	var events int
	report, err := runner.Run(context.Background(), artists.RefreshGenresOptions{
		Progress: func(artists.RefreshGenresProgress) { events++ },
	})

	require.NoError(t, err)
	require.Equal(t, 1, report.ArtistsWithIDs)
	require.Positive(t, events)
	require.NotNil(t, runner.checkpoints, "a non-dry-run with a database records checkpoints")

	again, err := runner.Run(context.Background(), artists.RefreshGenresOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, again.SkippedRecent, "the checkpoint written by the first run is honoured")
}

func TestRefreshRunnerPerCallDryRunIgnoresCheckpoints(t *testing.T) {
	paths := newMaintenancePaths(t)
	runner, closeFn, err := NewRefreshRunner(context.Background(), paths.catalog, paths.db, envMap(nil), withArtistFetcher(stubArtistFetcher{}))
	require.NoError(t, err)
	defer closeFn()
	_, err = runner.Run(context.Background(), artists.RefreshGenresOptions{})
	require.NoError(t, err)

	dry, err := runner.Run(context.Background(), artists.RefreshGenresOptions{DryRun: true})

	require.NoError(t, err)
	require.Zero(t, dry.SkippedRecent, "a per-call dry run must not consult checkpoints")
}

func TestRefreshRunnerDryRunSkipsDatabase(t *testing.T) {
	paths := newMaintenancePaths(t)
	runner, closeFn, err := NewRefreshRunner(context.Background(), paths.catalog, paths.db, envMap(nil), WithDryRun(true), withArtistFetcher(stubArtistFetcher{}))
	require.NoError(t, err)
	defer closeFn()

	_, err = runner.Run(context.Background(), artists.RefreshGenresOptions{})

	require.NoError(t, err)
	require.NoFileExists(t, paths.db)
}

func TestDiscoverRunnerUsesConfiguredWikipediaSource(t *testing.T) {
	paths := newMaintenancePaths(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"parse":{"sections":[
			{"toclevel":1,"line":"Groups","anchor":"Groups"},
			{"toclevel":2,"line":"Killarmy","anchor":"Killarmy"}
		]}}`)
	}))
	defer server.Close()

	runner, closeFn, err := NewDiscoverRunner(context.Background(), paths.catalog, WithWikipedia(server.URL, ""))
	require.NoError(t, err)
	defer closeFn()

	report, err := runner.Run(context.Background(), artists.DiscoverWuFamilyOptions{
		LedgerPath: filepath.Join(t.TempDir(), "ledger.yaml"),
	})

	require.NoError(t, err)
	require.Equal(t, 1, report.Found)
	require.Len(t, report.New, 1)
}

func TestSpotifyConfigMapsEnvironmentAndOptions(t *testing.T) {
	env := envMap(map[string]string{
		"SPOTIFY_CLIENT_ID":       "id",
		"SPOTIFY_CLIENT_SECRET":   "secret",
		"SPOTIFY_MARKET":          "US",
		"SPOTIFY_MAX_RETRY_AFTER": "45",
	})
	progress := func(spotifyadapter.ProgressEvent) {}

	config, err := spotifyConfig(env, resolveMaintenanceConfig([]MaintenanceOption{WithSpotifyProgress(progress)}))
	require.NoError(t, err)
	require.Equal(t, "id", config.ClientID)
	require.Equal(t, "secret", config.ClientSecret)
	require.Equal(t, "US", config.Market, "SPOTIFY_MARKET is the fallback market")
	require.Equal(t, 45*time.Second, config.MaxRetryAfter)
	require.NotNil(t, config.Progress)

	config, err = spotifyConfig(env, resolveMaintenanceConfig([]MaintenanceOption{WithMarket("ES"), WithMaxRetryAfter(5 * time.Second)}))
	require.NoError(t, err)
	require.Equal(t, "ES", config.Market, "an explicit market wins over SPOTIFY_MARKET")
	require.Equal(t, 5*time.Second, config.MaxRetryAfter, "the option overrides SPOTIFY_MAX_RETRY_AFTER")
	require.Nil(t, config.Progress)

	_, err = spotifyConfig(envMap(nil), resolveMaintenanceConfig(nil))
	require.True(t, errors.Is(err, ErrSpotifyCredentialsMissing))
}

func TestSpotifyMaxRetryAfterMatchesCLIParsing(t *testing.T) {
	cases := map[string]time.Duration{
		"":      0, // the Spotify client then applies its 10 minute default
		"90s":   90 * time.Second,
		" 30 ":  30 * time.Second,
		"bogus": 0,
	}
	for value, want := range cases {
		got := SpotifyMaxRetryAfter(envMap(map[string]string{"SPOTIFY_MAX_RETRY_AFTER": value}))
		require.Equal(t, want, got, "SPOTIFY_MAX_RETRY_AFTER=%q", value)
	}
}
