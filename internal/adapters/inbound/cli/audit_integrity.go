package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

const auditIntegrityUsage = "usage: spotwufamily artists audit-integrity [--offline] [--strict] [--format text|json] [--catalog data/artists.yaml]\n" +
	"exit codes: 0 pass, 1 errors (or warnings with --strict), 2 usage"

// executeArtistsAuditIntegrity is the offline CI gate for the editorial
// catalog. It reads only the YAML file: no network, database or secrets.
func executeArtistsAuditIntegrity(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, auditIntegrityUsage)
		return 0
	}
	catalogPath, format, strict := defaultCatalogPath, "text", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--offline":
			// The audit is always offline; the flag documents that in scripts.
		case "--strict":
			strict = true
		case "--catalog", "--format":
			if i+1 >= len(args) {
				_, _ = fmt.Fprintf(stderr, "artists audit-integrity: %s requires a value\n", args[i])
				return 2
			}
			i++
			if args[i-1] == "--catalog" {
				catalogPath = args[i]
			} else {
				format = args[i]
			}
		default:
			_, _ = fmt.Fprintf(stderr, "artists audit-integrity: unknown option %q\n%s\n", args[i], auditIntegrityUsage)
			return 2
		}
	}
	if format != "text" && format != "json" {
		_, _ = fmt.Fprintln(stderr, "artists audit-integrity: --format must be text or json")
		return 2
	}

	report, err := artists.NewAuditIntegrity(store).Run(ctx, catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists audit-integrity: %v\n", err)
		return 1
	}
	if format == "json" {
		encoded, err := artists.FormatIntegrityJSON(report)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "artists audit-integrity: encode report: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(encoded)
	} else {
		_, _ = stdout.Write(artists.FormatIntegrityText(report))
	}
	if report.Failed(strict) {
		return 1
	}
	return 0
}
