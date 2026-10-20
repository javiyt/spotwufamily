package artists

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// CandidateInput is one row of an external candidate inventory. Everything in
// it is an unverified claim from the named source.
type CandidateInput struct {
	Name             string
	Kind             string
	Aliases          []string
	Confidence       string
	EvidenceCategory string
	RelationClaim    string
	Functions        []string
	SourceTitle      string
	SourceURL        string
	ProposedSlug     string
	ProposedCategory string
	// AliasOf names (not slugs) the entry this name is claimed to be a variant of.
	AliasOf     string
	ExternalIDs map[string]string
	Note        string
}

type CandidateInventoryReader interface {
	ReadCandidates(ctx context.Context, path string) ([]CandidateInput, error)
}

// CandidateLedgerStore persists the candidate ledger. A missing file loads as
// an empty ledger so discovery works before any import.
type CandidateLedgerStore interface {
	LoadLedger(ctx context.Context, path string) (catalog.CandidateLedger, error)
	SaveLedger(ctx context.Context, path string, ledger catalog.CandidateLedger) error
}

type ImportCandidatesOptions struct {
	// InputPath may be empty to only stage already reviewed decisions.
	InputPath   string
	CatalogPath string
	LedgerPath  string
	Apply       bool
	// Revert undoes staging instead of importing or staging.
	Revert bool
}

type CandidateChange struct {
	Name    string
	Key     string
	Action  string
	Status  catalog.CandidateStatus
	Slug    string
	Details string
}

// CandidateCounts summarises a ledger by decision.
type CandidateCounts struct {
	Total             int
	Pending           int
	Accepted          int
	Rejected          int
	NeedsMoreEvidence int
	AliasOfExisting   int
}

type ImportCandidatesReport struct {
	Read    int
	Changes []CandidateChange
	Invalid []string
	Before  CandidateCounts
	After   CandidateCounts
	// Ledger is the ledger as it is (or, in a dry run, would be) after the run.
	Ledger  catalog.CandidateLedger
	Applied bool
}

const (
	ActionAdded           = "added"
	ActionAutoAlias       = "alias_of_existing"
	ActionKnown           = "known"
	ActionSuppressed      = "suppressed_rejected"
	ActionMergedSource    = "merged_provenance"
	ActionStaged          = "staged"
	ActionAliasApplied    = "alias_applied"
	ActionReverted        = "reverted"
	ActionRevertKept      = "revert_kept"
	ActionDuplicateInFile = "duplicate_in_input"
)

type ImportCandidates struct {
	reader  CandidateInventoryReader
	ledgers CandidateLedgerStore
	catalog CatalogStore
}

func NewImportCandidates(reader CandidateInventoryReader, ledgers CandidateLedgerStore, catalogs CatalogStore) ImportCandidates {
	return ImportCandidates{reader: reader, ledgers: ledgers, catalog: catalogs}
}

func (u ImportCandidates) Run(ctx context.Context, options ImportCandidatesOptions) (ImportCandidatesReport, error) {
	c, err := u.catalog.Load(ctx, options.CatalogPath)
	if err != nil {
		return ImportCandidatesReport{}, fmt.Errorf("load artist catalog: %w", err)
	}
	ledger, err := u.ledgers.LoadLedger(ctx, options.LedgerPath)
	if err != nil {
		return ImportCandidatesReport{}, fmt.Errorf("load candidate ledger: %w", err)
	}
	report := ImportCandidatesReport{Before: CountCandidates(ledger)}

	switch {
	case options.Revert:
		report.Changes = revertStaging(&c, &ledger)
	default:
		if options.InputPath != "" {
			inputs, err := u.reader.ReadCandidates(ctx, options.InputPath)
			if err != nil {
				return ImportCandidatesReport{}, fmt.Errorf("read candidate inventory: %w", err)
			}
			report.Read = len(inputs)
			report.Changes, report.Invalid = mergeCandidates(&ledger, c, inputs)
		}
		report.Changes = append(report.Changes, stageCandidates(&c, &ledger)...)
	}
	catalog.SortCandidates(ledger.Candidates)
	report.After = CountCandidates(ledger)
	report.Ledger = ledger

	if issues := catalog.ValidateCandidateLedger(ledger, c); len(issues) > 0 {
		return report, fmt.Errorf("candidate ledger is invalid: %s", issues[0].Error())
	}
	if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
		return report, fmt.Errorf("staged catalog is invalid: %s", issues[0].Error())
	}
	if options.Apply {
		if err := u.catalog.Save(ctx, options.CatalogPath, c); err != nil {
			return report, fmt.Errorf("save artist catalog: %w", err)
		}
		if err := u.ledgers.SaveLedger(ctx, options.LedgerPath, ledger); err != nil {
			return report, fmt.Errorf("save candidate ledger: %w", err)
		}
		report.Applied = true
	}
	return report, nil
}

