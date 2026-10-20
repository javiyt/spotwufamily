package catalogexport

import (
	"sort"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// RelationshipsFromCatalog selects the relationships that may be published:
// verified edges between artists present in the catalog, in canonical order.
// Proposed and rejected claims never leave the editorial file.
func RelationshipsFromCatalog(c catalog.EditorialCatalog) []Relationship {
	known := make(map[string]struct{}, len(c.Artists))
	for _, artist := range c.Artists {
		known[artist.Slug] = struct{}{}
	}

	out := make([]Relationship, 0, len(c.Relationships))
	for _, rel := range c.Relationships {
		if !rel.Established() {
			continue
		}
		rel = rel.Normalized()
		if _, ok := known[rel.From]; !ok {
			continue
		}
		if _, ok := known[rel.To]; !ok {
			continue
		}
		exported := Relationship{
			Type: string(rel.Type), From: rel.From, To: rel.To, Start: rel.Start, End: rel.End, Era: rel.Era,
			Former: rel.Former(), Confidence: string(rel.Confidence), Commentary: rel.Commentary,
		}
		for _, evidence := range rel.Evidence {
			exported.Evidence = append(exported.Evidence, RelationshipEvidence{Type: string(evidence.Type), URL: evidence.URL, Rationale: evidence.Rationale})
		}
		out = append(out, exported)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	return out
}
