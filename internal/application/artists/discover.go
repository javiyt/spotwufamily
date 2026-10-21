package artists

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type DiscoverySource interface {
	DiscoverWuFamilyArtists(context.Context) ([]DiscoveredArtist, error)
}

type DiscoveredArtist struct {
	Name       string
	Category   catalog.Category
	Source     string
	SourceURL  string
	SourceNote string
}

type DiscoverWuFamily struct {
	store   CatalogStore
	source  DiscoverySource
	ledgers CandidateLedgerStore
}

func NewDiscoverWuFamily(store CatalogStore, source DiscoverySource) DiscoverWuFamily {
	return DiscoverWuFamily{store: store, source: source}
}

// WithLedger makes discovery read the candidate ledger so names that were
// already imported or decided are reported as suppressed instead of new. The
// ledger is only read; discovery never changes a manual decision.
func (d DiscoverWuFamily) WithLedger(ledgers CandidateLedgerStore) DiscoverWuFamily {
	d.ledgers = ledgers
	return d
}

type DiscoverWuFamilyOptions struct {
	CatalogPath string
	LedgerPath  string
	Apply       bool
	// OnlySlugs, when non-nil, restricts Apply to the new candidates with
	// these slugs (the set a user reviewed); others are reported, not added.
	OnlySlugs []string
	Progress  func(DiscoverWuFamilyProgress)
}

type DiscoverWuFamilyReport struct {
	Found    int
	Existing []DiscoveryCandidate
	New      []DiscoveryCandidate
	Added    []DiscoveryCandidate
	// Suppressed are discovered names already present in the candidate ledger.
	Suppressed []DiscoveryCandidate
}

type DiscoveryCandidate struct {
	Name      string
	Slug      string
	Category  catalog.Category
	Aliases   []string
	Source    string
	SourceURL string
	Reason    string
	// LedgerStatus is set on suppressed candidates.
	LedgerStatus catalog.CandidateStatus
}

type DiscoverWuFamilyProgress struct {
	Stage    string
	Found    int
	Existing int
	New      int
	Added    int
}

func (d DiscoverWuFamily) Run(ctx context.Context, options DiscoverWuFamilyOptions) (DiscoverWuFamilyReport, error) {
	c, err := d.store.Load(ctx, options.CatalogPath)
	if err != nil {
		return DiscoverWuFamilyReport{}, fmt.Errorf("load artist catalog: %w", err)
	}

	emitDiscoverWuFamilyProgress(options.Progress, DiscoverWuFamilyProgress{Stage: "fetching"})
	discovered, err := d.source.DiscoverWuFamilyArtists(ctx)
	if err != nil {
		return DiscoverWuFamilyReport{}, fmt.Errorf("discover Wu Family artists: %w", err)
	}
	emitDiscoverWuFamilyProgress(options.Progress, DiscoverWuFamilyProgress{Stage: "fetched", Found: len(discovered)})

	var ledger catalog.CandidateLedger
	if d.ledgers != nil && options.LedgerPath != "" {
		ledger, err = d.ledgers.LoadLedger(ctx, options.LedgerPath)
		if err != nil {
			return DiscoverWuFamilyReport{}, fmt.Errorf("load candidate ledger: %w", err)
		}
	}
	report := buildDiscoverWuFamilyReport(c, discovered, ledger)
	emitDiscoverWuFamilyProgress(options.Progress, DiscoverWuFamilyProgress{Stage: "compared", Found: report.Found, Existing: len(report.Existing), New: len(report.New)})
	toAdd := report.New
	if options.OnlySlugs != nil {
		toAdd = allowedCandidates(report.New, options.OnlySlugs)
	}
	if options.Apply && len(toAdd) > 0 {
		nextOrder := nextEditorialOrder(c.Artists)
		for _, candidate := range toAdd {
			nextOrder++
			c.Artists = append(c.Artists, catalog.Artist{
				Slug:           candidate.Slug,
				Name:           candidate.Name,
				SpotifyID:      "",
				Category:       candidate.Category,
				Roles:          []catalog.Category{candidate.Category},
				Aliases:        candidate.Aliases,
				Enabled:        false,
				EditorialOrder: nextOrder,
				Notes:          discoveryNote(candidate),
				ExternalURL:    candidate.SourceURL,
			})
			report.Added = append(report.Added, candidate)
		}
		if issues := catalog.ValidateEditorialCatalog(c); len(issues) > 0 {
			return report, fmt.Errorf("discovered catalog is invalid: %s", issues[0].Error())
		}
		if err := d.store.Save(ctx, options.CatalogPath, c); err != nil {
			return report, fmt.Errorf("save artist catalog: %w", err)
		}
		emitDiscoverWuFamilyProgress(options.Progress, DiscoverWuFamilyProgress{Stage: "saved", Found: report.Found, Existing: len(report.Existing), New: len(report.New), Added: len(report.Added)})
	}

	return report, nil
}