// CountCandidates tallies a ledger by status.
func CountCandidates(ledger catalog.CandidateLedger) CandidateCounts {
	counts := CandidateCounts{Total: len(ledger.Candidates)}
	for _, candidate := range ledger.Candidates {
		switch candidate.Status {
		case catalog.CandidateAccepted:
			counts.Accepted++
		case catalog.CandidateRejected:
			counts.Rejected++
		case catalog.CandidateNeedsMoreEvidence:
			counts.NeedsMoreEvidence++
		case catalog.CandidateAliasOfExisting:
			counts.AliasOfExisting++
		default:
			counts.Pending++
		}
	}
	return counts
}

type catalogMatcher struct {
	strict map[string]string
	loose  map[string]string
	ext    map[string]string
}

var (
	musicBrainzIDPattern = regexp.MustCompile(`musicbrainz\.org/artist/([0-9a-f-]{36})`)
	wikidataIDPattern    = regexp.MustCompile(`wikidata\.org/(?:wiki|entity)/(Q[0-9]+)`)
)

func newCatalogMatcher(c catalog.EditorialCatalog) catalogMatcher {
	m := catalogMatcher{strict: map[string]string{}, loose: map[string]string{}, ext: map[string]string{}}
	for _, artist := range c.Artists {
		for _, value := range append([]string{artist.Slug, artist.Name, artist.PublicName}, artist.Aliases...) {
			key := catalog.CandidateKey(value)
			if key == "" {
				continue
			}
			m.strict[key] = artist.Slug
			m.loose[strings.ReplaceAll(key, "-", "")] = artist.Slug
		}
		for _, id := range artist.AllSpotifyIDs() {
			m.ext["spotify:"+id] = artist.Slug
		}
		for _, profile := range artist.SpotifyProfiles {
			m.ext["spotify:"+profile.ID] = artist.Slug
			for _, item := range profile.Evidence {
				if match := musicBrainzIDPattern.FindStringSubmatch(item.URL); match != nil {
					m.ext["musicbrainz:"+match[1]] = artist.Slug
				}
				if match := wikidataIDPattern.FindStringSubmatch(item.URL); match != nil {
					m.ext["wikidata:"+match[1]] = artist.Slug
				}
			}
		}
	}
	return m
}

func (m catalogMatcher) exact(names []string, ids map[string]string) (string, string, bool) {
	for _, name := range names {
		if slug, ok := m.strict[catalog.CandidateKey(name)]; ok && catalog.CandidateKey(name) != "" {
			return slug, "name or alias", true
		}
	}
	for _, kind := range sortedKeys(ids) {
		if slug, ok := m.ext[strings.ToLower(kind)+":"+ids[kind]]; ok {
			return slug, kind + " identifier", true
		}
	}
	return "", "", false
}

