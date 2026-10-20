package sqlite

import (
	"context"
	"fmt"

	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// SaveArtistRelationships replaces the projected relationships with the
// verified ones from the editorial catalog. Proposed and rejected claims are
// deliberately not projected, so nothing unreviewed can reach an export.
// Artists must already be saved with SaveConfiguredArtists.
func (d *Database) SaveArtistRelationships(ctx context.Context, relationships []catalog.Relationship) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relationships transaction: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	if _, err := tx.ExecContext(ctx, `DELETE FROM artist_relationships`); err != nil {
		return fmt.Errorf("clear artist relationships: %w", err)
	}
	for _, rel := range relationships {
		if !rel.Established() {
			continue
		}
		rel = rel.Normalized()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO artist_relationships(relationship_type, from_slug, to_slug, start_date, end_date, era, confidence, commentary, reviewer, reviewed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rel.Type, rel.From, rel.To, rel.Start, rel.End, rel.Era, rel.Confidence, rel.Commentary, rel.Reviewer, rel.ReviewedAt,
		); err != nil {
			return fmt.Errorf("insert relationship %s: %w", rel.Key(), err)
		}
		for position, evidence := range rel.Evidence {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO artist_relationship_evidence(relationship_type, from_slug, to_slug, position, evidence_type, url, rationale)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
				rel.Type, rel.From, rel.To, position, evidence.Type, evidence.URL, evidence.Rationale,
			); err != nil {
				return fmt.Errorf("insert evidence for %s: %w", rel.Key(), err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit relationships transaction: %w", err)
	}
	return nil
}

func (d *Database) exportRelationships(ctx context.Context) ([]catalogexport.Relationship, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT relationship_type, from_slug, to_slug, start_date, end_date, era, confidence, commentary
FROM artist_relationships
ORDER BY relationship_type, from_slug, to_slug`)
	if err != nil {
		return nil, fmt.Errorf("query relationships: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]catalogexport.Relationship, 0)
	for rows.Next() {
		var rel catalogexport.Relationship
		if err := rows.Scan(&rel.Type, &rel.From, &rel.To, &rel.Start, &rel.End, &rel.Era, &rel.Confidence, &rel.Commentary); err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		rel.Former = rel.Type == string(catalog.RelMemberOf) && rel.End != ""
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read relationships: %w", err)
	}

	evidence, err := d.db.QueryContext(ctx, `
SELECT relationship_type, from_slug, to_slug, evidence_type, url, rationale
FROM artist_relationship_evidence
ORDER BY relationship_type, from_slug, to_slug, position`)
	if err != nil {
		return nil, fmt.Errorf("query relationship evidence: %w", err)
	}
	defer func() { _ = evidence.Close() }()

	index := make(map[[3]string]int, len(out))
	for i, rel := range out {
		index[[3]string{rel.Type, rel.From, rel.To}] = i
	}
	for evidence.Next() {
		var t, from, to string
		var item catalogexport.RelationshipEvidence
		if err := evidence.Scan(&t, &from, &to, &item.Type, &item.URL, &item.Rationale); err != nil {
			return nil, fmt.Errorf("scan relationship evidence: %w", err)
		}
		if i, ok := index[[3]string{t, from, to}]; ok {
			out[i].Evidence = append(out[i].Evidence, item)
		}
	}
	return out, evidence.Err()
}
