package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/artists"
)

const (
	reconcileUsage = `usage: spotwufamily artists reconcile --artist SLUG [--dry-run | --apply --confirm PLAN_CHECKSUM | --restore RETIRED_AT]
                                    [--format text|json|markdown] [--backup-dir build/reconcile-backups]
                                    [--catalog data/artists.yaml] [--db data/catalog.db]
       spotwufamily artists reconcile --status [--format text|json] [--catalog data/artists.yaml] [--db data/catalog.db]`

	defaultReconcileBackupDir = "build/reconcile-backups"
)

type artistReconcileOptions struct {
	catalogPath string
	dbPath      string
	slug        string
	format      string
	restore     string
	confirm     string
	backupDir   string
	apply       bool
	dryRun      bool
	status      bool
}

func parseArtistReconcileOptions(args []string) (artistReconcileOptions, error) {
	options := artistReconcileOptions{catalogPath: defaultCatalogPath, dbPath: defaultDatabasePath, format: "text", backupDir: defaultReconcileBackupDir}
	for i := 0; i < len(args); i++ {
		name := args[i]
		target := map[string]*string{
			"--catalog":    &options.catalogPath,
			"--db":         &options.dbPath,
			"--artist":     &options.slug,
			"--slug":       &options.slug,
			"--format":     &options.format,
			"--restore":    &options.restore,
			"--confirm":    &options.confirm,
			"--backup-dir": &options.backupDir,
		}[name]
		switch {
		case name == "--apply":
			options.apply = true
		case name == "--dry-run":
			options.dryRun = true
		case name == "--status":
			options.status = true
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
	if options.format != "text" && options.format != "json" && options.format != "markdown" {
		return artistReconcileOptions{}, fmt.Errorf("--format must be text, json or markdown")
	}
	if options.status {
		if options.apply || options.dryRun || options.restore != "" || options.format == "markdown" {
			return artistReconcileOptions{}, fmt.Errorf("--status only accepts --format text|json")
		}
		return options, nil
	}
	if options.slug == "" {
		return artistReconcileOptions{}, fmt.Errorf("--artist is required")
	}
	if options.apply && options.restore != "" {
		return artistReconcileOptions{}, fmt.Errorf("--apply and --restore cannot be combined")
	}
	if options.apply && options.dryRun {
		return artistReconcileOptions{}, fmt.Errorf("--apply and --dry-run cannot be combined")
	}
	if options.dryRun && options.restore != "" {
		return artistReconcileOptions{}, fmt.Errorf("--dry-run and --restore cannot be combined")
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

	if options.apply {
		release, ok := lockDatabase(options.dbPath, "artists reconcile", stderr)
		if !ok {
			return 1
		}
		defer release()
	}

	database, err := sqliteadapter.Open(options.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	use := artists.NewReconcileArtist(store, database)
	if options.status {
		return executeReconcileStatus(ctx, use, options, stdout, stderr)
	}

	// Dry runs must stay write-free, so only mutating modes migrate the schema.
	if options.apply || options.restore != "" {
		if err := database.Migrate(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists reconcile: migrate database: %v\n", err)
			return 1
		}
	}

	report, err := use.Run(ctx, artists.ReconcileInput{
		CatalogPath:      options.catalogPath,
		Slug:             options.slug,
		Apply:            options.apply,
		ConfirmChecksum:  options.confirm,
		BackupDir:        options.backupDir,
		RestoreRetiredAt: options.restore,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
		return 1
	}

	switch options.format {
	case "json":
		data, err := artists.FormatReconcileJSON(report)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(data)
	case "markdown":
		_, _ = stdout.Write(artists.FormatReconcileMarkdown(report))
	default:
		printReconcileReport(stdout, report)
	}

	return 0
}

func executeReconcileStatus(ctx context.Context, use artists.ReconcileArtist, options artistReconcileOptions, stdout, stderr io.Writer) int {
	status, err := use.Status(ctx, options.catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
		return 1
	}
	if options.format == "json" {
		data, err := artists.FormatPendingJSON(status)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "artists reconcile: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(data)
		return 0
	}
	if len(status.Pending) == 0 {
		_, _ = fmt.Fprintln(stdout, "no pending reconciliations: YAML and SQLite identity mappings agree")
		return 0
	}
	printPendingReconciliations(stdout, status.Pending)

	return 0
}

// printPendingReconciliations is the warning shown by every command that can
// leave YAML and SQLite out of alignment.
func printPendingReconciliations(w io.Writer, pending []artists.PendingReconciliation) {
	if len(pending) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: %d pending reconciliation(s): YAML and SQLite identity mappings are not aligned\n", len(pending))
	for _, p := range pending {
		_, _ = fmt.Fprintf(w, "  %s %s previous=%s target=%s", p.Slug, p.Status, strings.Join(p.PreviousIDs, ","), strings.Join(p.TargetIDs, ","))
		if p.LastError != "" {
			_, _ = fmt.Fprintf(w, " last_error=%q", p.LastError)
		}
		_, _ = fmt.Fprintln(w)
		switch p.Status {
		case artists.StatusAwaitingResync:
			_, _ = fmt.Fprintf(w, "    next: spotwufamily sync --artist %s --resume\n", p.Slug)
		default:
			_, _ = fmt.Fprintf(w, "    next: spotwufamily artists reconcile --artist %s --dry-run\n", p.Slug)
		}
	}
}

// pendingReconciliationReader is the database, or a runner that owns one.
type pendingReconciliationReader interface {
	PendingReconciliations(context.Context) ([]artists.PendingReconciliation, error)
}

func warnPendingReconciliations(ctx context.Context, w io.Writer, database pendingReconciliationReader) {
	pending, err := database.PendingReconciliations(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(w, "warning: cannot read pending reconciliations: %v\n", err)
		return
	}
	printPendingReconciliations(w, pending)
}

func printReconcileReport(w io.Writer, r artists.ReconcileReport) {
	mode := r.Status
	if r.DryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(w, "artists reconcile %s: slug=%s linked_ids=%s\n", mode, r.Slug, strings.Join(r.LinkedIDs, ","))
	if r.Restored != nil {
		_, _ = fmt.Fprintf(w, "restored retired_at=%s albums=%d tracks=%d\n", r.RetiredAt, r.Restored.Albums, r.Restored.Tracks)
		_, _ = fmt.Fprintf(w, "pending: rerun artists reconcile --artist %s --dry-run to review the mapping again\n", r.Slug)
		return
	}
	_, _ = fmt.Fprintf(w, "identity: baseline=%s previous=%s current=%s added=%s removed=%s\n",
		r.Identity.BaselineSource, strings.Join(r.Identity.Previous, ","), strings.Join(r.Identity.Current, ","),
		strings.Join(r.Identity.Added, ","), strings.Join(r.Identity.Removed, ","))
	_, _ = fmt.Fprintf(w, "before: albums=%d tracks=%d\n", r.Before.Albums, r.Before.Tracks)
	_, _ = fmt.Fprintf(w, "supported: albums=%d tracks=%d\n", r.Supported.Albums, r.Supported.Tracks)
	_, _ = fmt.Fprintf(w, "orphan: albums=%d tracks=%d (shared with other artists: albums=%d tracks=%d; unreferenced after cleanup: albums=%d tracks=%d)\n",
		r.Orphan.Albums, r.Orphan.Tracks, r.SharedOrphans.Albums, r.SharedOrphans.Tracks, r.UnreferencedAfter.Albums, r.UnreferencedAfter.Tracks)
	_, _ = fmt.Fprintf(w, "unverifiable (kept): albums=%d tracks=%d\n", r.Unverifiable.Albums, r.Unverifiable.Tracks)
	_, _ = fmt.Fprintf(w, "after: albums=%d tracks=%d\n", r.After.Albums, r.After.Tracks)
	_, _ = fmt.Fprintf(w, "sync state: stale_checkpoints=%d metadata_cache_stale=%t resync_required=%t\n", len(r.StaleCheckpoints), r.MetadataStale, r.ResyncRequired)
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
	_, _ = fmt.Fprintf(w, "plan_checksum=%s\n", r.PlanChecksum)
	switch {
	case r.DryRun && r.NeedsChanges():
		_, _ = fmt.Fprintf(w, "dry run: nothing changed; apply with: artists reconcile --artist %s --apply --confirm %s\n", r.Slug, r.PlanChecksum)
	case r.DryRun:
		_, _ = fmt.Fprintln(w, "dry run: nothing changed; nothing to reconcile")
	case r.Backup != nil:
		_, _ = fmt.Fprintf(w, "backup=%s sha256=%s snapshot_sha256=%s\n", r.Backup.Path, r.Backup.SHA256, r.Backup.SnapshotChecksum)
		_, _ = fmt.Fprintf(w, "retired_at=%s (rollback: artists reconcile --artist %s --restore %s)\n", r.RetiredAt, r.Slug, r.RetiredAt)
		if r.Status == artists.StatusAwaitingResync {
			_, _ = fmt.Fprintf(w, "pending: resync the replacement IDs with: sync --artist %s --resume\n", r.Slug)
		}
	default:
		_, _ = fmt.Fprintln(w, "already reconciled: nothing changed")
	}
}

// summarizeIDs keeps compilation credit lists readable; the JSON format has them all.
func summarizeIDs(ids []string, limit int) string {
	if len(ids) <= limit {
		return strings.Join(ids, ",")
	}

	return fmt.Sprintf("%s,+%d more", strings.Join(ids[:limit], ","), len(ids)-limit)
}
