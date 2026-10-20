package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/musicbrainz"
	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const auditProfilesUsage = "usage: spotwufamily artists audit-profiles [--artist slug] [--offline] [--report profiles-audit.md|-] [--format markdown|json] [--catalog data/artists.yaml] [--db data/catalog.db] [--market ES] [--concurrency 4] [--quiet] [--apply --artist slug --spotify-id ID]"

type artistAuditProfilesOptions struct {
	catalogPath string
	dbPath      string
	reportPath  string
	format      string
	artistSlug  string
	spotifyID   string
	market      string
	concurrency int
	offline     bool
	apply       bool
	quiet       bool
}

func parseArtistAuditProfilesOptions(args []string) (artistAuditProfilesOptions, error) {
	options := artistAuditProfilesOptions{catalogPath: defaultCatalogPath, dbPath: defaultDatabasePath}
	value := func(i *int, name string) (string, error) {
		*i++
		if *i >= len(args) {
			return "", fmt.Errorf("%s requires a value", name)
		}
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
		case "--catalog":
			options.catalogPath, err = value(&i, "--catalog")
		case "--db":
			options.dbPath, err = value(&i, "--db")
		case "--report":
			options.reportPath, err = value(&i, "--report")
		case "--format":
			options.format, err = value(&i, "--format")
		case "--artist":
			options.artistSlug, err = value(&i, "--artist")
		case "--spotify-id":
			options.spotifyID, err = value(&i, "--spotify-id")
		case "--market":
			options.market, err = value(&i, "--market")
		case "--concurrency":
			var raw string
			if raw, err = value(&i, "--concurrency"); err == nil {
				if options.concurrency, err = strconv.Atoi(raw); err != nil || options.concurrency < 1 {
					err = fmt.Errorf("--concurrency must be a positive integer")
				}
			}
		case "--offline":
			options.offline = true
		case "--apply":
			options.apply = true
		case "--quiet":
			options.quiet = true
		default:
			err = fmt.Errorf("unknown option %q", args[i])
		}
		if err != nil {
			return artistAuditProfilesOptions{}, err
		}
	}

	if options.format == "" {
		options.format = "markdown"
		if strings.HasSuffix(strings.ToLower(options.reportPath), ".json") {
			options.format = "json"
		}
	}
	if options.format != "markdown" && options.format != "json" {
		return artistAuditProfilesOptions{}, fmt.Errorf("--format must be markdown or json")
	}
	if options.reportPath == "" {
		options.reportPath = "profiles-audit.md"
		if options.format == "json" {
			options.reportPath = "profiles-audit.json"
		}
	}
	if options.apply && (options.artistSlug == "" || options.spotifyID == "") {
		return artistAuditProfilesOptions{}, fmt.Errorf("--apply requires --artist and --spotify-id")
	}
	if options.apply && options.offline {
		return artistAuditProfilesOptions{}, fmt.Errorf("--apply cannot be combined with --offline")
	}
	return options, nil
}

func executeArtistsAuditProfiles(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, auditProfilesUsage)
		return 0
	}
	options, err := parseArtistAuditProfilesOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-profiles: %v\n", err)
		return 2
	}

	source, musicBrainz, cleanup, err := auditProfilesSources(ctx, options, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-profiles: %v\n", err)
		return 1
	}
	defer cleanup()

	auditOptions := artists.AuditProfilesOptions{
		CatalogPath: options.catalogPath,
		ArtistSlug:  options.artistSlug,
		Offline:     options.offline,
		Concurrency: options.concurrency,
	}
	if !options.quiet {
		auditOptions.Progress = func(done, total int, result artists.ProfileAuditResult) {
			_, _ = fmt.Fprintf(stderr, "artists audit-profiles: %d/%d %s %s: %s\n", done, total, result.ArtistSlug, result.SpotifyID, result.Classification)
		}
	}
	report, err := artists.NewAuditProfiles(store, source, musicBrainz).Run(ctx, auditOptions)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-profiles: %v\n", err)
		return 1
	}

	if code := writeProfileAuditReport(report, options, stdout, stderr); code != 0 {
		return code
	}
	if !options.apply {
		return 0
	}

	result, err := artists.NewApplyProfileAudit(store).Run(ctx, options.catalogPath, artists.ApplyProfileAuditInput{
		Report: report, Slug: options.artistSlug, SpotifyID: options.spotifyID,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-profiles: apply: %v\n", err)
		if errors.Is(err, artists.ErrApplyBlocked) {
			return 3
		}
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "artists audit-profiles: %s/%s moved from %s to %s\n", result.Slug, result.SpotifyID, result.From, result.To)
	return 0
}

func writeProfileAuditReport(report artists.ProfileAuditReport, options artistAuditProfilesOptions, stdout, stderr io.Writer) int {
	var encoded []byte
	if options.format == "json" {
		var err error
		if encoded, err = artists.FormatProfileAuditJSON(report); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists audit-profiles: encode report: %v\n", err)
			return 1
		}
	} else {
		encoded = artists.FormatProfileAuditMarkdown(report)
	}
	if options.reportPath == "-" {
		_, _ = stdout.Write(encoded)
		return 0
	}
	if err := os.WriteFile(options.reportPath, encoded, 0o644); err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-profiles: write report: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote profile audit report: %s\n", options.reportPath)
	return 0
}

// auditProfilesSources wires the SQLite cache with, unless offline, the live
// Spotify and MusicBrainz clients.
func auditProfilesSources(ctx context.Context, options artistAuditProfilesOptions, stderr io.Writer) (artists.SpotifyProfileSource, artists.MusicBrainzReleaseGroupSearcher, func(), error) {
	cacheDatabase, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("open cache database: %w", err)
	}
	cleanup := func() { _ = cacheDatabase.Close() }
	if err := cacheDatabase.Migrate(ctx); err != nil {
		cleanup()
		return nil, nil, func() {}, fmt.Errorf("migrate cache database: %w", err)
	}

	if options.offline {
		return artists.NewCacheFirstProfileSource(cacheDatabase, nil), offlineMusicBrainz{}, cleanup, nil
	}

	clientID, clientSecret := os.Getenv("SPOTIFY_CLIENT_ID"), os.Getenv("SPOTIFY_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		cleanup()
		return nil, nil, func() {}, fmt.Errorf("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required unless --offline is set")
	}
	market := options.market
	if market == "" {
		market = os.Getenv("SPOTIFY_MARKET")
	}
	client, err := spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		Market:        market,
		MaxRetryAfter: spotifyMaxRetryAfter(),
		Progress:      spotifyProgress(stderr, options.quiet),
	})
	if err != nil {
		cleanup()
		return nil, nil, func() {}, err
	}
	musicBrainzClient := musicbrainz.NewClient(musicbrainz.Config{UserAgent: os.Getenv("MUSICBRAINZ_USER_AGENT")})
	return artists.NewCacheFirstProfileSource(cacheDatabase, spotifyadapter.NewProfileSource(client)), musicBrainzClient, cleanup, nil
}

// offlineMusicBrainz is never queried: the audit skips MusicBrainz when offline.
type offlineMusicBrainz struct{}

func (offlineMusicBrainz) SearchArtistAlbumReleaseGroups(context.Context, catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	return nil, artists.ErrSourceUnavailable
}
