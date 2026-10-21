package maintenance

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// DiscoverPort runs Wu Family discovery against Wikipedia. With apply it adds
// new candidates to the catalog through the YAML store (which keeps a .bak);
// otherwise it only reports. A non-nil only restricts apply to the new
// candidates with those slugs.
type DiscoverPort interface {
	Run(ctx context.Context, apply bool, only []string, progress func(artists.DiscoverWuFamilyProgress)) (artists.DiscoverWuFamilyReport, error)
}

// DiscoverDeps are the ports the discovery actions need.
type DiscoverDeps struct {
	Runner DiscoverPort
	Digest CatalogDigest
}

var discoverColumns = []string{"status", "slug", "name", "category", "reason"}

// discoverAction implements discover-preview (dry run) and discover-import.
type discoverAction struct {
	deps  DiscoverDeps
	apply bool
}

// NewDiscoverPreview returns the discover-preview action: a dry run that
// reports new, existing and suppressed Wu Family candidates.
func NewDiscoverPreview(d DiscoverDeps) Action { return &discoverAction{deps: d} }

// NewDiscoverImport returns the discover-import action: it adds the new
// candidates to the catalog as disabled artists.
func NewDiscoverImport(d DiscoverDeps) Action { return &discoverAction{deps: d, apply: true} }

func (a *discoverAction) Name() string {
	if a.apply {
		return "discover-import"
	}
	return "discover-preview"
}

func (a *discoverAction) Describe() Description {
	title := "Discover Wu Family artists (preview)"
	if a.apply {
		title = "Import discovered Wu Family artists"
	}
	return Description{Title: title, UsesNetwork: true}
}

func (a *discoverAction) Available() (bool, string) { return true, "" }

func (a *discoverAction) Validate(p Params) error { return RejectUnknown(p) }

// Preview of the dry run only digests the catalog. The import preview runs a
// dry run so the token is bound to the exact candidate set being imported.
func (a *discoverAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return Preview{}, err
	}
	pv := Preview{
		Title:       a.Describe().Title,
		UsesNetwork: true,
		Scope:       []ScopeLine{{Label: "Source", Value: "Wikipedia: Wu-Tang Clan affiliates"}},
	}
	if !a.apply {
		pv.Scope = append(pv.Scope, ScopeLine{Label: "Writes", Value: "none (dry run)"})
		pv.Fingerprint = fingerprint(a.Name(), digest)
		return pv, nil
	}

	slugs, err := a.newSlugs(ctx)
	if err != nil {
		return Preview{}, err
	}
	pv.Scope = append(pv.Scope,
		ScopeLine{Label: "New candidates", Value: strconv.Itoa(len(slugs))},
		ScopeLine{Label: "Writes", Value: "catalog (new artists added disabled; previous file kept as .bak)"},
	)
	if len(slugs) > 0 {
		pv.Scope = append(pv.Scope, ScopeLine{Label: "Artists", Value: joinLimited(slugs, 20)})
	} else {
		pv.Warnings = append(pv.Warnings, "No new candidates were found; nothing will be imported.")
	}
	pv.Fingerprint = a.importFingerprint(digest, slugs)
	return pv, nil
}

// newSlugs runs discovery without writing and returns the sorted slugs of
// the new candidates (never nil).
func (a *discoverAction) newSlugs(ctx context.Context) ([]string, error) {
	report, err := a.deps.Runner.Run(ctx, false, nil, nil)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(report.New))
	for _, c := range report.New {
		slugs = append(slugs, c.Slug)
	}
	slices.Sort(slugs)
	return slugs, nil
}

func (a *discoverAction) importFingerprint(digest string, slugs []string) string {
	return fingerprint(append([]string{a.Name(), digest}, slugs...)...)
}

// Spec binds the import to the fingerprint the user confirmed (failing
// closed without one); the dry run needs no confirmation.
func (a *discoverAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	var verified string
	if a.apply {
		fp, err := verifiedFingerprint(ctx)
		if err != nil {
			return jobs.Spec{}, err
		}
		verified = fp
	}
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: []string{ResourceCatalogYAML},
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, verified)
		},
	}, nil
}

// checkPreview recomputes the import fingerprint while the job holds the
// catalog and returns the previewed slugs, or ErrStalePreview when the
// catalog, ledger or candidate set changed since the user confirmed.
func (a *discoverAction) checkPreview(ctx context.Context, verified string) ([]string, error) {
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return nil, err
	}
	slugs, err := a.newSlugs(ctx)
	if err != nil {
		return nil, err
	}
	if a.importFingerprint(digest, slugs) != verified {
		return nil, ErrStalePreview
	}
	return slugs, nil
}

func (a *discoverAction) run(ctx context.Context, r jobs.Reporter, verified string) (jobs.Outcome, error) {
	var only []string
	if a.apply {
		r.Step("checking preview")
		slugs, err := a.checkPreview(ctx, verified)
		if err != nil {
			if ctx.Err() != nil {
				return jobs.Outcome{}, ctx.Err()
			}
			return jobs.Outcome{}, Safe(err)
		}
		// Only the previewed candidates are imported, even if the source
		// changes between this check and the import.
		only = slugs
	}
	report, err := a.deps.Runner.Run(ctx, a.apply, only, func(e artists.DiscoverWuFamilyProgress) {
		r.Step(e.Stage)
	})
	if err != nil {
		if ctx.Err() != nil {
			return jobs.Outcome{}, ctx.Err()
		}
		return jobs.Outcome{}, Safe(err)
	}

	r.Count("found", report.Found)
	r.Count("existing", len(report.Existing))
	r.Count("new", len(report.New))
	r.Count("added", len(report.Added))
	r.Count("suppressed", len(report.Suppressed))

	rep := &jobs.Report{Name: a.Name(), Columns: discoverColumns}
	add := func(status string, cs []artists.DiscoveryCandidate) {
		for _, c := range cs {
			reason := c.Reason
			if status == "suppressed" && c.LedgerStatus != "" {
				reason = "ledger: " + string(c.LedgerStatus)
			}
			rep.Rows = append(rep.Rows, []string{status, c.Slug, c.Name, string(c.Category), reason})
		}
	}
	add("existing", report.Existing)
	add("suppressed", report.Suppressed)
	if a.apply {
		add("added", report.Added)
	} else {
		add("new", report.New)
	}
	SortRows(rep)

	summary := fmt.Sprintf("Found %d candidates: %d new, %d existing, %d suppressed.",
		report.Found, len(report.New), len(report.Existing), len(report.Suppressed))
	if a.apply {
		summary = strings.TrimSuffix(summary, ".") + fmt.Sprintf("; %d added to the catalog.", len(report.Added))
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary, Report: rep}}, nil
}
