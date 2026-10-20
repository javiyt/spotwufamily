package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

const reconcileUsage = "usage: spotwufamily artists reconcile --slug SLUG [--apply | --restore RETIRED_AT] [--format text|json] [--catalog data/artists.yaml] [--db data/catalog.db]"

type artistReconcileOptions struct {
	catalogPath string
	dbPath      string
	slug        string
	format      string
	restore     string
	apply       bool
}

func parseArtistReconcileOptions(args []string) (artistReconcileOptions, error) {
	options := artistReconcileOptions{catalogPath: defaultCatalogPath, dbPath: defaultDatabasePath, format: "text"}
	for i := 0; i < len(args); i++ {
		name := args[i]
		target := map[string]*string{
			"--catalog": &options.catalogPath,
			"--db":      &options.dbPath,
			"--slug":    &options.slug,
			"--format":  &options.format,
			"--restore": &options.restore,
		}[name]
		switch {
		case name == "--apply":
			options.apply = true
		case target != nil:
			i++
			if i >= len(args) {
				return artistReconcileOptions{}, fmt.Errorf("%s requires a value", name)
			}
			*target = args[i]
		default:
			return artistReconcileOptions{}, fmt.Errorf("unknown option %q", name)
		}
	}
	if options.slug == "" {
		return artistReconcileOptions{}, fmt.Errorf("--slug is required")
	}
	if options.format != "text" && options.format != "json" {
		return artistReconcileOptions{}, fmt.Errorf("--format must be text or json")
	}
	if options.apply && options.restore != "" {
		return artistReconcileOptions{}, fmt.Errorf("--apply and --restore cannot be combined")
	}

	return options, nil
}

func executeArtistsReconcile(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, reconcileUsage)
		return 0
	}
	options, err := parseArtistReconcileOptions(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
		return 2
	}

	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	report, err := artists.NewReconcileArtist(store, database).Run(ctx, artists.ReconcileInput{
		CatalogPath:      options.catalogPath,
		Slug:             options.slug,
		Apply:            options.apply,
		RestoreRetiredAt: options.restore,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
		return 1
	}

	if options.format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
			return 1
		}
		return 0
	}
	printReconcileReport(stdout, report)

	return 0
}

func printReconcileReport(w io.Writer, r artists.ReconcileReport) {
	mode := "applied"
	switch {
	case r.Restored != nil:
		mode = "restored"
	case r.DryRun:
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(w, "artists reconcile %s: slug=%s linked_ids=%s\n", mode, r.Slug, strings.Join(r.LinkedIDs, ","))
	if r.Restored != nil {
		_, _ = fmt.Fprintf(w, "restored retired_at=%s albums=%d tracks=%d\n", r.RetiredAt, r.Restored.Albums, r.Restored.Tracks)
		return
	}
	_, _ = fmt.Fprintf(w, "before: albums=%d tracks=%d\n", r.Before.Albums, r.Before.Tracks)
	_, _ = fmt.Fprintf(w, "supported: albums=%d tracks=%d\n", r.Supported.Albums, r.Supported.Tracks)
	_, _ = fmt.Fprintf(w, "orphan: albums=%d tracks=%d (shared with other artists: albums=%d tracks=%d; unreferenced after cleanup: albums=%d tracks=%d)\n",
		r.Orphan.Albums, r.Orphan.Tracks, r.SharedOrphans.Albums, r.SharedOrphans.Tracks, r.UnreferencedAfter.Albums, r.UnreferencedAfter.Tracks)
	_, _ = fmt.Fprintf(w, "unverifiable (kept): albums=%d tracks=%d\n", r.Unverifiable.Albums, r.Unverifiable.Tracks)
	_, _ = fmt.Fprintf(w, "after: albums=%d tracks=%d\n", r.After.Albums, r.After.Tracks)
	for _, item := range r.Items {
		if item.Class == artists.ReconcileSupported {
			continue
		}
		shared := ""
		if len(item.SharedWith) > 0 {
			shared = " shared_with=" + strings.Join(item.SharedWith, ",")
		}
		_, _ = fmt.Fprintf(w, "  %s %s %s %q credited=%s%s\n", item.Class, item.Kind, item.SpotifyID, item.Name, summarizeIDs(item.CreditedArtistIDs, 4), shared)
	}
	if r.DryRun {
		_, _ = fmt.Fprintln(w, "dry run: nothing changed; rerun with --apply to retire the orphan associations")
		return
	}
	if r.RetiredAt != "" {
		_, _ = fmt.Fprintf(w, "retired_at=%s (rollback: artists reconcile --slug %s --restore %s)\n", r.RetiredAt, r.Slug, r.RetiredAt)
	}
}

// summarizeIDs keeps compilation credit lists readable; the JSON format has them all.
func summarizeIDs(ids []string, limit int) string {
	if len(ids) <= limit {
		return strings.Join(ids, ",")
	}

	return fmt.Sprintf("%s,+%d more", strings.Join(ids[:limit], ","), len(ids)-limit)
}
