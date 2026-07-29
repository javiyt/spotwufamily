package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/jsoncandidates"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/musicbrainz"
	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/wikipedia"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const defaultCatalogPath = "data/artists.yaml"
const defaultDatabasePath = "data/catalog.db"
const defaultSnapshotPath = "data/catalog.snapshot.sql"
const defaultExportOutputDir = "site/data/generated"
const defaultExportStaticDir = "site/static"
const defaultExportContentDir = "site/content/generated"

func Execute(args []string, stdout, stderr io.Writer) int {
	return ExecuteWithInput(args, os.Stdin, stdout, stderr)
}

func ExecuteWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootHelp(stdout)
		return 0
	}

	ctx := context.Background()
	store := yamlcatalog.NewStore()

	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, "spotwufamily v2-dev")
		return 0
	case "artists":
		return executeArtists(ctx, args[1:], stdin, stdout, stderr, store)
	case "sync":
		return executeSync(ctx, args[1:], stdout, stderr, store)
	case "export":
		return executeExport(ctx, args[1:], stdout, stderr)
	case "audit":
		return executeAudit(ctx, args[1:], stdout, stderr, store)
	case "db":
		return executeDB(ctx, args[1:], stdout, stderr)
	case "site":
		return executeSite(ctx, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printRootHelp(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printRootHelp(stderr)
		return 2
	}
}

func executeSite(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || hasHelp(args) {
		printSiteHelp(stdout)
		return 0
	}
	if args[0] != "build" {
		_, _ = fmt.Fprintf(stderr, "unknown site command %q\n\n", args[0])
		printSiteHelp(stderr)
		return 2
	}

	source := "site"
	destination := "/tmp/spotwufamily-site"
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--source":
			i++
			if i >= len(args) {
				return optionError(stderr, "site build", "--source requires a value")
			}
			source = args[i]
		case "--destination":
			i++
			if i >= len(args) {
				return optionError(stderr, "site build", "--destination requires a value")
			}
			destination = args[i]
		default:
			return optionError(stderr, "site build", fmt.Sprintf("unknown option %q", args[i]))
		}
	}

	commandArgs := []string{"--source", source, "--minify"}
	if destination != "" {
		commandArgs = append(commandArgs, "--destination", destination)
	}
	cmd := exec.CommandContext(ctx, "hugo", commandArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintf(stderr, "site build: %v\n", err)
		return 1
	}

	return 0
}

func optionError(stderr io.Writer, command, message string) int {
	_, _ = fmt.Fprintf(stderr, "%s: %s\n", command, message)
	return 2
}

type syncOptions struct {
	catalogPath  string
	dbPath       string
	snapshotPath string
	artistSlug   string
	market       string
	full         bool
	dryRun       bool
}

func executeSync(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		printSyncHelp(stdout)
		return 0
	}

	options, err := parseSyncOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sync: %v\n", err)
		return 2
	}

	var repository *sqliteadapter.Database
	if !options.dryRun {
		repository, err = sqliteadapter.Open(options.dbPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "sync: %v\n", err)
			return 1
		}
		defer func() { _ = repository.Close() }()
		if err := repository.Migrate(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "sync: migrate database: %v\n", err)
			return 1
		}
	}

	var fetcher catalogsync.Fetcher
	if !options.dryRun {
		fetcher, err = syncFetcher(options)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "sync: %v\n", err)
			return 1
		}
	}

	report, err := catalogsync.NewSyncCatalog(store, fetcher, repository, catalogsync.SystemClock{}).Run(ctx, catalogsync.Options{
		CatalogPath: options.catalogPath,
		ArtistSlug:  options.artistSlug,
		Market:      options.market,
		Full:        options.full,
		DryRun:      options.dryRun,
	})

	printSyncReport(stdout, report)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sync: %v\n", err)
		for _, item := range report.Errors {
			_, _ = fmt.Fprintf(stderr, "- %s: %v\n", item.Slug, item.Err)
		}
		return 1
	}

	if !options.dryRun {
		if err := repository.WriteSnapshot(ctx, options.snapshotPath); err != nil {
			_, _ = fmt.Fprintf(stderr, "sync: write snapshot: %v\n", err)
			return 1
		}
	}

	return 0
}

func parseSyncOptions(args []string) (syncOptions, error) {
	options := syncOptions{catalogPath: defaultCatalogPath, dbPath: defaultDatabasePath, snapshotPath: defaultSnapshotPath}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return syncOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--db":
			i++
			if i >= len(args) {
				return syncOptions{}, fmt.Errorf("--db requires a value")
			}
			options.dbPath = args[i]
		case "--snapshot":
			i++
			if i >= len(args) {
				return syncOptions{}, fmt.Errorf("--snapshot requires a value")
			}
			options.snapshotPath = args[i]
		case "--artist":
			i++
			if i >= len(args) {
				return syncOptions{}, fmt.Errorf("--artist requires a value")
			}
			options.artistSlug = args[i]
		case "--market":
			i++
			if i >= len(args) {
				return syncOptions{}, fmt.Errorf("--market requires a value")
			}
			options.market = args[i]
		case "--full":
			options.full = true
		case "--dry-run":
			options.dryRun = true
		default:
			return syncOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}

	return options, nil
}

func syncFetcher(options syncOptions) (catalogsync.Fetcher, error) {
	clientID := os.Getenv("SPOTIFY_CLIENT_ID")
	clientSecret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required")
	}

	market := options.market
	if market == "" {
		market = os.Getenv("SPOTIFY_MARKET")
	}

	return spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Market:       market,
	})
}

func printSyncReport(stdout io.Writer, report catalogsync.Report) {
	mode := "sync"
	if report.DryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(stdout, "%s planned artists: %d\n", mode, report.ArtistsPlanned)
	_, _ = fmt.Fprintf(stdout, "processed: %d failed: %d skipped: %d\n", report.ArtistsProcessed, report.ArtistsFailed, report.ArtistsSkipped)
	if report.RunID > 0 {
		_, _ = fmt.Fprintf(stdout, "sync_run_id: %d\n", report.RunID)
	}
	_, _ = fmt.Fprintf(stdout, "albums: %d tracks: %d artist_albums: %d artist_tracks: %d\n",
		report.Stats.AlbumsUpserted,
		report.Stats.TracksUpserted,
		report.Stats.ArtistAlbumsUpserted,
		report.Stats.ArtistTracksUpserted,
	)
}

type auditOptions struct {
	catalogPath     string
	dbPath          string
	snapshotPath    string
	outputDir       string
	staticDir       string
	contentDir      string
	siteSource      string
	siteDestination string
	skipSite        bool
	skipGitDiff     bool
}

