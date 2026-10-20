// Package bootstrap is the shared composition root: it wires concrete adapters
// into application use cases so inbound adapters (CLI, HTTP admin) do not each
// repeat the wiring.
package bootstrap

import (
	"context"
	"os"
	"time"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/musicbrainz"
	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// adminSpotifyMaxRetryAfter bounds how long an interactive request waits on a
// Spotify 429: anything longer fails fast and the UI shows the Retry-After.
const adminSpotifyMaxRetryAfter = 5 * time.Second

// NewAdminService wires the admin use cases (queries and catalog edits). A missing or unusable
// database is not an error: the service keeps serving the YAML catalog
// (offline mode) and never creates the database file. The returned function
// releases the database handle.
func NewAdminService(catalogPath, dbPath string) (admin.Service, func(), error) {
	store := yamlcatalog.NewStore()
	credentials := SpotifyCredentialsPresent

	var ops admin.OperationsReader
	var cache artists.SpotifyAlbumCache
	closeFn := func() {}
	if info, err := os.Stat(dbPath); err == nil && !info.IsDir() {
		if database, err := sqliteadapter.Open(dbPath); err == nil {
			ops = admin.NewOperations(database, database)
			cache = database
			closeFn = func() { _ = database.Close() }
		}
	}

	svc := admin.NewService(store, ops, credentials, catalogPath)
	return svc.WithResolution(resolutionDeps(catalogPath, cache)), closeFn, nil
}

// resolutionDeps wires the profile review workbench. The Spotify client lives
// only in this server process: its credentials are read from the environment
// here and never reach a template, response or log. Without credentials the
// workbench is cache-only, and without a database it is catalog-only.
func resolutionDeps(catalogPath string, cache artists.SpotifyAlbumCache) admin.ResolutionDeps {
	deps := admin.ResolutionDeps{
		Log:  filesystem.NewReviewLog(catalogPath + ".reviews.jsonl"),
		Live: false,
		Now:  time.Now,
	}
	if cache != nil {
		deps.Cached = artists.NewCacheFirstProfileSource(cache, nil)
		deps.Source = deps.Cached
	}
	if !SpotifyCredentialsPresent() {
		return deps
	}

	client, err := spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:      os.Getenv("SPOTIFY_CLIENT_ID"),
		ClientSecret:  os.Getenv("SPOTIFY_CLIENT_SECRET"),
		Market:        os.Getenv("SPOTIFY_MARKET"),
		MaxRetryAfter: adminSpotifyMaxRetryAfter,
	})
	if err != nil {
		return deps
	}
	deps.Live = true
	deps.Source = artists.NewCacheFirstProfileSource(cache, spotifyadapter.NewProfileSource(client))
	mb := musicbrainz.NewClient(musicbrainz.Config{UserAgent: os.Getenv("MUSICBRAINZ_USER_AGENT")})
	deps.MusicBrainz = mb
	var albums artists.SpotifyAlbumFetcher = client
	if cache != nil {
		albums = artists.NewCachedSpotifyAlbumFetcher(cache, client)
	}
	deps.Searcher = mappedSearcher{inner: artists.NewAlbumEvidenceCandidateSearcher(client, albums, mb)}
	return deps
}

// mappedSearcher is the CLI's evidence-aware candidate search with transport
// errors translated to the application's source errors.
type mappedSearcher struct {
	inner artists.CandidateSearcher
}

func (m mappedSearcher) SearchArtistCandidates(ctx context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	found, err := m.inner.SearchArtistCandidates(ctx, artist)
	return found, spotifyadapter.MapSourceError(err)
}

func (m mappedSearcher) SearchArtistCandidatesWithCatalog(ctx context.Context, artist catalog.Artist, c catalog.EditorialCatalog) ([]catalog.ArtistCandidate, error) {
	if contextual, ok := m.inner.(artists.CatalogAwareCandidateSearcher); ok {
		found, err := contextual.SearchArtistCandidatesWithCatalog(ctx, artist, c)
		return found, spotifyadapter.MapSourceError(err)
	}
	return m.SearchArtistCandidates(ctx, artist)
}

// SpotifyCredentialsPresent reports whether both Client Credentials variables
// are set. Values are never read into the result.
func SpotifyCredentialsPresent() bool {
	return os.Getenv("SPOTIFY_CLIENT_ID") != "" && os.Getenv("SPOTIFY_CLIENT_SECRET") != ""
}

// MigrateDatabase creates and migrates a database (used by smoke tests).
func MigrateDatabase(ctx context.Context, dbPath string) error {
	database, err := sqliteadapter.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	return database.Migrate(ctx)
}
