package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/wikipedia"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// EnvLookup reads one configuration value (os.Getenv in production). It lets
// tests supply credentials and settings without touching the process
// environment.
type EnvLookup func(string) string

// ErrSpotifyCredentialsMissing is returned by the maintenance constructors when
// SPOTIFY_CLIENT_ID or SPOTIFY_CLIENT_SECRET is empty. Its message is the one
// the CLI has always printed.
var ErrSpotifyCredentialsMissing = errors.New("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required")

// MaintenanceOption tunes how a maintenance runner is assembled.
type MaintenanceOption func(*maintenanceConfig)

type maintenanceConfig struct {
	dryRun           bool
	market           string
	maxRetryAfter    time.Duration
	maxRetryAfterSet bool
	spotifyProgress  func(spotifyadapter.ProgressEvent)
	wikipediaAPIURL  string
	wikipediaPage    string

	// Test seams: replace the Spotify client so no credentials or network are needed.
	syncFetcher   catalogsync.Fetcher
	artistFetcher artists.SpotifyArtistFetcher
}

// WithDryRun builds a runner for a dry run. A dry sync needs neither the
// database nor Spotify; a dry genre refresh still reads Spotify but does not
// open the database.
func WithDryRun(dryRun bool) MaintenanceOption {
	return func(c *maintenanceConfig) { c.dryRun = dryRun }
}

// WithMarket sets the Spotify client market; empty falls back to SPOTIFY_MARKET.
func WithMarket(market string) MaintenanceOption {
	return func(c *maintenanceConfig) { c.market = market }
}

// WithMaxRetryAfter overrides SPOTIFY_MAX_RETRY_AFTER: the longest Spotify 429
// Retry-After the client waits before aborting. Zero means the client default.
func WithMaxRetryAfter(d time.Duration) MaintenanceOption {
	return func(c *maintenanceConfig) {
		c.maxRetryAfter = d
		c.maxRetryAfterSet = true
	}
}

// WithSpotifyProgress receives the Spotify client's request-level events.
func WithSpotifyProgress(progress func(spotifyadapter.ProgressEvent)) MaintenanceOption {
	return func(c *maintenanceConfig) { c.spotifyProgress = progress }
}

// WithWikipedia points Wu Family discovery at another API URL or page; empty
// values keep the client defaults.
func WithWikipedia(apiURL, page string) MaintenanceOption {
	return func(c *maintenanceConfig) {
		c.wikipediaAPIURL = apiURL
		c.wikipediaPage = page
	}
}

func withSyncFetcher(fetcher catalogsync.Fetcher) MaintenanceOption {
	return func(c *maintenanceConfig) { c.syncFetcher = fetcher }
}

func withArtistFetcher(fetcher artists.SpotifyArtistFetcher) MaintenanceOption {
	return func(c *maintenanceConfig) { c.artistFetcher = fetcher }
}

func resolveMaintenanceConfig(opts []MaintenanceOption) maintenanceConfig {
	var config maintenanceConfig
	for _, opt := range opts {
		opt(&config)
	}
	return config
}