func executeAudit(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		printAuditHelp(stdout)
		return 0
	}

	options, err := parseAuditOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: %v\n", err)
		return 2
	}

	if issues, err := artists.NewValidateCatalog(store).Run(ctx, options.catalogPath); err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: validate catalog: %v\n", err)
		return 1
	} else if len(issues) > 0 {
		for _, issue := range issues {
			_, _ = fmt.Fprintf(stderr, "- %s\n", issue.Error())
		}
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "audit: catalog ok (%s)\n", options.catalogPath)

	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: open database: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	migrations, err := sqliteadapter.EmbeddedMigrations()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: load migrations: %v\n", err)
		return 1
	}
	report, err := database.Verify(ctx, migrations)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: verify database: %v\n", err)
		return 1
	}
	if err := verifySnapshotFreshness(ctx, database, options.snapshotPath); err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "audit: database ok (%d migrations)\n", report.Migrations)

	exportReport, err := catalogexport.NewExportCatalog(database, filesystem.NewWriter()).Run(ctx, catalogexport.Options{
		OutputDir:  options.outputDir,
		StaticDir:  options.staticDir,
		ContentDir: options.contentDir,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: export catalog: %v\n", err)
		return 1
	}
	if err := verifyExportArtifacts(options); err != nil {
		_, _ = fmt.Fprintf(stderr, "audit: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "audit: export ok (artists=%d albums=%d tracks=%d files_written=%d files_kept=%d)\n",
		exportReport.Artists,
		exportReport.Albums,
		exportReport.Tracks,
		exportReport.FilesWritten,
		exportReport.FilesKept,
	)

	if !options.skipGitDiff {
		if err := verifyGeneratedGitDiff(ctx, options); err != nil {
			_, _ = fmt.Fprintf(stderr, "audit: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "audit: generated git diff ok")
	}

	if !options.skipSite {
		if code := executeSite(ctx, []string{"build", "--source", options.siteSource, "--destination", options.siteDestination}, stdout, stderr); code != 0 {
			return code
		}
		_, _ = fmt.Fprintf(stdout, "audit: site build ok (%s)\n", options.siteDestination)
	}

	_, _ = fmt.Fprintln(stdout, "audit: ok")
	return 0
}

func parseAuditOptions(args []string) (auditOptions, error) {
	options := auditOptions{
		catalogPath:     defaultCatalogPath,
		dbPath:          defaultDatabasePath,
		snapshotPath:    defaultSnapshotPath,
		outputDir:       defaultExportOutputDir,
		staticDir:       defaultExportStaticDir,
		contentDir:      defaultExportContentDir,
		siteSource:      "site",
		siteDestination: "/tmp/spotwufamily-site",
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--db":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--db requires a value")
			}
			options.dbPath = args[i]
		case "--snapshot":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--snapshot requires a value")
			}
			options.snapshotPath = args[i]
		case "--output":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--output requires a value")
			}
			options.outputDir = args[i]
		case "--static":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--static requires a value")
			}
			options.staticDir = args[i]
		case "--content":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--content requires a value")
			}
			options.contentDir = args[i]
		case "--site-source":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--site-source requires a value")
			}
			options.siteSource = args[i]
		case "--site-destination":
			i++
			if i >= len(args) {
				return auditOptions{}, fmt.Errorf("--site-destination requires a value")
			}
			options.siteDestination = args[i]
		case "--skip-site":
			options.skipSite = true
		case "--skip-git-diff":
			options.skipGitDiff = true
		default:
			return auditOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}

	return options, nil
}

func verifySnapshotFreshness(ctx context.Context, database *sqliteadapter.Database, snapshotPath string) error {
	generated, err := database.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("generate snapshot: %w", err)
	}
	current, err := os.ReadFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", snapshotPath, err)
	}
	if !bytes.Equal(generated, current) {
		return fmt.Errorf("snapshot is out of date: %s", snapshotPath)
	}
	return nil
}

func verifyExportArtifacts(options auditOptions) error {
	required := []struct {
		path string
		dir  bool
	}{
		{path: filepath.Join(options.outputDir, "catalog-summary.json")},
		{path: filepath.Join(options.outputDir, "artists", "index.json")},
		{path: filepath.Join(options.outputDir, "albums", "index.json")},
		{path: filepath.Join(options.outputDir, "tracks", "index.json")},
		{path: filepath.Join(options.staticDir, "search-index.json")},
	}
	for _, item := range required {
		info, err := os.Stat(item.path)
		if err != nil {
			return fmt.Errorf("missing export artifact %s: %w", item.path, err)
		}
		if item.dir && !info.IsDir() {
			return fmt.Errorf("export artifact is not a directory: %s", item.path)
		}
		if !item.dir && info.IsDir() {
			return fmt.Errorf("export artifact is a directory: %s", item.path)
		}
	}
	return nil
}

func verifyGeneratedGitDiff(ctx context.Context, options auditOptions) error {
	if _, err := os.Stat(".git"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat .git: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "diff", "--exit-code", "--", options.outputDir, filepath.Join(options.staticDir, "search-index.json"))
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("generated files differ from Git: %s", strings.TrimSpace(output.String()))
	}
	return nil
}

type exportOptions struct {
	dbPath     string
	outputDir  string
	staticDir  string
	contentDir string
}

func executeExport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if hasHelp(args) {
		printExportHelp(stdout)
		return 0
	}

	options, err := parseExportOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "export: %v\n", err)
		return 2
	}

	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	migrations, err := sqliteadapter.EmbeddedMigrations()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}
	if _, err := database.Verify(ctx, migrations); err != nil {
		_, _ = fmt.Fprintf(stderr, "export: database is not verified: %v\n", err)
		return 1
	}

	report, err := catalogexport.NewExportCatalog(database, filesystem.NewWriter()).Run(ctx, catalogexport.Options{
		OutputDir:  options.outputDir,
		StaticDir:  options.staticDir,
		ContentDir: options.contentDir,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "export: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "exported catalog: artists=%d albums=%d tracks=%d files_written=%d files_kept=%d\n",
		report.Artists,
		report.Albums,
		report.Tracks,
		report.FilesWritten,
		report.FilesKept,
	)

	return 0
}