func (m catalogMatcher) loosely(names []string) (string, bool) {
	for _, name := range names {
		if slug, ok := m.loose[catalog.CandidateLooseKey(name)]; ok && catalog.CandidateLooseKey(name) != "" {
			return slug, true
		}
	}
	return "", false
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// mergeCandidates folds inventory rows into the ledger. It never changes the
// status or curated fields of a candidate that is already known, so manual
// decisions survive any number of re-imports.
func mergeCandidates(ledger *catalog.CandidateLedger, c catalog.EditorialCatalog, inputs []CandidateInput) ([]CandidateChange, []string) {
	matcher := newCatalogMatcher(c)
	index := map[string]int{}
	indexLedger := func() {
		index = map[string]int{}
		for i, candidate := range ledger.Candidates {
			for _, key := range candidate.Keys() {
				index[key] = i
			}
		}
	}
	indexLedger()

	sorted := append([]CandidateInput(nil), inputs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := catalog.CandidateKey(sorted[i].Name), catalog.CandidateKey(sorted[j].Name)
		if a != b {
			return a < b
		}
		return sorted[i].SourceURL < sorted[j].SourceURL
	})

	var changes []CandidateChange
	var invalid []string
	seenInInput := map[string]struct{}{}
	for _, input := range sorted {
		name := strings.TrimSpace(input.Name)
		key := catalog.CandidateKey(name)
		if key == "" {
			invalid = append(invalid, fmt.Sprintf("%q: name has no usable characters", input.Name))
			continue
		}
		source := catalog.CandidateSource{Title: strings.TrimSpace(input.SourceTitle), URL: strings.TrimSpace(input.SourceURL)}
		names := append([]string{name}, input.Aliases...)

		var hit = -1
		for _, candidateName := range names {
			if i, ok := index[catalog.CandidateKey(candidateName)]; ok && catalog.CandidateKey(candidateName) != "" {
				hit = i
				break
			}
		}
		if hit >= 0 {
			existing := &ledger.Candidates[hit]
			_, repeated := seenInInput[key]
			merged := false
			if source != (catalog.CandidateSource{}) && !hasSource(existing.Sources, source) {
				existing.Sources = append(existing.Sources, source)
				merged = true
			}
			if extra := catalog.MergeCandidateNames(existing.Aliases, input.Aliases, existing.Name); len(extra) != len(existing.Aliases) {
				existing.Aliases = extra
				merged = true
			}
			indexLedger()
			action, detail := ActionKnown, string(existing.Status)
			switch {
			case repeated && !merged:
				action = ActionDuplicateInFile
			case existing.Status == catalog.CandidateRejected:
				action = ActionSuppressed
			case merged:
				action = ActionMergedSource
			}
			seenInInput[key] = struct{}{}
			changes = append(changes, CandidateChange{Name: existing.Name, Key: existing.Key(), Action: action, Status: existing.Status, Details: detail})
			continue
		}
		seenInInput[key] = struct{}{}

		candidate := newCandidate(name, input, source)
		change := CandidateChange{Name: name, Key: key, Action: ActionAdded, Status: candidate.Status}
		if slug, via, ok := matcher.exact(names, input.ExternalIDs); ok {
			candidate.Status = catalog.CandidateAliasOfExisting
			candidate.AliasOf = slug
			candidate.Reason = "Matches existing catalog entry " + slug + " by " + via + "."
			change.Action, change.Status, change.Slug = ActionAutoAlias, candidate.Status, slug
		} else {
			if slug, _, ok := matcher.exact([]string{input.AliasOf}, nil); ok && strings.TrimSpace(input.AliasOf) != "" {
				candidate.AliasOf = slug
				change.Details = "claims to be a variant of " + slug + "; needs review"
			}
			if slug, ok := matcher.loosely(names); ok {
				candidate.PossibleDuplicateOf = slug
				change.Details = strings.TrimSpace(change.Details + " possible duplicate of " + slug)
			}
		}
		ledger.Candidates = append(ledger.Candidates, candidate)
		indexLedger()
		changes = append(changes, change)
	}
	return changes, invalid
}

