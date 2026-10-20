package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/candidateinventory"
	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	defaultLedgerPath = "data/wu-candidates.yaml"

	artistsImportCandidatesUsage = "usage: spotwufamily artists import-candidates [--input inventory.json|csv] [--catalog data/artists.yaml] [--ledger data/wu-candidates.yaml] [--report candidates.md] [--apply] [--revert]\n" +
		"  Without --apply this is a dry run: nothing is written. --input is optional; without it only reviewed decisions are staged.\n" +
		"  --revert undoes staging (entries still disabled and without Spotify IDs)."
	artistsReviewCandidateUsage = "usage: spotwufamily artists review-candidate --name NAME --status accepted|rejected|needs_more_evidence|alias_of_existing --reviewer NAME\n" +
		"  [--reason TEXT] [--kind artist|group] [--category C] [--role C]... [--relation creator|member|producer|collaborator|association]\n" +
		"  [--group SLUG]... [--relation-claim TEXT] [--alias-of SLUG] [--alias NAME]... [--slug SLUG] [--external-id KIND=ID]...\n" +
		"  [--evidence 'type|https-url|rationale']... [--catalog data/artists.yaml] [--ledger data/wu-candidates.yaml] [--dry-run]"
)

func executeArtistsImportCandidates(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, artistsImportCandidatesUsage)
		return 0
	}
	options := artists.ImportCandidatesOptions{CatalogPath: defaultCatalogPath, LedgerPath: defaultLedgerPath}
	reportPath := ""
	values := map[string]*string{"--input": &options.InputPath, "--catalog": &options.CatalogPath, "--ledger": &options.LedgerPath, "--report": &reportPath}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			options.Apply = true
		case "--revert":
			options.Revert = true
		default:
			target, ok := values[args[i]]
			if !ok {
				_, _ = fmt.Fprintf(stderr, "artists import-candidates: unknown option %q\n", args[i])
				return 2
			}
			i++
			if i >= len(args) {
				_, _ = fmt.Fprintf(stderr, "artists import-candidates: %s requires a value\n", args[i-1])
				return 2
			}
			*target = args[i]
		}
	}
	if options.Revert && options.InputPath != "" {
		_, _ = fmt.Fprintln(stderr, "artists import-candidates: --revert cannot be combined with --input")
		return 2
	}

	ledgers := yamlcatalog.NewLedgerStore()
	report, err := artists.NewImportCandidates(candidateinventory.NewReader(), ledgers, store).Run(ctx, options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists import-candidates: %v\n", err)
		return 1
	}
	if reportPath != "" {
		if err := os.WriteFile(reportPath, artists.FormatImportCandidatesMarkdown(report), 0o644); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists import-candidates: write report: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "wrote candidate curation report: %s\n", reportPath)
	}

	mode := "dry-run"
	if report.Applied {
		mode = "applied"
	}
	_, _ = fmt.Fprintf(stdout, "artists import-candidates %s: read=%d changes=%d invalid=%d total=%d pending=%d accepted=%d rejected=%d needs_more_evidence=%d alias_of_existing=%d ledger=%s\n",
		mode, report.Read, len(report.Changes), len(report.Invalid), report.After.Total, report.After.Pending, report.After.Accepted,
		report.After.Rejected, report.After.NeedsMoreEvidence, report.After.AliasOfExisting, options.LedgerPath)
	return 0
}

func executeArtistsReviewCandidate(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, artistsReviewCandidateUsage)
		return 0
	}
	options := artists.ReviewCandidateOptions{CatalogPath: defaultCatalogPath, LedgerPath: defaultLedgerPath}
	review := &options.Review
	review.At = time.Now().UTC()
	var status, kind, category, relation string
	var roles, groups, aliases, externalIDs, evidence []string
	single := map[string]*string{
		"--catalog": &options.CatalogPath, "--ledger": &options.LedgerPath, "--name": &options.Name,
		"--status": &status, "--kind": &kind, "--reviewer": &review.Reviewer, "--reason": &review.Reason,
		"--category": &category, "--relation": &relation, "--relation-claim": &review.RelationClaim,
		"--alias-of": &review.AliasOf, "--slug": &review.ProposedSlug,
	}
	repeated := map[string]*[]string{
		"--role": &roles, "--group": &groups, "--alias": &aliases, "--external-id": &externalIDs, "--evidence": &evidence,
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--dry-run" {
			options.DryRun = true
			continue
		}
		name := args[i]
		i++
		if i >= len(args) {
			_, _ = fmt.Fprintf(stderr, "artists review-candidate: %s requires a value\n", name)
			return 2
		}
		switch {
		case single[name] != nil:
			*single[name] = args[i]
		case repeated[name] != nil:
			*repeated[name] = append(*repeated[name], args[i])
		default:
			_, _ = fmt.Fprintf(stderr, "artists review-candidate: unknown option %q\n", name)
			return 2
		}
	}
	if options.Name == "" || status == "" || review.Reviewer == "" {
		_, _ = fmt.Fprintln(stderr, "artists review-candidate: --name, --status and --reviewer are required")
		_, _ = fmt.Fprintln(stderr, artistsReviewCandidateUsage)
		return 2
	}
	review.Status = catalog.CandidateStatus(status)
	options.Kind = catalog.CandidateKind(kind)
	review.Category = catalog.Category(category)
	review.Relation = catalog.WuRelation(relation)
	review.RelationGroups = groups
	review.Aliases = aliases
	for _, role := range roles {
		review.Roles = append(review.Roles, catalog.Category(role))
	}
	if len(externalIDs) > 0 {
		review.ExternalIDs = map[string]string{}
		for _, pair := range externalIDs {
			key, value, ok := strings.Cut(pair, "=")
			if !ok || key == "" || value == "" {
				_, _ = fmt.Fprintf(stderr, "artists review-candidate: --external-id must be KIND=ID, got %q\n", pair)
				return 2
			}
			review.ExternalIDs[strings.ToLower(key)] = value
		}
	}
	for _, raw := range evidence {
		parts := strings.SplitN(raw, "|", 3)
		if len(parts) != 3 {
			_, _ = fmt.Fprintf(stderr, "artists review-candidate: --evidence must be 'type|url|rationale', got %q\n", raw)
			return 2
		}
		item := catalog.Evidence{Type: catalog.EvidenceType(parts[0]), URL: parts[1], Rationale: parts[2]}
		if issues := catalog.ValidateEvidence(item); len(issues) > 0 {
			_, _ = fmt.Fprintf(stderr, "artists review-candidate: invalid evidence: %s\n", issues[0].Error())
			return 2
		}
		review.Evidence = append(review.Evidence, item)
	}

	result, err := artists.NewReviewCandidate(yamlcatalog.NewLedgerStore(), store).Run(ctx, options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists review-candidate: %v\n", err)
		return 1
	}
	mode := "applied"
	if options.DryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(stdout, "artists review-candidate %s: name=%q status=%s evidence=%d ledger=%s\n", mode, result.Name, result.Status, len(result.Evidence), options.LedgerPath)
	return 0
}