func parseExportOptions(args []string) (exportOptions, error) {
	options := exportOptions{dbPath: defaultDatabasePath, outputDir: defaultExportOutputDir, staticDir: defaultExportStaticDir, contentDir: defaultExportContentDir}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--db":
			i++
			if i >= len(args) {
				return exportOptions{}, fmt.Errorf("--db requires a value")
			}
			options.dbPath = args[i]
		case "--output":
			i++
			if i >= len(args) {
				return exportOptions{}, fmt.Errorf("--output requires a value")
			}
			options.outputDir = args[i]
		case "--static":
			i++
			if i >= len(args) {
				return exportOptions{}, fmt.Errorf("--static requires a value")
			}
			options.staticDir = args[i]
		case "--content":
			i++
			if i >= len(args) {
				return exportOptions{}, fmt.Errorf("--content requires a value")
			}
			options.contentDir = args[i]
		default:
			return exportOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}

	return options, nil
}

func executeDB(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || hasHelp(args) {
		printDBHelp(stdout)
		return 0
	}
	if len(args) > 1 && hasHelp(args[1:]) {
		printDBHelp(stdout)
		return 0
	}

	options, err := parseDBOptions(args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db %s: %v\n", args[0], err)
		return 2
	}

	switch args[0] {
	case "migrate":
		return executeDBMigrate(ctx, stdout, stderr, options)
	case "verify":
		return executeDBVerify(ctx, stdout, stderr, options)
	case "snapshot":
		return executeDBSnapshot(ctx, stdout, stderr, options)
	case "rebuild":
		return executeDBRebuild(ctx, stdout, stderr, options)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown db command %q\n\n", args[0])
		printDBHelp(stderr)
		return 2
	}
}

type dbOptions struct {
	dbPath       string
	snapshotPath string
}

func parseDBOptions(args []string) (dbOptions, error) {
	options := dbOptions{dbPath: defaultDatabasePath, snapshotPath: defaultSnapshotPath}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--db":
			i++
			if i >= len(args) {
				return dbOptions{}, fmt.Errorf("--db requires a value")
			}
			options.dbPath = args[i]
		case "--snapshot":
			i++
			if i >= len(args) {
				return dbOptions{}, fmt.Errorf("--snapshot requires a value")
			}
			options.snapshotPath = args[i]
		case "--help", "-h", "help":
			return options, nil
		default:
			return dbOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}

	return options, nil
}

func executeDBMigrate(ctx context.Context, stdout, stderr io.Writer, options dbOptions) int {
	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db migrate: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	if err := database.Migrate(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "db migrate: %v\n", err)
		return 1
	}
	if err := database.Optimize(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "db migrate: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "database migrated: %s\n", options.dbPath)
	return 0
}

func executeDBVerify(ctx context.Context, stdout, stderr io.Writer, options dbOptions) int {
	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db verify: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	migrations, err := sqliteadapter.EmbeddedMigrations()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db verify: %v\n", err)
		return 1
	}
	report, err := database.Verify(ctx, migrations)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db verify: %v\n", err)
		return 1
	}

	if _, err := os.Stat(options.snapshotPath); err == nil {
		generated, err := database.Snapshot(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "db verify: %v\n", err)
			return 1
		}
		current, err := os.ReadFile(options.snapshotPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "db verify: read snapshot: %v\n", err)
			return 1
		}
		if !bytes.Equal(generated, current) {
			_, _ = fmt.Fprintf(stderr, "db verify: snapshot is out of date: %s\n", options.snapshotPath)
			return 1
		}
		report.Checks = append(report.Checks, "snapshot")
	} else if err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(stderr, "db verify: stat snapshot: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "database verified: %s (%d migrations; checks: %s)\n", options.dbPath, report.Migrations, strings.Join(report.Checks, ", "))
	return 0
}

func executeDBSnapshot(ctx context.Context, stdout, stderr io.Writer, options dbOptions) int {
	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db snapshot: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	if err := database.WriteSnapshot(ctx, options.snapshotPath); err != nil {
		_, _ = fmt.Fprintf(stderr, "db snapshot: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "database snapshot written: %s\n", options.snapshotPath)
	return 0
}

func executeDBRebuild(ctx context.Context, stdout, stderr io.Writer, options dbOptions) int {
	if err := os.Remove(options.dbPath); err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(stderr, "db rebuild: remove database: %v\n", err)
		return 1
	}

	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "db rebuild: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	if err := database.Migrate(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "db rebuild: %v\n", err)
		return 1
	}
	if _, err := os.Stat(options.snapshotPath); err == nil {
		if err := sqliteadapter.RestoreSnapshot(ctx, database.DB(), options.snapshotPath); err != nil {
			_, _ = fmt.Fprintf(stderr, "db rebuild: %v\n", err)
			return 1
		}
	} else if err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(stderr, "db rebuild: stat snapshot: %v\n", err)
		return 1
	}
	if err := database.Optimize(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "db rebuild: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "database rebuilt: %s\n", options.dbPath)
	return 0
}

func executeArtists(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if len(args) == 0 {
		printArtistsHelp(stdout)
		return 0
	}

	switch args[0] {
	case "validate":
		return executeArtistsValidate(ctx, args[1:], stdout, stderr, store)
	case "import-groups":
		return executeArtistsImportGroups(ctx, args[1:], stdout, stderr, store)
	case "enable-with-ids":
		return executeArtistsEnableWithIDs(ctx, args[1:], stdout, stderr, store)
	case "discover-wu":
		return executeArtistsDiscoverWu(ctx, args[1:], stdout, stderr, store)
	case "refresh-genres":
		return executeArtistsRefreshGenres(ctx, args[1:], stdout, stderr, store)
	case "resolve":
		return executeArtistsResolve(ctx, args[1:], stdin, stdout, stderr, store)
	case "audit-albums":
		return executeArtistsAuditAlbums(ctx, args[1:], stdout, stderr, store)
	case "seed-db":
		return executeArtistsSeedDB(ctx, args[1:], stdout, stderr, store)
	case "help", "--help", "-h":
		printArtistsHelp(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown artists command %q\n\n", args[0])
		printArtistsHelp(stderr)
		return 2
	}
}

type artistsEnableWithIDsOptions struct {
	catalogPath string
	dryRun      bool
}

func executeArtistsEnableWithIDs(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists enable-with-ids [--catalog data/artists.yaml] [--dry-run]")
		return 0
	}

	options, err := parseArtistsEnableWithIDsOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists enable-with-ids: %v\n", err)
		return 2
	}

	c, err := store.Load(ctx, options.catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists enable-with-ids: load artist catalog: %v\n", err)
		return 1
	}

	enabled := 0
	alreadyEnabled := 0
	withoutIDs := 0
	for i := range c.Artists {
		if len(c.Artists[i].AllSpotifyIDs()) == 0 {
			withoutIDs++
			continue
		}
		if c.Artists[i].Enabled {
			alreadyEnabled++
			continue
		}
		c.Artists[i].Enabled = true
		enabled++
	}

	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		_, _ = fmt.Fprintf(stderr, "artists enable-with-ids: enabled catalog is invalid: %s\n", issues[0].Error())
		return 1
	}

	if !options.dryRun {
		if err := store.Save(ctx, options.catalogPath, c); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists enable-with-ids: save artist catalog: %v\n", err)
			return 1
		}
	}

	mode := "updated"
	if options.dryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(stdout, "artists enable-with-ids %s: enabled=%d already_enabled=%d without_spotify_ids=%d catalog=%s\n", mode, enabled, alreadyEnabled, withoutIDs, options.catalogPath)
	return 0
}