// allowedCandidates keeps the candidates whose slug is in only.
func allowedCandidates(candidates []DiscoveryCandidate, only []string) []DiscoveryCandidate {
	allowed := make(map[string]bool, len(only))
	for _, slug := range only {
		allowed[slug] = true
	}
	var out []DiscoveryCandidate
	for _, c := range candidates {
		if allowed[c.Slug] {
			out = append(out, c)
		}
	}
	return out
}

func emitDiscoverWuFamilyProgress(progress func(DiscoverWuFamilyProgress), event DiscoverWuFamilyProgress) {
	if progress != nil {
		progress(event)
	}
}

func buildDiscoverWuFamilyReport(c catalog.EditorialCatalog, discovered []DiscoveredArtist, ledger catalog.CandidateLedger) DiscoverWuFamilyReport {
	existing := catalogIdentityIndex(c.Artists)
	decided := ledgerIdentityIndex(ledger)
	seenDiscovered := map[string]struct{}{}
	report := DiscoverWuFamilyReport{Found: len(discovered)}

	for _, item := range discovered {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		primaryName, aliases := discoveryNames(name)
		key := catalog.Slugify(primaryName)
		if key == "" {
			continue
		}
		if _, ok := seenDiscovered[key]; ok {
			continue
		}
		seenDiscovered[key] = struct{}{}

		category := item.Category
		if !catalog.IsKnownCategory(category) {
			category = catalog.CategoryCollaborator
		}
		candidate := DiscoveryCandidate{
			Name:      primaryName,
			Slug:      uniqueDiscoverySlug(key, existing),
			Category:  category,
			Aliases:   aliases,
			Source:    item.Source,
			SourceURL: item.SourceURL,
			Reason:    item.SourceNote,
		}
		if existingKey, ok := firstExistingDiscoveryKey(existing, append([]string{primaryName}, aliases...)); ok {
			candidate.Slug = existingKey
			report.Existing = append(report.Existing, candidate)
			continue
		}
		if status, ok := firstLedgerStatus(decided, append([]string{primaryName}, aliases...)); ok {
			candidate.LedgerStatus = status
			report.Suppressed = append(report.Suppressed, candidate)
			continue
		}
		report.New = append(report.New, candidate)
		existing[candidate.Slug] = candidate.Slug
		for _, alias := range candidate.Aliases {
			if aliasKey := catalog.Slugify(alias); aliasKey != "" {
				existing[aliasKey] = candidate.Slug
			}
		}
	}

	sortDiscoveryCandidates(report.Existing)
	sortDiscoveryCandidates(report.New)
	sortDiscoveryCandidates(report.Suppressed)
	return report
}

func ledgerIdentityIndex(ledger catalog.CandidateLedger) map[string]catalog.CandidateStatus {
	index := map[string]catalog.CandidateStatus{}
	for _, candidate := range ledger.Candidates {
		for _, key := range candidate.Keys() {
			index[key] = candidate.Status
		}
	}
	return index
}

func firstLedgerStatus(index map[string]catalog.CandidateStatus, names []string) (catalog.CandidateStatus, bool) {
	for _, name := range names {
		if status, ok := index[catalog.CandidateKey(name)]; ok {
			return status, true
		}
	}
	return "", false
}

