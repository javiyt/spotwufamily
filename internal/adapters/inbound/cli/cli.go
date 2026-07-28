package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/jsoncandidates"
	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

const defaultCatalogPath = "data/artists.yaml"

func Execute(args []string, stdout, stderr io.Writer) int {
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
		return executeArtists(ctx, args[1:], stdout, stderr, store)
	case "sync", "export", "audit", "build":
		return notImplemented(stderr, args[0])
	case "db":
		return executeReservedGroup(stderr, "db", args[1:], "migrate", "verify", "snapshot", "rebuild")
	case "site":
		return executeReservedGroup(stderr, "site", args[1:], "build")
	case "help", "--help", "-h":
		printRootHelp(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printRootHelp(stderr)
		return 2
	}
}

func executeReservedGroup(stderr io.Writer, group string, args []string, commands ...string) int {
	if len(args) == 0 || hasHelp(args) {
		_, _ = fmt.Fprintf(stderr, "%s commands planned for a later phase: %s\n", group, strings.Join(commands, ", "))
		return 2
	}

	for _, command := range commands {
		if args[0] == command {
			return notImplemented(stderr, group+" "+command)
		}
	}

	_, _ = fmt.Fprintf(stderr, "unknown %s command %q\n", group, args[0])
	return 2
}

func notImplemented(stderr io.Writer, command string) int {
	_, _ = fmt.Fprintf(stderr, "%s is planned for a later phase and is not implemented yet\n", command)
	return 2
}

func executeArtists(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if len(args) == 0 {
		printArtistsHelp(stdout)
		return 0
	}

	switch args[0] {
	case "validate":
		return executeArtistsValidate(ctx, args[1:], stdout, stderr, store)
	case "import-groups":
		return executeArtistsImportGroups(ctx, args[1:], stdout, stderr, store)
	case "resolve":
		return executeArtistsResolve(ctx, args[1:], stdout, stderr, store)
	case "help", "--help", "-h":
		printArtistsHelp(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown artists command %q\n\n", args[0])
		printArtistsHelp(stderr)
		return 2
	}
}

func executeArtistsResolve(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, "usage: spotwufamily artists resolve --non-interactive [--candidates candidates.json] [--report report.md] [--catalog data/artists.yaml] [--market ES]")
		return 0
	}

	options, err := parseResolveOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
		return 2
	}
	if !options.nonInteractive {
		_, _ = fmt.Fprintln(stderr, "interactive artist resolution is planned after the Spotify adapter is available")
		return 2
	}

	searcher, err := resolveSearcher(options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists resolve: %v\n", err)
		return 1
	}

	report, err := artists.NewResolveArtists(store, searcher).Run(ctx, options.catalogPath)
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
	nonInteractive bool
}

func parseResolveOptions(args []string) (resolveOptions, error) {
	options := resolveOptions{catalogPath: defaultCatalogPath, reportPath: "-"}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--non-interactive":
			options.nonInteractive = true
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

	return options, nil
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

	return spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Market:       market,
	})
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
	_, _ = fmt.Fprintln(w, "  artists resolve")
	_, _ = fmt.Fprintln(w, "  sync")
	_, _ = fmt.Fprintln(w, "  db migrate|verify|snapshot|rebuild")
	_, _ = fmt.Fprintln(w, "  export")
	_, _ = fmt.Fprintln(w, "  site build")
	_, _ = fmt.Fprintln(w, "  audit")
	_, _ = fmt.Fprintln(w, "  build")
}

func printArtistsHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: spotwufamily artists <command> [args]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "commands:")
	_, _ = fmt.Fprintln(w, "  import-groups [groups-path] [catalog-path]")
	_, _ = fmt.Fprintln(w, "  validate [catalog-path]")
	_, _ = fmt.Fprintln(w, "  resolve")
}