func parseArtistsEnableWithIDsOptions(args []string) (artistsEnableWithIDsOptions, error) {
	options := artistsEnableWithIDsOptions{catalogPath: defaultCatalogPath}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return artistsEnableWithIDsOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--dry-run":
			options.dryRun = true
		default:
			return artistsEnableWithIDsOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}
	return options, nil
}

type artistsDiscoverWuOptions struct {
	catalogPath     string
	reportPath      string
	wikipediaAPIURL string
	wikipediaPage   string
	apply           bool
}

func executeArtistsDiscoverWu(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists discover-wu [--catalog data/artists.yaml] [--report wu-discovery.md] [--apply] [--wikipedia-api-url URL] [--wikipedia-page TITLE]")
		return 0
	}

	options, err := parseArtistsDiscoverWuOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists discover-wu: %v\n", err)
		return 2
	}

	source := wikipedia.NewClient(wikipedia.Config{APIURL: options.wikipediaAPIURL, Page: options.wikipediaPage})
	report, err := artists.NewDiscoverWuFamily(store, source).Run(ctx, artists.DiscoverWuFamilyOptions{
		CatalogPath: options.catalogPath,
		Apply:       options.apply,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists discover-wu: %v\n", err)
		return 1
	}

	markdown := artists.FormatDiscoverWuFamilyMarkdown(report)
	if options.reportPath != "" {
		if err := os.WriteFile(options.reportPath, markdown, 0o644); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists discover-wu: write report: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "wrote Wu Family discovery report: %s\n", options.reportPath)
	}

	mode := "reported"
	if options.apply {
		mode = "applied"
	}
	_, _ = fmt.Fprintf(stdout, "artists discover-wu %s: found=%d existing=%d new=%d added=%d catalog=%s\n", mode, report.Found, len(report.Existing), len(report.New), len(report.Added), options.catalogPath)
	return 0
}

func parseArtistsDiscoverWuOptions(args []string) (artistsDiscoverWuOptions, error) {
	options := artistsDiscoverWuOptions{catalogPath: defaultCatalogPath, reportPath: "wu-discovery.md"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return artistsDiscoverWuOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--report":
			i++
			if i >= len(args) {
				return artistsDiscoverWuOptions{}, fmt.Errorf("--report requires a value")
			}
			options.reportPath = args[i]
		case "--wikipedia-api-url":
			i++
			if i >= len(args) {
				return artistsDiscoverWuOptions{}, fmt.Errorf("--wikipedia-api-url requires a value")
			}
			options.wikipediaAPIURL = args[i]
		case "--wikipedia-page":
			i++
			if i >= len(args) {
				return artistsDiscoverWuOptions{}, fmt.Errorf("--wikipedia-page requires a value")
			}
			options.wikipediaPage = args[i]
		case "--apply":
			options.apply = true
		default:
			return artistsDiscoverWuOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}
	return options, nil
}

type artistsRefreshGenresOptions struct {
	catalogPath string
	market      string
	dryRun      bool
}

func executeArtistsRefreshGenres(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists refresh-genres [--catalog data/artists.yaml] [--market ES] [--dry-run]")
		return 0
	}

	options, err := parseArtistsRefreshGenresOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists refresh-genres: %v\n", err)
		return 2
	}

	fetcher, err := spotifyArtistFetcher(options.market)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists refresh-genres: %v\n", err)
		return 1
	}

	report, err := artists.NewRefreshGenres(store, fetcher).Run(ctx, artists.RefreshGenresOptions{
		CatalogPath: options.catalogPath,
		DryRun:      options.dryRun,
	})
	mode := "updated"
	if options.dryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(stdout, "artists refresh-genres %s: artists_with_ids=%d updated=%d unchanged=%d without_genres=%d without_images=%d catalog=%s\n", mode, report.ArtistsWithIDs, report.Updated, report.Unchanged, len(report.WithoutGenres), len(report.WithoutImages), options.catalogPath)
	if len(report.WithoutGenres) > 0 {
		_, _ = fmt.Fprintf(stdout, "artists without Spotify genres: %s\n", strings.Join(report.WithoutGenres, ", "))
	}
	if len(report.WithoutImages) > 0 {
		_, _ = fmt.Fprintf(stdout, "artists without Spotify images: %s\n", strings.Join(report.WithoutImages, ", "))
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists refresh-genres: %v\n", err)
		for _, item := range report.Errors {
			_, _ = fmt.Fprintf(stderr, "- %s (%s): %v\n", item.Slug, item.ID, item.Err)
		}
		return 1
	}
	return 0
}

func parseArtistsRefreshGenresOptions(args []string) (artistsRefreshGenresOptions, error) {
	options := artistsRefreshGenresOptions{catalogPath: defaultCatalogPath}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return artistsRefreshGenresOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--market":
			i++
			if i >= len(args) {
				return artistsRefreshGenresOptions{}, fmt.Errorf("--market requires a value")
			}
			options.market = args[i]
		case "--dry-run":
			options.dryRun = true
		default:
			return artistsRefreshGenresOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}
	return options, nil
}

func spotifyArtistFetcher(market string) (artists.SpotifyArtistFetcher, error) {
	clientID := os.Getenv("SPOTIFY_CLIENT_ID")
	clientSecret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required")
	}
	if market == "" {
		market = os.Getenv("SPOTIFY_MARKET")
	}
	return spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Market:       market,
	})
}

type artistsSeedDBOptions struct {
	catalogPath string
	dbPath      string
}