func newCandidate(name string, input CandidateInput, source catalog.CandidateSource) catalog.Candidate {
	kind := catalog.CandidateKindArtist
	switch strings.ToLower(strings.TrimSpace(input.Kind)) {
	case "group", "grupo", "collective":
		kind = catalog.CandidateKindGroup
	}
	category := catalog.Category(strings.TrimSpace(input.ProposedCategory))
	if !catalog.IsKnownCategory(category) {
		category = catalog.CategoryAffiliateArtist
		if kind == catalog.CandidateKindGroup {
			category = catalog.CategoryAffiliateGroup
		} else {
			for _, function := range input.Functions {
				lower := strings.ToLower(function)
				if strings.Contains(lower, "productor") || strings.Contains(lower, "producer") {
					category = catalog.CategoryProducer
				}
			}
		}
	}
	candidate := catalog.Candidate{
		Name:             name,
		Kind:             kind,
		Status:           catalog.CandidatePending,
		Confidence:       catalog.ParseCandidateConfidence(input.Confidence),
		EvidenceCategory: strings.TrimSpace(input.EvidenceCategory),
		RelationClaim:    strings.TrimSpace(input.RelationClaim),
		Category:         category,
		ProposedSlug:     strings.TrimSpace(input.ProposedSlug),
		Aliases:          catalog.MergeCandidateNames(nil, input.Aliases, name),
		Reason:           strings.TrimSpace(input.Note),
	}
	if len(input.ExternalIDs) > 0 {
		candidate.ExternalIDs = map[string]string{}
		for k, v := range input.ExternalIDs {
			candidate.ExternalIDs[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if source != (catalog.CandidateSource{}) {
		candidate.Sources = []catalog.CandidateSource{source}
	}
	return candidate
}

func hasSource(sources []catalog.CandidateSource, source catalog.CandidateSource) bool {
	for _, existing := range sources {
		if existing == source {
			return true
		}
	}
	return false
}

// stageCandidates writes accepted candidates into the catalog as disabled
// entries without any Spotify ID, and applies alias decisions to existing
// entries. Both are idempotent and recorded on the candidate for reverting.
func stageCandidates(c *catalog.EditorialCatalog, ledger *catalog.CandidateLedger) []CandidateChange {
	var changes []CandidateChange
	order := nextEditorialOrder(c.Artists)
	for i := range ledger.Candidates {
		candidate := &ledger.Candidates[i]
		switch {
		case candidate.Status == catalog.CandidateAccepted && candidate.StagedSlug == "":
			index := catalogIdentityIndex(c.Artists)
			slug := candidate.ProposedSlug
			if slug == "" {
				slug = candidate.Key()
			}
			slug = uniqueDiscoverySlug(slug, index)
			order++
			c.Artists = append(c.Artists, catalog.Artist{
				Slug:           slug,
				Name:           candidate.Name,
				Category:       candidate.Category,
				Roles:          append([]catalog.Category(nil), candidate.Roles...),
				Aliases:        append([]string{}, candidate.Aliases...),
				Enabled:        false,
				EditorialOrder: order,
				Notes:          candidateNote(*candidate),
				ExternalURL:    firstEvidenceURL(candidate.Evidence),
				AddedAt:        reviewDate(candidate.ReviewedAt),
			})
			candidate.StagedSlug = slug
			changes = append(changes, CandidateChange{Name: candidate.Name, Key: candidate.Key(), Action: ActionStaged, Status: candidate.Status, Slug: slug})
		case candidate.Status == catalog.CandidateAliasOfExisting && candidate.AliasOf != "" && !candidate.AliasApplied:
			for j := range c.Artists {
				if c.Artists[j].Slug != candidate.AliasOf {
					continue
				}
				existing := &c.Artists[j]
				known := map[string]struct{}{}
				for _, value := range append([]string{existing.Slug, existing.Name, existing.PublicName}, existing.Aliases...) {
					known[catalog.CandidateKey(value)] = struct{}{}
				}
				added := false
				for _, name := range append([]string{candidate.Name}, candidate.Aliases...) {
					key := catalog.CandidateKey(name)
					if _, ok := known[key]; key == "" || ok {
						continue
					}
					existing.Aliases = append(existing.Aliases, name)
					known[key] = struct{}{}
					added = true
				}
				// Alias already represented: nothing was changed, nothing to revert.
				candidate.AliasApplied = added
				if added {
					changes = append(changes, CandidateChange{Name: candidate.Name, Key: candidate.Key(), Action: ActionAliasApplied, Status: candidate.Status, Slug: existing.Slug})
				}
			}
		}
	}
	return changes
}

func candidateNote(candidate catalog.Candidate) string {
	note := fmt.Sprintf("Curated from source-backed candidate inventory (%s", candidate.Relation)
	if len(candidate.RelationGroups) > 0 {
		note += " of " + strings.Join(candidate.RelationGroups, ", ")
	}
	note += "); Spotify profile not mapped."
	if candidate.Reason != "" {
		note += " " + candidate.Reason
	}
	return note
}

func firstEvidenceURL(evidence []catalog.Evidence) string {
	for _, item := range evidence {
		if item.Independent() && item.URL != "" {
			return item.URL
		}
	}
	return ""
}

func reviewDate(reviewedAt string) string {
	if parsed, err := time.Parse(time.RFC3339, reviewedAt); err == nil {
		return parsed.UTC().Format(time.DateOnly)
	}
	return ""
}

// revertStaging removes what staging added, but only while it is untouched:
// an entry that has since been enabled or given a Spotify profile is kept.
func revertStaging(c *catalog.EditorialCatalog, ledger *catalog.CandidateLedger) []CandidateChange {
	var changes []CandidateChange
	for i := range ledger.Candidates {
		candidate := &ledger.Candidates[i]
		if candidate.StagedSlug != "" {
			position := -1
			for j, artist := range c.Artists {
				if artist.Slug == candidate.StagedSlug {
					position = j
				}
			}
			switch {
			case position < 0:
				candidate.StagedSlug = ""
			case c.Artists[position].Enabled || len(c.Artists[position].AllSpotifyIDs()) > 0 || len(c.Artists[position].SpotifyProfiles) > 0:
				changes = append(changes, CandidateChange{Name: candidate.Name, Key: candidate.Key(), Action: ActionRevertKept, Status: candidate.Status, Slug: candidate.StagedSlug, Details: "entry was edited after staging"})
			default:
				c.Artists = append(c.Artists[:position], c.Artists[position+1:]...)
				changes = append(changes, CandidateChange{Name: candidate.Name, Key: candidate.Key(), Action: ActionReverted, Status: candidate.Status, Slug: candidate.StagedSlug})
				candidate.StagedSlug = ""
			}
		}
		if candidate.AliasApplied {
			for j := range c.Artists {
				if c.Artists[j].Slug != candidate.AliasOf {
					continue
				}
				remove := map[string]struct{}{}
				for _, name := range append([]string{candidate.Name}, candidate.Aliases...) {
					remove[catalog.CandidateKey(name)] = struct{}{}
				}
				kept := c.Artists[j].Aliases[:0:0]
				for _, alias := range c.Artists[j].Aliases {
					if _, ok := remove[catalog.CandidateKey(alias)]; !ok {
						kept = append(kept, alias)
					}
				}
				c.Artists[j].Aliases = kept
			}
			candidate.AliasApplied = false
			changes = append(changes, CandidateChange{Name: candidate.Name, Key: candidate.Key(), Action: ActionReverted, Status: candidate.Status, Slug: candidate.AliasOf})
		}
	}
	return changes
}

// ReviewCandidateOptions describes one manual decision.
type ReviewCandidateOptions struct {
	CatalogPath string
	LedgerPath  string
	Name        string
	// Kind is used only when the candidate is not in the ledger yet.
	Kind   catalog.CandidateKind
	Review catalog.CandidateReview
	DryRun bool
}

type ReviewCandidate struct {
	ledgers CandidateLedgerStore
	catalog CatalogStore
}

func NewReviewCandidate(ledgers CandidateLedgerStore, catalogs CatalogStore) ReviewCandidate {
	return ReviewCandidate{ledgers: ledgers, catalog: catalogs}
}

func (u ReviewCandidate) Run(ctx context.Context, options ReviewCandidateOptions) (catalog.Candidate, error) {
	c, err := u.catalog.Load(ctx, options.CatalogPath)
	if err != nil {
		return catalog.Candidate{}, fmt.Errorf("load artist catalog: %w", err)
	}
	ledger, err := u.ledgers.LoadLedger(ctx, options.LedgerPath)
	if err != nil {
		return catalog.Candidate{}, fmt.Errorf("load candidate ledger: %w", err)
	}
	key := catalog.CandidateKey(options.Name)
	if key == "" {
		return catalog.Candidate{}, fmt.Errorf("candidate name is required")
	}
	position := -1
	for i, candidate := range ledger.Candidates {
		for _, candidateKey := range candidate.Keys() {
			if candidateKey == key {
				position = i
			}
		}
	}
	if position < 0 {
		if !catalog.IsKnownCandidateKind(options.Kind) {
			return catalog.Candidate{}, fmt.Errorf("candidate %q is not in the ledger; pass --kind artist|group to create it", options.Name)
		}
		ledger.Candidates = append(ledger.Candidates, catalog.Candidate{
			Name:       strings.TrimSpace(options.Name),
			Kind:       options.Kind,
			Status:     catalog.CandidatePending,
			Confidence: catalog.ConfidenceLow,
		})
		position = len(ledger.Candidates) - 1
	}
	reviewed, err := catalog.ReviewCandidate(ledger.Candidates[position], options.Review)
	if err != nil {
		return catalog.Candidate{}, err
	}
	ledger.Candidates[position] = reviewed
	catalog.SortCandidates(ledger.Candidates)
	if issues := catalog.ValidateCandidateLedger(ledger, c); len(issues) > 0 {
		return catalog.Candidate{}, fmt.Errorf("candidate ledger would be invalid: %s", issues[0].Error())
	}
	if !options.DryRun {
		if err := u.ledgers.SaveLedger(ctx, options.LedgerPath, ledger); err != nil {
			return catalog.Candidate{}, fmt.Errorf("save candidate ledger: %w", err)
		}
	}
	return reviewed, nil
}

// FormatImportCandidatesMarkdown renders the plan, counts and the decisions
// recorded in the ledger. The output is deterministic.
func FormatImportCandidatesMarkdown(report ImportCandidatesReport) []byte {
	var buf bytes.Buffer
	buf.WriteString("# Wu Family Candidate Curation\n\n")
	writeCountsTable(&buf, "Before", report.Before)
	writeCountsTable(&buf, "After", report.After)

	buf.WriteString("## Plan\n\n")
	if len(report.Changes) == 0 {
		buf.WriteString("No changes.\n\n")
	} else {
		actions := map[string]int{}
		for _, change := range report.Changes {
			actions[change.Action]++
		}
		buf.WriteString("| Action | Count |\n| --- | --- |\n")
		for _, action := range sortedActionKeys(actions) {
			buf.WriteString(fmt.Sprintf("| `%s` | %d |\n", action, actions[action]))
		}
		buf.WriteString("\n")
	}
	if len(report.Invalid) > 0 {
		buf.WriteString("## Invalid Rows\n\n")
		for _, row := range report.Invalid {
			buf.WriteString("- " + escapeMarkdownTable(row) + "\n")
		}
		buf.WriteString("\n")
	}

	for _, status := range []catalog.CandidateStatus{catalog.CandidateAccepted, catalog.CandidateAliasOfExisting, catalog.CandidateNeedsMoreEvidence, catalog.CandidateRejected} {
		writeDecisionSection(&buf, status, report.Ledger)
	}
	return buf.Bytes()
}

func writeCountsTable(buf *bytes.Buffer, title string, counts CandidateCounts) {
	buf.WriteString("## Counts " + title + "\n\n")
	buf.WriteString("| Total | Pending | Accepted | Rejected | Needs more evidence | Alias of existing |\n| --- | --- | --- | --- | --- | --- |\n")
	buf.WriteString(fmt.Sprintf("| %d | %d | %d | %d | %d | %d |\n\n", counts.Total, counts.Pending, counts.Accepted, counts.Rejected, counts.NeedsMoreEvidence, counts.AliasOfExisting))
}

func sortedActionKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeDecisionSection(buf *bytes.Buffer, status catalog.CandidateStatus, ledger catalog.CandidateLedger) {
	var selected []catalog.Candidate
	for _, candidate := range ledger.Candidates {
		if candidate.Status == status {
			selected = append(selected, candidate)
		}
	}
	buf.WriteString("## Decisions: " + string(status) + "\n\n")
	if len(selected) == 0 {
		buf.WriteString("None.\n\n")
		return
	}
	buf.WriteString("| Name | Detail | Evidence |\n| --- | --- | --- |\n")
	for _, candidate := range selected {
		detail := candidate.Reason
		switch status {
		case catalog.CandidateAccepted:
			detail = fmt.Sprintf("%s `%s`, %s", candidate.Relation, candidate.Category, strings.Join(candidate.RelationGroups, ", "))
			if candidate.StagedSlug != "" {
				detail += fmt.Sprintf(" (staged as `%s`)", candidate.StagedSlug)
			}
		case catalog.CandidateAliasOfExisting:
			detail = "alias of `" + candidate.AliasOf + "`"
		}
		links := make([]string, 0, len(candidate.Evidence))
		for _, item := range candidate.Evidence {
			if item.URL != "" {
				links = append(links, fmt.Sprintf("[%s](%s)", item.Type, item.URL))
			}
		}
		buf.WriteString(fmt.Sprintf("| %s | %s | %s |\n", escapeMarkdownTable(candidate.Name), escapeMarkdownTable(detail), strings.Join(links, " ")))
	}
	buf.WriteString("\n")
}
