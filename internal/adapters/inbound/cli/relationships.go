package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const relationshipsUsage = `usage: spotwufamily artists relationships <command> [options]

commands:
  list     [--slug S] [--type T] [--established]
  members  --group SLUG [--established]
  groups   --artist SLUG [--established]
  add      --type T --from A --to B [--status proposed|verified|rejected] [--confidence low|medium|high]
           [--start Y] [--end Y] [--era TEXT] [--commentary TEXT] [--reviewer NAME]
           [--evidence-type T --evidence-url URL --evidence-rationale TEXT] [--dry-run]
  review   --type T --from A --to B --status S [--reviewer NAME] [--confidence C] [--commentary TEXT]
           [--evidence-type T --evidence-url URL --evidence-rationale TEXT] [--dry-run]
  remove   --type T --from A --to B [--dry-run]

common: [--catalog data/artists.yaml]
types: member_of, producer_for, collective_or_imprint, collaborated_with, associated_with`

const renameUsage = "usage: spotwufamily artists rename --slug OLD --to NEW [--catalog data/artists.yaml] [--dry-run]"

type relationshipOptions struct {
	catalogPath string
	values      map[string]string
	dryRun      bool
	established bool
}

var relationshipValueFlags = []string{
	"--slug", "--type", "--group", "--artist", "--from", "--to", "--status", "--confidence", "--start", "--end",
	"--era", "--commentary", "--reviewer", "--evidence-type", "--evidence-url", "--evidence-rationale",
}

func parseRelationshipOptions(args []string, extra ...string) (relationshipOptions, error) {
	options := relationshipOptions{catalogPath: defaultCatalogPath, values: map[string]string{}}
	valueFlags := map[string]bool{"--catalog": true}
	for _, name := range append(append([]string{}, relationshipValueFlags...), extra...) {
		valueFlags[name] = true
	}
	for i := 0; i < len(args); i++ {
		name := args[i]
		switch {
		case name == "--dry-run":
			options.dryRun = true
		case name == "--established":
			options.established = true
		case valueFlags[name]:
			i++
			if i >= len(args) {
				return options, fmt.Errorf("%s requires a value", name)
			}
			if name == "--catalog" {
				options.catalogPath = args[i]
			} else {
				options.values[name] = args[i]
			}
		default:
			return options, fmt.Errorf("unknown option %q", name)
		}
	}
	return options, nil
}