func executeArtistsSeedDB(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists seed-db [--catalog data/artists.yaml] [--db data/catalog.db]")
		return 0
	}

	options, err := parseArtistsSeedDBOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists seed-db: %v\n", err)
		return 2
	}

	c, err := store.Load(ctx, options.catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists seed-db: load artist catalog: %v\n", err)
		return 1
	}
	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		_, _ = fmt.Fprintf(stderr, "artists seed-db: artist catalog is invalid: %s\n", issues[0].Error())
		return 1
	}

	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists seed-db: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	if err := database.Migrate(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "artists seed-db: migrate database: %v\n", err)
		return 1
	}
	if err := database.SaveConfiguredArtists(ctx, c.Artists); err != nil {
		_, _ = fmt.Fprintf(stderr, "artists seed-db: save configured artists: %v\n", err)
		return 1
	}

	spotifyIDs := 0
	for _, artist := range c.Artists {
		spotifyIDs += len(artist.AllSpotifyIDs())
	}
	_, _ = fmt.Fprintf(stdout, "seeded configured artists: artists=%d spotify_ids=%d db=%s\n", len(c.Artists), spotifyIDs, options.dbPath)
	return 0
}

func parseArtistsSeedDBOptions(args []string) (artistsSeedDBOptions, error) {
	options := artistsSeedDBOptions{catalogPath: defaultCatalogPath, dbPath: defaultDatabasePath}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return artistsSeedDBOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--db":
			i++
			if i >= len(args) {
				return artistsSeedDBOptions{}, fmt.Errorf("--db requires a value")
			}
			options.dbPath = args[i]
		default:
			return artistsSeedDBOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}
	return options, nil
}

type artistAlbumAuditOptions struct {
	catalogPath string
	reportPath  string
	artistSlug  string
	market      string
}

func executeArtistsAuditAlbums(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists audit-albums [--artist slug] [--catalog data/artists.yaml] [--market ES] [--report albums-audit.md]")
		return 0
	}

	options, err := parseArtistAlbumAuditOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-albums: %v\n", err)
		return 2
	}

	spotifyClient, err := artistAlbumAuditSpotifyFetcher(options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-albums: %v\n", err)
		return 1
	}
	musicBrainzClient := musicbrainz.NewClient(musicbrainz.Config{
		UserAgent: os.Getenv("MUSICBRAINZ_USER_AGENT"),
	})

	report, err := artists.NewAuditAlbums(store, spotifyClient, musicBrainzClient).Run(ctx, artists.AuditAlbumsOptions{
		CatalogPath: options.catalogPath,
		ArtistSlug:  options.artistSlug,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-albums: %v\n", err)
		for _, item := range report.Errors {
			_, _ = fmt.Fprintf(stderr, "- %s: %v\n", item.Slug, item.Err)
		}
		return 1
	}

	markdown := artists.FormatAuditAlbumsMarkdown(report)
	if options.reportPath == "" || options.reportPath == "-" {
		_, _ = stdout.Write(markdown)
	} else if err := os.WriteFile(options.reportPath, markdown, 0o644); err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-albums: write report: %v\n", err)
		return 1
	} else {
		_, _ = fmt.Fprintf(stdout, "wrote album audit report: %s\n", options.reportPath)
	}

	return 0
}

func parseArtistAlbumAuditOptions(args []string) (artistAlbumAuditOptions, error) {
	options := artistAlbumAuditOptions{catalogPath: defaultCatalogPath, reportPath: "albums-audit.md"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--catalog":
			i++
			if i >= len(args) {
				return artistAlbumAuditOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--report":
			i++
			if i >= len(args) {
				return artistAlbumAuditOptions{}, fmt.Errorf("--report requires a value")
			}
			options.reportPath = args[i]
		case "--artist":
			i++
			if i >= len(args) {
				return artistAlbumAuditOptions{}, fmt.Errorf("--artist requires a value")
			}
			options.artistSlug = args[i]
		case "--market":
			i++
			if i >= len(args) {
				return artistAlbumAuditOptions{}, fmt.Errorf("--market requires a value")
			}
			options.market = args[i]
		default:
			return artistAlbumAuditOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}

	return options, nil
}

func artistAlbumAuditSpotifyFetcher(options artistAlbumAuditOptions) (artists.SpotifyAlbumFetcher, error) {
	clientID := os.Getenv("SPOTIFY_CLIENT_ID")
	clientSecret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required")
	}

	market := options.market
	if market == "" {
		market = os.Getenv("SPOTIFY_MARKET")
	}

	return spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Market:       market,
	})
}

func executeArtistsResolve(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists resolve [--interactive|--non-interactive] [--review-all] [--apply] [--min-score 95] [--min-score-gap 10] [--enable-applied] [--candidates data/artist-candidates.example.json] [--report report.md] [--catalog data/artists.yaml] [--market ES]")
		return 0
	}

	options, err := parseResolveOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
		return 2
	}
	searcher, err := resolveSearcher(options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
		return 1
	}
	if options.interactive || !options.nonInteractive {
		return executeArtistsResolveInteractive(ctx, stdin, stdout, stderr, store, searcher, options)
	}

	resolver := artists.NewResolveArtists(store, searcher)
	var report artists.ResolveReport
	if options.apply {
		report, err = resolver.Apply(ctx, options.catalogPath, artists.ApplyResolveOptions{
			MinScore:       options.minScore,
			MinScoreGap:    options.minScoreGap,
			EnableResolved: options.enableApplied,
		})
	} else {
		report, err = resolver.Run(ctx, options.catalogPath)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
		return 1
	}

	markdown := artists.FormatResolveReportMarkdown(report)
	if options.reportPath == "" || options.reportPath == "-" {
		_, _ = stdout.Write(markdown)
	} else if err := os.WriteFile(options.reportPath, markdown, 0o644); err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: write report: %v\n", err)
		return 1
	} else {
		_, _ = fmt.Fprintf(stdout, "wrote artist resolution report: %s\n", options.reportPath)
	}
	if options.apply {
		_, _ = fmt.Fprintf(stdout, "applied resolved Spotify IDs: %d; skipped: %d\n", len(report.Applied), len(report.Skipped))
	}

	if len(report.Errors) > 0 {
		return 1
	}

	return 0
}

type resolveOptions struct {
	catalogPath    string
	candidatesPath string
	reportPath     string
	market         string
	interactive    bool
	nonInteractive bool
	reviewAll      bool
	apply          bool
	enableApplied  bool
	minScore       int
	minScoreGap    int
}