func discoveryNames(name string) (string, []string) {
	parts := splitDiscoveryNames(name)
	primary := strings.TrimSpace(parts[0])
	aliases := make([]string, 0, len(parts)-1)
	seen := map[string]struct{}{catalog.Slugify(primary): struct{}{}}
	for _, part := range parts[1:] {
		alias := strings.TrimSpace(part)
		key := catalog.Slugify(alias)
		if alias == "" || key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		aliases = append(aliases, alias)
	}
	return primary, aliases
}

func splitDiscoveryNames(name string) []string {
	parts := strings.Split(name, "/")
	if len(parts) == 1 {
		return []string{name}
	}
	return parts
}

func firstExistingDiscoveryKey(existing map[string]string, names []string) (string, bool) {
	for _, name := range names {
		key := catalog.Slugify(name)
		if key == "" {
			continue
		}
		if slug, ok := existing[key]; ok {
			return slug, true
		}
	}
	return "", false
}

func catalogIdentityIndex(artists []catalog.Artist) map[string]string {
	index := map[string]string{}
	for _, artist := range artists {
		for _, value := range append([]string{artist.Slug, artist.Name, artist.PublicName}, artist.Aliases...) {
			key := catalog.Slugify(value)
			if key != "" {
				index[key] = artist.Slug
			}
		}
	}
	return index
}

func uniqueDiscoverySlug(slug string, existing map[string]string) string {
	if slug == "" {
		slug = "artist"
	}
	if _, ok := existing[slug]; !ok {
		return slug
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", slug, suffix)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

func nextEditorialOrder(artists []catalog.Artist) int {
	next := 0
	for _, artist := range artists {
		if artist.EditorialOrder > next {
			next = artist.EditorialOrder
		}
	}
	return next
}

func discoveryNote(candidate DiscoveryCandidate) string {
	note := "Discovered from " + candidate.Source + "; review before enabling."
	if candidate.Reason != "" {
		note = "Discovered from " + candidate.Source + " (" + candidate.Reason + "); review before enabling."
	}
	return note
}

func sortDiscoveryCandidates(candidates []DiscoveryCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Category != candidates[j].Category {
			return candidates[i].Category < candidates[j].Category
		}
		return strings.ToLower(candidates[i].Name) < strings.ToLower(candidates[j].Name)
	})
}

func FormatDiscoverWuFamilyMarkdown(report DiscoverWuFamilyReport) []byte {
	var buf bytes.Buffer
	buf.WriteString("# Wu Family Discovery\n\n")
	buf.WriteString(fmt.Sprintf("Summary: found=%d existing=%d new=%d added=%d\n\n", report.Found, len(report.Existing), len(report.New), len(report.Added)))
	writeDiscoverySection(&buf, "New Candidates", report.New)
	writeDiscoverySection(&buf, "Already In Catalog", report.Existing)
	if len(report.Added) > 0 {
		writeDiscoverySection(&buf, "Added To Catalog", report.Added)
	}
	if len(report.Suppressed) > 0 {
		writeDiscoverySection(&buf, "Suppressed By Candidate Ledger", report.Suppressed)
	}
	return buf.Bytes()
}

func writeDiscoverySection(buf *bytes.Buffer, title string, candidates []DiscoveryCandidate) {
	buf.WriteString("## " + title + "\n\n")
	if len(candidates) == 0 {
		buf.WriteString("None.\n\n")
		return
	}
	buf.WriteString("| Name | Slug | Category | Source |\n")
	buf.WriteString("| --- | --- | --- | --- |\n")
	for _, candidate := range candidates {
		source := candidate.Source
		if candidate.SourceURL != "" {
			source = fmt.Sprintf("[%s](%s)", candidate.Source, candidate.SourceURL)
		}
		buf.WriteString(fmt.Sprintf("| %s | `%s` | `%s` | %s |\n", escapeMarkdownTable(candidate.Name), candidate.Slug, candidate.Category, source))
	}
	buf.WriteString("\n")
}

func escapeMarkdownTable(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
