package artists

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// FormatReconcileJSON renders the impact report as deterministic indented JSON.
func FormatReconcileJSON(report ReconcileReport) ([]byte, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode reconcile report: %w", err)
	}

	return append(data, '\n'), nil
}

// FormatReconcileMarkdown renders the impact report as a deterministic Markdown
// diff suitable for a pull request description. It never includes timestamps.
func FormatReconcileMarkdown(report ReconcileReport) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Reconciliation impact: %s\n\n", report.Slug)
	fmt.Fprintf(&b, "- Status: `%s`\n", report.Status)
	fmt.Fprintf(&b, "- Plan checksum: `%s`\n", report.PlanChecksum)
	fmt.Fprintf(&b, "- Baseline: `%s`\n\n", report.Identity.BaselineSource)

	b.WriteString("## Identity mapping\n\n```diff\n")
	for _, id := range report.Identity.Removed {
		fmt.Fprintf(&b, "- %s\n", id)
	}
	for _, id := range report.Identity.Kept {
		fmt.Fprintf(&b, "  %s\n", id)
	}
	for _, id := range report.Identity.Added {
		fmt.Fprintf(&b, "+ %s\n", id)
	}
	b.WriteString("```\n\n")

	b.WriteString("## Associations\n\n")
	b.WriteString("| | Albums | Tracks |\n|---|---:|---:|\n")
	row := func(label string, c ReconcileCounts) {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", label, c.Albums, c.Tracks)
	}
	row("Active before", report.Before)
	row("Supported", report.Supported)
	row("Orphan (unlinked)", report.Orphan)
	row("Orphan shared with other artists", report.SharedOrphans)
	row("Orphan unreferenced afterwards", report.UnreferencedAfter)
	row("Unverifiable (kept)", report.Unverifiable)
	row("Active after", report.After)
	b.WriteString("\n")

	if len(report.CreditedArtistIDs) > 0 {
		fmt.Fprintf(&b, "Credited artists on orphan associations: %s\n\n", strings.Join(report.CreditedArtistIDs, ", "))
	}

	b.WriteString("## Sync state\n\n")
	if len(report.StaleCheckpoints) == 0 {
		b.WriteString("- No stale sync checkpoints.\n")
	}
	for _, cp := range report.StaleCheckpoints {
		fmt.Fprintf(&b, "- Stale checkpoint of run %d (`%s`)\n", cp.RunID, cp.SpotifyIDs)
	}
	fmt.Fprintf(&b, "- Metadata cache stale: %t\n", report.MetadataStale)
	fmt.Fprintf(&b, "- Resync of replacement IDs required: %t\n\n", report.ResyncRequired)

	b.WriteString("## Decisions\n\n")
	if len(report.Decisions) == 0 {
		b.WriteString("None.\n\n")
	}
	for _, d := range report.Decisions {
		fmt.Fprintf(&b, "- `%s` %s: %s\n", d.Action, d.Target, d.Reason)
	}
	if len(report.Decisions) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("## Affected entities\n\n")
	for _, item := range report.Items {
		if item.Class == ReconcileSupported {
			continue
		}
		shared := ""
		if len(item.SharedWith) > 0 {
			shared = ", shared with " + strings.Join(item.SharedWith, ",")
		}
		fmt.Fprintf(&b, "- %s %s `%s` %q (%s%s)\n", item.Class, item.Kind, item.SpotifyID, item.Name, strings.Join(item.CreditedArtistIDs, ","), shared)
	}

	return b.Bytes()
}

// FormatPendingJSON renders the pending reconciliations as deterministic JSON.
func FormatPendingJSON(status PendingStatus) ([]byte, error) {
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode pending reconciliations: %w", err)
	}

	return append(data, '\n'), nil
}