func parseResolveOptions(args []string) (resolveOptions, error) {
	options := resolveOptions{catalogPath: defaultCatalogPath, reportPath: "-", minScore: 95, minScoreGap: 10}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--interactive":
			options.interactive = true
		case "--review-all":
			options.reviewAll = true
		case "--non-interactive":
			options.nonInteractive = true
		case "--apply":
			options.apply = true
		case "--enable-applied":
			options.enableApplied = true
		case "--min-score":
			i++
			if i >= len(args) {
				return resolveOptions{}, fmt.Errorf("--min-score requires a value")
			}
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return resolveOptions{}, fmt.Errorf("--min-score must be an integer")
			}
			options.minScore = value
		case "--min-score-gap":
			i++
			if i >= len(args) {
				return resolveOptions{}, fmt.Errorf("--min-score-gap requires a value")
			}
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return resolveOptions{}, fmt.Errorf("--min-score-gap must be an integer")
			}
			options.minScoreGap = value
		case "--catalog":
			i++
			if i >= len(args) {
				return resolveOptions{}, fmt.Errorf("--catalog requires a value")
			}
			options.catalogPath = args[i]
		case "--candidates":
			i++
			if i >= len(args) {
				return resolveOptions{}, fmt.Errorf("--candidates requires a value")
			}
			options.candidatesPath = args[i]
		case "--report":
			i++
			if i >= len(args) {
				return resolveOptions{}, fmt.Errorf("--report requires a value")
			}
			options.reportPath = args[i]
		case "--market":
			i++
			if i >= len(args) {
				return resolveOptions{}, fmt.Errorf("--market requires a value")
			}
			options.market = args[i]
		default:
			return resolveOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}
	if options.interactive && options.nonInteractive {
		return resolveOptions{}, fmt.Errorf("--interactive and --non-interactive cannot be used together")
	}
	if options.apply && options.interactive {
		return resolveOptions{}, fmt.Errorf("--apply is only for --non-interactive mode; interactive mode applies selected candidates")
	}
	if options.apply && !options.nonInteractive {
		return resolveOptions{}, fmt.Errorf("--apply requires --non-interactive, or use interactive mode without --apply")
	}

	return options, nil
}

func executeArtistsResolveInteractive(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, store artists.CatalogStore, searcher artists.CandidateSearcher, options resolveOptions) int {
	c, err := store.Load(ctx, options.catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: load artist catalog: %v\n", err)
		return 1
	}

	reader := bufio.NewReader(stdin)
	applied := 0
	skipped := 0
	kept := 0
	cleared := 0
	for i := range c.Artists {
		artist := &c.Artists[i]
		if len(artist.AllSpotifyIDs()) > 0 && !options.reviewAll {
			continue
		}

		candidates, err := searcher.SearchArtistCandidates(ctx, *artist)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "artists resolve: search %s: %v\n", artist.Slug, err)
			skipped++
			continue
		}
		matches := catalog.RankCandidates(*artist, candidates)
		_, _ = fmt.Fprintf(stdout, "\n%s (%s)\n", artist.Name, artist.Slug)
		currentSpotifyIDs := artist.AllSpotifyIDs()
		if len(currentSpotifyIDs) > 0 {
			_, _ = fmt.Fprintln(stdout, "current Spotify IDs:")
			for index, spotifyID := range currentSpotifyIDs {
				marker := "additional"
				if index == 0 && spotifyID == artist.SpotifyID {
					marker = "primary"
				}
				_, _ = fmt.Fprintf(stdout, "  - %s | %s | %s\n", spotifyID, spotifyArtistURL(catalog.ArtistCandidate{SpotifyID: spotifyID}), marker)
			}
		}
		if len(artist.Aliases) > 0 {
			_, _ = fmt.Fprintf(stdout, "aliases: %s\n", strings.Join(artist.Aliases, ", "))
		}
		if len(matches) == 0 {
			_, _ = fmt.Fprintln(stdout, "no candidates found")
			if len(artist.AllSpotifyIDs()) == 0 {
				skipped++
				continue
			}
			action, done, code := promptKeepClearOrQuit(ctx, reader, stdout, stderr, store, options.catalogPath, c, applied, skipped, kept, cleared)
			if done {
				return code
			}
			switch action {
			case "keep":
				kept++
			case "clear":
				artist.SpotifyID = ""
				artist.SpotifyIDs = nil
				artist.Enabled = false
				cleared++
				if err := saveInteractiveResolve(ctx, store, options.catalogPath, c); err != nil {
					_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
					return 1
				}
			default:
				skipped++
			}
			continue
		}

		matches = filterCurrentArtistMatches(matches, *artist)
		if len(matches) == 0 {
			_, _ = fmt.Fprintln(stdout, "no new candidates found")
			if len(artist.AllSpotifyIDs()) == 0 {
				skipped++
				continue
			}
			action, done, code := promptKeepClearOrQuit(ctx, reader, stdout, stderr, store, options.catalogPath, c, applied, skipped, kept, cleared)
			if done {
				return code
			}
			switch action {
			case "keep":
				kept++
			case "clear":
				artist.SpotifyID = ""
				artist.SpotifyIDs = nil
				artist.Enabled = false
				cleared++
				if err := saveInteractiveResolve(ctx, store, options.catalogPath, c); err != nil {
					_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
					return 1
				}
			default:
				skipped++
			}
			continue
		}

		limit := len(matches)
		if limit > 10 {
			limit = 10
		}
		printInteractiveMatches(stdout, matches, limit)

		for {
			matches = filterCurrentArtistMatches(matches, *artist)
			if len(matches) < limit {
				limit = len(matches)
				printInteractiveMatches(stdout, matches, limit)
			}
			if len(artist.AllSpotifyIDs()) > 0 {
				_, _ = fmt.Fprint(stdout, "select candidate number to replace primary, aN or aN,aM=add extra IDs, k=keep current, c=clear all IDs, s=skip, q=save and quit: ")
			} else {
				_, _ = fmt.Fprint(stdout, "select candidate number, s=skip, q=save and quit: ")
			}
			line, readErr := reader.ReadString('\n')
			if readErr != nil && readErr != io.EOF {
				_, _ = fmt.Fprintf(stderr, "artists resolve: read selection: %v\n", readErr)
				return 1
			}
			choice := strings.ToLower(strings.TrimSpace(line))
			if choice == "" || choice == "s" || choice == "skip" {
				skipped++
				break
			}
			if len(artist.AllSpotifyIDs()) > 0 && (choice == "k" || choice == "keep") {
				kept++
				break
			}
			if len(artist.AllSpotifyIDs()) > 0 && (choice == "c" || choice == "clear") {
				artist.SpotifyID = ""
				artist.SpotifyIDs = nil
				artist.Enabled = false
				cleared++
				break
			}
			if choice == "q" || choice == "quit" {
				if err := saveInteractiveResolve(ctx, store, options.catalogPath, c); err != nil {
					_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
					return 1
				}
				printInteractiveResolveSummary(stdout, applied, skipped, kept, cleared)
				return 0
			}

			if strings.Contains(choice, ",") {
				additionalSelections, ok := parseAdditionalSelections(choice, limit)
				if !ok {
					_, _ = fmt.Fprintf(stdout, "invalid selection %q\n", choice)
					if readErr == io.EOF {
						skipped++
						break
					}
					continue
				}
				appliedAny := false
				for _, selected := range additionalSelections {
					result := applyInteractiveCandidate(stdout, c, artist, matches[selected-1].Candidate, true, options.enableApplied)
					if result.applied {
						applied++
						appliedAny = true
					}
				}
				if appliedAny {
					if err := saveInteractiveResolve(ctx, store, options.catalogPath, c); err != nil {
						_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
						return 1
					}
				}
				continue
			}

			additional := false
			selectedChoice := choice
			if strings.HasPrefix(choice, "a") && len(choice) > 1 {
				additional = true
				selectedChoice = strings.TrimPrefix(choice, "a")
			}
			selected, err := strconv.Atoi(selectedChoice)
			if err != nil || selected < 1 || selected > limit {
				_, _ = fmt.Fprintf(stdout, "invalid selection %q\n", choice)
				if readErr == io.EOF {
					skipped++
					break
				}
				continue
			}

			result := applyInteractiveCandidate(stdout, c, artist, matches[selected-1].Candidate, additional, options.enableApplied)
			if result.applied {
				applied++
				if err := saveInteractiveResolve(ctx, store, options.catalogPath, c); err != nil {
					_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
					return 1
				}
			}
			if additional {
				continue
			}
			if result.kept {
				kept++
			}
			if result.skipped {
				skipped++
			}
			break
		}
	}

	if err := saveInteractiveResolve(ctx, store, options.catalogPath, c); err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
		return 1
	}
	printInteractiveResolveSummary(stdout, applied, skipped, kept, cleared)
	return 0
}