func (o relationshipOptions) require(names ...string) error {
	for _, name := range names {
		if o.values[name] == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func (o relationshipOptions) key() catalog.RelationshipKey {
	return catalog.NewRelationshipKey(catalog.RelationshipType(o.values["--type"]), o.values["--from"], o.values["--to"])
}

func (o relationshipOptions) evidence() ([]catalog.Evidence, error) {
	if o.values["--evidence-type"] == "" && o.values["--evidence-url"] == "" && o.values["--evidence-rationale"] == "" {
		return nil, nil
	}
	if err := o.require("--evidence-type", "--evidence-rationale"); err != nil {
		return nil, fmt.Errorf("evidence needs --evidence-type and --evidence-rationale: %w", err)
	}
	return []catalog.Evidence{{
		Type: catalog.EvidenceType(o.values["--evidence-type"]), URL: o.values["--evidence-url"], Rationale: o.values["--evidence-rationale"],
	}}, nil
}

func executeArtistsRelationships(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if len(args) == 0 || hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, relationshipsUsage)
		return 0
	}
	command, rest := args[0], args[1:]
	options, err := parseRelationshipOptions(rest)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists relationships %s: %v\n", command, err)
		return 2
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "artists relationships %s: %v\n", command, err)
		if artists.IsInvalidRelationship(err) || errors.Is(err, catalog.ErrRelationshipExists) || errors.Is(err, catalog.ErrRelationshipNotFound) || errors.Is(err, catalog.ErrUnknownArtist) {
			return 2
		}
		return 1
	}
	query := artists.NewQueryRelationships(store)
	manage := artists.NewManageRelationships(store)

	switch command {
	case "list":
		filter := artists.RelationshipFilter{Slug: options.values["--slug"], EstablishedOnly: options.established}
		if t := options.values["--type"]; t != "" {
			filter.Types = []catalog.RelationshipType{catalog.RelationshipType(t)}
		}
		rels, err := query.List(ctx, options.catalogPath, filter)
		if err != nil {
			return fail(err)
		}
		for _, rel := range rels {
			_, _ = fmt.Fprintln(stdout, describeRelationship(rel))
		}
		_, _ = fmt.Fprintf(stdout, "relationships: %d\n", len(rels))
	case "members":
		if err := options.require("--group"); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships members: %v\n", err)
			return 2
		}
		entries, err := query.MembersOf(ctx, options.catalogPath, options.values["--group"], options.established)
		if err != nil {
			return fail(err)
		}
		for _, entry := range entries {
			_, _ = fmt.Fprintf(stdout, "%s (%s)%s\n", entry.Artist.Slug, entry.Artist.Name, relationshipSuffix(entry.Relationship))
		}
		_, _ = fmt.Fprintf(stdout, "members of %s: %d\n", options.values["--group"], len(entries))
	case "groups":
		if err := options.require("--artist"); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships groups: %v\n", err)
			return 2
		}
		entries, err := query.GroupsFor(ctx, options.catalogPath, options.values["--artist"], options.established)
		if err != nil {
			return fail(err)
		}
		for _, entry := range entries {
			_, _ = fmt.Fprintf(stdout, "%s (%s)%s\n", entry.Artist.Slug, entry.Artist.Name, relationshipSuffix(entry.Relationship))
		}
		_, _ = fmt.Fprintf(stdout, "groups of %s: %d\n", options.values["--artist"], len(entries))
	case "add":
		if err := options.require("--type", "--from", "--to"); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships add: %v\n", err)
			return 2
		}
		evidence, err := options.evidence()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships add: %v\n", err)
			return 2
		}
		status := catalog.RelationshipStatus(options.values["--status"])
		if status == "" {
			status = catalog.RelationshipProposed
		}
		change, err := manage.Add(ctx, options.catalogPath, catalog.Relationship{
			Type: catalog.RelationshipType(options.values["--type"]), From: options.values["--from"], To: options.values["--to"],
			Start: options.values["--start"], End: options.values["--end"], Era: options.values["--era"],
			Status: status, Confidence: catalog.CandidateConfidence(options.values["--confidence"]),
			Commentary: options.values["--commentary"], Reviewer: options.values["--reviewer"], Evidence: evidence,
		}, options.dryRun)
		if err != nil {
			return fail(err)
		}
		reportChange(stdout, "added", change)
	case "review":
		if err := options.require("--type", "--from", "--to", "--status"); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships review: %v\n", err)
			return 2
		}
		evidence, err := options.evidence()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships review: %v\n", err)
			return 2
		}
		key := options.key()
		current, err := query.List(ctx, options.catalogPath, artists.RelationshipFilter{Slug: key.From, Types: []catalog.RelationshipType{key.Type}})
		if err != nil {
			return fail(err)
		}
		var rel *catalog.Relationship
		for i := range current {
			if current[i].Key() == key {
				rel = &current[i]
			}
		}
		if rel == nil {
			return fail(fmt.Errorf("%w: %s", catalog.ErrRelationshipNotFound, key))
		}
		updated := *rel
		updated.Status = catalog.RelationshipStatus(options.values["--status"])
		updated.Evidence = append(append([]catalog.Evidence(nil), rel.Evidence...), evidence...)
		if v := options.values["--reviewer"]; v != "" {
			updated.Reviewer = v
		}
		if v := options.values["--confidence"]; v != "" {
			updated.Confidence = catalog.CandidateConfidence(v)
		}
		if v := options.values["--commentary"]; v != "" {
			updated.Commentary = v
		}
		updated.ReviewedAt = ""
		change, err := manage.Update(ctx, options.catalogPath, key, updated, options.dryRun)
		if err != nil {
			return fail(err)
		}
		reportChange(stdout, "reviewed", change)
	case "remove":
		if err := options.require("--type", "--from", "--to"); err != nil {
			_, _ = fmt.Fprintf(stderr, "artists relationships remove: %v\n", err)
			return 2
		}
		change, err := manage.Remove(ctx, options.catalogPath, options.key(), options.dryRun)
		if err != nil {
			return fail(err)
		}
		reportChange(stdout, "removed", change)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown relationships command %q\n\n%s\n", command, relationshipsUsage)
		return 2
	}

	return 0
}

func executeArtistsRename(ctx context.Context, args []string, stdout, stderr io.Writer, store artists.CatalogStore) int {
	if hasHelp(args) {
		_, _ = fmt.Fprintln(stdout, renameUsage)
		return 0
	}
	options, err := parseRelationshipOptions(args)
	if err == nil {
		err = options.require("--slug", "--to")
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists rename: %v\n", err)
		return 2
	}
	change, err := artists.NewManageRelationships(store).RenameArtist(ctx, options.catalogPath, options.values["--slug"], options.values["--to"], options.dryRun)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artists rename: %v\n", err)
		return 2
	}
	prefix := "artists rename"
	if change.DryRun {
		prefix += " dry-run"
	}
	_, _ = fmt.Fprintf(stdout, "%s: %s -> %s, relationships rewritten=%d\n", prefix, options.values["--slug"], options.values["--to"], len(change.Rewritten))
	for _, key := range change.Rewritten {
		_, _ = fmt.Fprintf(stdout, "  %s\n", key)
	}
	return 0
}

func reportChange(w io.Writer, verb string, change artists.RelationshipChange) {
	prefix := "relationship " + verb
	if change.DryRun {
		prefix = "dry-run, nothing saved: would have " + verb + " relationship"
	}
	switch {
	case change.After != nil:
		_, _ = fmt.Fprintf(w, "%s: %s\n", prefix, describeRelationship(*change.After))
	case change.Before != nil:
		_, _ = fmt.Fprintf(w, "%s: %s\n", prefix, describeRelationship(*change.Before))
	}
}

func describeRelationship(rel catalog.Relationship) string {
	return fmt.Sprintf("%s %s -> %s [%s]%s", rel.Type, rel.From, rel.To, rel.Status, relationshipSuffix(rel))
}

func relationshipSuffix(rel catalog.Relationship) string {
	parts := make([]string, 0, 3)
	if rel.Former() {
		parts = append(parts, "former")
	}
	if rel.Start != "" || rel.End != "" {
		parts = append(parts, fmt.Sprintf("%s..%s", dateOrUnknown(rel.Start), dateOrUnknown(rel.End)))
	}
	if rel.Era != "" {
		parts = append(parts, rel.Era)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func dateOrUnknown(value string) string {
	if value == "" {
		return "?"
	}
	return value
}