// SpotifyMaxRetryAfter parses SPOTIFY_MAX_RETRY_AFTER as a Go duration or a
// number of seconds. Unset or invalid values yield zero, which the Spotify
// client treats as its 10 minute default.
func SpotifyMaxRetryAfter(env EnvLookup) time.Duration {
	value := strings.TrimSpace(env("SPOTIFY_MAX_RETRY_AFTER"))
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// spotifyConfig builds the client configuration. It performs no network I/O.
func spotifyConfig(env EnvLookup, config maintenanceConfig) (spotifyadapter.Config, error) {
	clientID := env("SPOTIFY_CLIENT_ID")
	clientSecret := env("SPOTIFY_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return spotifyadapter.Config{}, ErrSpotifyCredentialsMissing
	}

	market := config.market
	if market == "" {
		market = env("SPOTIFY_MARKET")
	}
	maxRetryAfter := SpotifyMaxRetryAfter(env)
	if config.maxRetryAfterSet {
		maxRetryAfter = config.maxRetryAfter
	}

	return spotifyadapter.Config{
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		Market:        market,
		MaxRetryAfter: maxRetryAfter,
		Progress:      config.spotifyProgress,
	}, nil
}

// openMigrated opens and migrates the catalog database. Migration errors are
// prefixed so callers print the same message the CLI always has.
func openMigrated(dbPath string) (*sqliteadapter.Database, error) {
	database, err := sqliteadapter.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(context.Background()); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return database, nil
}

func noop() {}

// SyncRunner runs the catalog sync with the shared wiring: Spotify fetcher
// backed by the album cache, reconciliation bookkeeping and snapshot writing.
type SyncRunner struct {
	sync         catalogsync.SyncCatalog
	repository   *sqliteadapter.Database
	catalogPath  string
	snapshotPath string
	dryRun       bool
}

// SyncResult is the sync report plus the outcome of the snapshot write.
type SyncResult struct {
	Report catalogsync.Report
	// PartialSnapshot is true when the sync failed after starting a run and a
	// snapshot of the progress so far was attempted.
	PartialSnapshot bool
	// SnapshotErr is the snapshot write failure, if any (partial or final).
	SnapshotErr error
}

// NewSyncRunner opens and migrates the database and builds the Spotify
// fetcher. A dry run needs neither. It never contacts Spotify. The returned
// function releases the database and is safe to call even when err != nil.
func NewSyncRunner(catalogPath, dbPath, snapshotPath string, env EnvLookup, opts ...MaintenanceOption) (SyncRunner, func(), error) {
	config := resolveMaintenanceConfig(opts)
	runner := SyncRunner{catalogPath: catalogPath, snapshotPath: snapshotPath, dryRun: config.dryRun}
	if config.dryRun {
		runner.sync = catalogsync.NewSyncCatalog(yamlcatalog.NewStore(), nil, runner.repository, catalogsync.SystemClock{})
		return runner, noop, nil
	}

	database, err := openMigrated(dbPath)
	if err != nil {
		return SyncRunner{}, noop, err
	}
	closeFn := func() { _ = database.Close() }

	fetcher := config.syncFetcher
	if fetcher == nil {
		fetcher, err = cachedSpotifySyncFetcher(env, config, database)
		if err != nil {
			closeFn()
			return SyncRunner{}, noop, err
		}
	}

	runner.repository = database
	runner.sync = catalogsync.NewSyncCatalog(yamlcatalog.NewStore(), fetcher, database, catalogsync.SystemClock{})
	return runner, closeFn, nil
}

// Run syncs the catalog. Unless it is a dry run, artist errors are recorded
// as pending reconciliations and a snapshot is written: the full snapshot on
// success, or a partial one when a started run fails (notably on a Spotify
// rate-limit abort, *catalogsync.PartialSyncError). The sync error is returned
// unchanged; a snapshot failure is reported in SyncResult.SnapshotErr.
// o.CatalogPath defaults to the runner's catalog; o.Progress is passed through.
func (r SyncRunner) Run(ctx context.Context, o catalogsync.Options) (SyncResult, error) {
	if o.CatalogPath == "" {
		o.CatalogPath = r.catalogPath
	}
	if r.dryRun {
		o.DryRun = true
	}

	report, err := r.sync.Run(ctx, o)
	result := SyncResult{Report: report}
	if o.DryRun || r.repository == nil {
		return result, err
	}

	// A failed or rate-limited sync keeps the reconciliation pending and says why.
	for _, item := range report.Errors {
		_ = r.repository.RecordReconciliationError(ctx, item.Slug, item.Err.Error())
	}
	if err != nil {
		if report.RunID > 0 {
			result.PartialSnapshot = true
			result.SnapshotErr = r.repository.WriteSnapshot(ctx, r.snapshotPath)
		}
		return result, err
	}
	result.SnapshotErr = r.repository.WriteSnapshot(ctx, r.snapshotPath)
	return result, nil
}

// PendingReconciliations lists artists whose reconciliation is still pending.
// A dry-run runner has no database and reports none.
func (r SyncRunner) PendingReconciliations(ctx context.Context) ([]artists.PendingReconciliation, error) {
	if r.repository == nil {
		return nil, nil
	}
	return r.repository.PendingReconciliations(ctx)
}

func cachedSpotifySyncFetcher(env EnvLookup, config maintenanceConfig, cache artists.SpotifyAlbumCache) (catalogsync.Fetcher, error) {
	clientConfig, err := spotifyConfig(env, config)
	if err != nil {
		return nil, err
	}
	spotifyClient, err := spotifyadapter.NewClient(clientConfig)
	if err != nil {
		return nil, err
	}
	return cachedSyncFetcher{
		remote:       spotifyClient,
		cachedAlbums: artists.NewCachedSpotifyAlbumFetcher(cache, spotifyClient),
	}, nil
}

type cachedSyncFetcher struct {
	remote       catalogsync.Fetcher
	cachedAlbums artists.CachedSpotifyAlbumFetcher
}

func (f cachedSyncFetcher) GetArtist(ctx context.Context, spotifyID string) (catalog.ArtistCandidate, error) {
	return f.remote.GetArtist(ctx, spotifyID)
}

func (f cachedSyncFetcher) GetArtistAlbums(ctx context.Context, spotifyID string, groups []string) ([]catalog.Release, error) {
	return f.cachedAlbums.GetArtistAlbums(ctx, spotifyID, groups)
}

func (f cachedSyncFetcher) GetAlbum(ctx context.Context, spotifyID string) (catalog.Release, error) {
	return f.cachedAlbums.GetAlbum(ctx, spotifyID)
}

func (f cachedSyncFetcher) GetAlbumTracks(ctx context.Context, spotifyID string) ([]catalog.Track, error) {
	return f.cachedAlbums.GetAlbumTracks(ctx, spotifyID)
}

// RefreshRunner runs the artist genre/image refresh against Spotify, with
// database checkpoints unless it is a dry run.
type RefreshRunner struct {
	refresh     artists.RefreshGenres
	catalogPath string
	dryRun      bool
	checkpoints artists.MetadataRefreshCheckpointStore
}

// NewRefreshRunner builds the Spotify artist fetcher (credentials are checked
// first) and, unless dryRun or dbPath is empty, opens and migrates the
// checkpoint database. It never contacts Spotify. The returned function is
// safe to call even when err != nil.
func NewRefreshRunner(catalogPath, dbPath string, env EnvLookup, opts ...MaintenanceOption) (RefreshRunner, func(), error) {
	config := resolveMaintenanceConfig(opts)

	fetcher := config.artistFetcher
	if fetcher == nil {
		clientConfig, err := spotifyConfig(env, config)
		if err != nil {
			return RefreshRunner{}, noop, err
		}
		client, err := spotifyadapter.NewClient(clientConfig)
		if err != nil {
			return RefreshRunner{}, noop, err
		}
		fetcher = client
	}

	runner := RefreshRunner{
		refresh:     artists.NewRefreshGenres(yamlcatalog.NewStore(), fetcher),
		catalogPath: catalogPath,
		dryRun:      config.dryRun,
	}
	closeFn := noop
	if !config.dryRun && dbPath != "" {
		database, err := openMigrated(dbPath)
		if err != nil {
			return RefreshRunner{}, noop, err
		}
		runner.checkpoints = database
		closeFn = func() { _ = database.Close() }
	}
	return runner, closeFn, nil
}

// Run refreshes artist metadata. o.CatalogPath defaults to the runner's
// catalog and o.CheckpointStore to its database; o.Progress is passed through.
func (r RefreshRunner) Run(ctx context.Context, o artists.RefreshGenresOptions) (artists.RefreshGenresReport, error) {
	if o.CatalogPath == "" {
		o.CatalogPath = r.catalogPath
	}
	if r.dryRun {
		o.DryRun = true
	}
	if o.CheckpointStore == nil {
		o.CheckpointStore = r.checkpoints
	}
	return r.refresh.Run(ctx, o)
}

// DiscoverRunner runs Wu Family discovery from Wikipedia, reading the
// candidate ledger so already-decided names are suppressed.
type DiscoverRunner struct {
	discover    artists.DiscoverWuFamily
	catalogPath string
}

// NewDiscoverRunner builds the Wikipedia source. It needs no credentials and
// never contacts Wikipedia; the returned function is a no-op kept for symmetry.
func NewDiscoverRunner(catalogPath string, opts ...MaintenanceOption) (DiscoverRunner, func(), error) {
	config := resolveMaintenanceConfig(opts)
	source := wikipedia.NewClient(wikipedia.Config{APIURL: config.wikipediaAPIURL, Page: config.wikipediaPage})
	return DiscoverRunner{
		discover:    artists.NewDiscoverWuFamily(yamlcatalog.NewStore(), source).WithLedger(yamlcatalog.NewLedgerStore()),
		catalogPath: catalogPath,
	}, noop, nil
}

// Run discovers Wu Family candidates. o.CatalogPath defaults to the runner's
// catalog; o.Progress is passed through.
func (r DiscoverRunner) Run(ctx context.Context, o artists.DiscoverWuFamilyOptions) (artists.DiscoverWuFamilyReport, error) {
	if o.CatalogPath == "" {
		o.CatalogPath = r.catalogPath
	}
	return r.discover.Run(ctx, o)
}