func promptKeepClearOrQuit(ctx context.Context, reader *bufio.Reader, stdout, stderr io.Writer, store artists.CatalogStore, path string, c catalog.EditorialCatalog, applied, skipped, kept, cleared int) (string, bool, int) {
	for {
		_, _ = fmt.Fprint(stdout, "k=keep current, c=clear current, s=skip, q=save and quit: ")
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			_, _ = fmt.Fprintf(stderr, "artists resolve: read selection: %v\n", readErr)
			return "", true, 1
		}
		choice := strings.ToLower(strings.TrimSpace(line))
		switch choice {
		case "", "k", "keep":
			return "keep", false, 0
		case "c", "clear":
			return "clear", false, 0
		case "s", "skip":
			return "skip", false, 0
		case "q", "quit":
			if err := saveInteractiveResolve(ctx, store, path, c); err != nil {
				_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
				return "", true, 1
			}
			printInteractiveResolveSummary(stdout, applied, skipped, kept, cleared)
			return "", true, 0
		default:
			_, _ = fmt.Fprintf(stdout, "invalid selection %q\n", choice)
			if readErr == io.EOF {
				return "keep", false, 0
			}
		}
	}
}

func printInteractiveResolveSummary(stdout io.Writer, applied, skipped, kept, cleared int) {
	_, _ = fmt.Fprintf(stdout, "interactive resolve: applied=%d skipped=%d kept=%d cleared=%d\n", applied, skipped, kept, cleared)
}

func printInteractiveMatches(stdout io.Writer, matches []catalog.CandidateMatch, limit int) {
	if limit > len(matches) {
		limit = len(matches)
	}
	if limit == 0 {
		_, _ = fmt.Fprintln(stdout, "no new candidates found")
		return
	}
	for index := 0; index < limit; index++ {
		match := matches[index]
		candidate := match.Candidate
		genres := strings.Join(candidate.Genres, ", ")
		if genres == "" {
			genres = "-"
		}
		_, _ = fmt.Fprintf(stdout, "  %d. %s | %s | %s | score=%d confidence=%s popularity=%d followers=%d genres=%s\n",
			index+1,
			candidate.Name,
			candidate.SpotifyID,
			spotifyArtistURL(candidate),
			match.Score,
			match.Confidence,
			candidate.Popularity,
			candidate.Followers,
			genres,
		)
	}
}

type interactiveCandidateResult struct {
	applied bool
	kept    bool
	skipped bool
}

func applyInteractiveCandidate(stdout io.Writer, c catalog.EditorialCatalog, artist *catalog.Artist, candidate catalog.ArtistCandidate, additional, enableApplied bool) interactiveCandidateResult {
	if candidate.SpotifyID == "" {
		_, _ = fmt.Fprintln(stdout, "candidate has no Spotify ID; skipped")
		return interactiveCandidateResult{skipped: true}
	}
	if artist.HasSpotifyID(candidate.SpotifyID) {
		_, _ = fmt.Fprintf(stdout, "Spotify ID already configured for %s\n", artist.Name)
		return interactiveCandidateResult{kept: true}
	}
	if existing := spotifyIDOwner(c, candidate.SpotifyID, artist.Slug); existing != "" {
		_, _ = fmt.Fprintf(stdout, "Spotify ID already belongs to %s; skipped\n", existing)
		return interactiveCandidateResult{skipped: true}
	}
	if additional {
		artist.SpotifyIDs = append(artist.SpotifyIDs, candidate.SpotifyID)
		_, _ = fmt.Fprintf(stdout, "added extra Spotify ID: %s | %s\n", candidate.SpotifyID, spotifyArtistURL(candidate))
	} else {
		artist.SpotifyID = candidate.SpotifyID
	}
	if enableApplied {
		artist.Enabled = true
	}
	return interactiveCandidateResult{applied: true}
}

func parseAdditionalSelections(choice string, limit int) ([]int, bool) {
	parts := strings.Split(choice, ",")
	selected := make([]int, 0, len(parts))
	seen := map[int]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "a") || len(part) < 2 {
			return nil, false
		}
		value, err := strconv.Atoi(strings.TrimPrefix(part, "a"))
		if err != nil || value < 1 || value > limit {
			return nil, false
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		selected = append(selected, value)
	}
	if len(selected) == 0 {
		return nil, false
	}
	return selected, true
}

func filterCurrentArtistMatches(matches []catalog.CandidateMatch, artist catalog.Artist) []catalog.CandidateMatch {
	filtered := make([]catalog.CandidateMatch, 0, len(matches))
	for _, match := range matches {
		if artist.HasSpotifyID(match.Candidate.SpotifyID) {
			continue
		}
		filtered = append(filtered, match)
	}
	return filtered
}

func saveInteractiveResolve(ctx context.Context, store artists.CatalogStore, path string, c catalog.EditorialCatalog) error {
	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		messages := make([]string, 0, len(issues))
		for _, issue := range issues {
			messages = append(messages, issue.Error())
		}
		return fmt.Errorf("resolved catalog is invalid: %s", strings.Join(messages, "; "))
	}
	if err := store.Save(ctx, path, c); err != nil {
		return fmt.Errorf("save resolved artist catalog: %w", err)
	}
	return nil
}

func spotifyIDOwner(c catalog.EditorialCatalog, spotifyID, currentSlug string) string {
	for _, artist := range c.Artists {
		if artist.Slug != currentSlug && artist.HasSpotifyID(spotifyID) {
			return artist.Slug
		}
	}
	return ""
}

func spotifyArtistURL(candidate catalog.ArtistCandidate) string {
	if candidate.URL != "" {
		return candidate.URL
	}
	if candidate.SpotifyID == "" {
		return "-"
	}
	return "https://open.spotify.com/artist/" + candidate.SpotifyID
}

func resolveSearcher(options resolveOptions) (artists.CandidateSearcher, error) {
	if options.candidatesPath != "" {
		return jsoncandidates.NewSearcher(options.candidatesPath)
	}

	clientID := os.Getenv("SPOTIFY_CLIENT_ID")
	clientSecret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required when --candidates is not provided")
	}

	market := options.market
	if market == "" {
		market = os.Getenv("SPOTIFY_MARKET")
	}

	spotifyClient, err := spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Market:       market,
	})
	if err != nil {
		return nil, err
	}
	musicBrainzClient := musicbrainz.NewClient(musicbrainz.Config{
		UserAgent: os.Getenv("MUSICBRAINZ_USER_AGENT"),
	})

	return artists.NewAlbumEvidenceCandidateSearcher(spotifyClient, spotifyClient, musicBrainzClient), nil
}

func executeArtistsValidate(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists validate [catalog-path]")
		return 0
	}

	path := defaultCatalogPath
	if len(args) > 0 {
		path = args[0]
	}

	issues, err := artists.NewValidateCatalog(store).Run(ctx, path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "validate artists: %v\n", err)
		return 1
	}
	if len(issues) > 0 {
		for _, issue := range issues {
			_, _ = fmt.Fprintf(stderr, "- %s\n", issue.Error())
		}
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "artist catalog is valid: %s\n", path)
	return 0
}

func executeArtistsImportGroups(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists import-groups [groups-path] [catalog-path]")
		return 0
	}

	groupsPath := "data/groups.txt"
	if len(args) > 0 {
		groupsPath = args[0]
	}
	outputPath := defaultCatalogPath
	if len(args) > 1 {
		outputPath = args[1]
	}

	file, err := os.Open(groupsPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open groups file: %v\n", err)
		return 1
	}
	defer func() { _ = file.Close() }()

	result, err := artists.NewImportGroups(store).Run(ctx, file, outputPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "import groups: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(stdout, "imported %d artists into %s\n", result.Artists, outputPath)
	if len(result.ExactDuplicates) > 0 {
		_, _ = fmt.Fprintf(stdout, "exact duplicates skipped: %s\n", strings.Join(result.ExactDuplicates, ", "))
	}
	if len(result.NormalizedDuplicates) > 0 {
		_, _ = fmt.Fprintf(stdout, "normalized duplicates found: %s\n", strings.Join(result.NormalizedDuplicates, ", "))
	}

	return 0
}

func hasHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")
}

func printRootHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily <command> [args]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "commands:")
	_, _ = fmt.Fprintln(w, "  version")
	_, _ = fmt.Fprintln(w, "  artists import-groups")
	_, _ = fmt.Fprintln(w, "  artists validate")
	_, _ = fmt.Fprintln(w, "  artists enable-with-ids")
	_, _ = fmt.Fprintln(w, "  artists discover-wu")
	_, _ = fmt.Fprintln(w, "  artists refresh-genres")
	_, _ = fmt.Fprintln(w, "  artists resolve")
	_, _ = fmt.Fprintln(w, "  artists seed-db")
	_, _ = fmt.Fprintln(w, "  artists audit-albums")
	_, _ = fmt.Fprintln(w, "  sync")
	_, _ = fmt.Fprintln(w, "  db migrate|verify|snapshot|rebuild")
	_, _ = fmt.Fprintln(w, "  export")
	_, _ = fmt.Fprintln(w, "  site build")
	_, _ = fmt.Fprintln(w, "  audit")
}

func printArtistsHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily artists <command> [args]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "commands:")
	_, _ = fmt.Fprintln(w, "  import-groups [groups-path] [catalog-path]")
	_, _ = fmt.Fprintln(w, "  validate [catalog-path]")
	_, _ = fmt.Fprintln(w, "  enable-with-ids")
	_, _ = fmt.Fprintln(w, "  discover-wu")
	_, _ = fmt.Fprintln(w, "  refresh-genres")
	_, _ = fmt.Fprintln(w, "  resolve")
	_, _ = fmt.Fprintln(w, "  seed-db")
	_, _ = fmt.Fprintln(w, "  audit-albums")
}

func printSyncHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily sync [--artist slug] [--full] [--dry-run] [--market ES] [--catalog data/artists.yaml] [--db data/catalog.db] [--snapshot data/catalog.snapshot.sql]")
}

func printExportHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily export [--db data/catalog.db] [--output site/data/generated] [--static site/static] [--content site/content/generated]")
}

func printSiteHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily site build [--source site] [--destination /tmp/spotwufamily-site]")
}

func printAuditHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily audit [--catalog data/artists.yaml] [--db data/catalog.db] [--snapshot data/catalog.snapshot.sql] [--output site/data/generated] [--static site/static] [--content site/content/generated] [--site-source site] [--site-destination /tmp/spotwufamily-site] [--skip-site] [--skip-git-diff]")
}

func printDBHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily db <command> [--db data/catalog.db] [--snapshot data/catalog.snapshot.sql]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "commands:")
	_, _ = fmt.Fprintln(w, "  migrate")
	_, _ = fmt.Fprintln(w, "  verify")
	_, _ = fmt.Fprintln(w, "  snapshot")
	_, _ = fmt.Fprintln(w, "  rebuild")
}
