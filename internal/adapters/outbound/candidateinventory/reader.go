// Package candidateinventory reads external Wu Family candidate inventories
// from JSON or CSV files. It performs no network access.
package candidateinventory

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

type Reader struct{}

func NewReader() Reader { return Reader{} }

// fieldNames maps accepted column or key names (neutral English and the
// Spanish names used by the generated reference files) to canonical fields.
var fieldNames = map[string]string{
	"name": "name", "nombre": "name",
	"kind": "kind", "tipo": "kind",
	"aliases": "aliases", "alias": "aliases",
	"confidence": "confidence", "nivel_verificacion_referencia": "confidence", "nivel_verificacion": "confidence",
	"evidence_category": "evidence_category", "fuentes_ids": "evidence_category",
	"relation_claim": "relation_claim", "relaciones_a_grupos": "relation_claim",
	"functions": "functions", "funciones": "functions",
	"source_title": "source_title", "source_url": "source_url",
	"proposed_slug": "proposed_slug", "proposed_category": "proposed_category",
	"alias_of": "alias_of", "external_ids": "external_ids",
	"note": "note", "criterio": "note",
}

func (Reader) ReadCandidates(ctx context.Context, path string) ([]artists.CandidateInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	defaultTitle := filepath.Base(path)

	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return readCSV(data, defaultTitle)
	case ".json":
		return readJSON(data, defaultTitle)
	default:
		return nil, fmt.Errorf("unsupported inventory format %q: use .json or .csv", filepath.Ext(path))
	}
}

func readCSV(data []byte, defaultTitle string) ([]artists.CandidateInput, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse CSV header: %w", err)
	}
	columns := make([]string, len(header))
	for i, name := range header {
		columns[i] = fieldNames[strings.ToLower(strings.TrimSpace(name))]
	}
	var inputs []artists.CandidateInput
	for line := 2; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			return inputs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parse CSV line %d: %w", line, err)
		}
		row := map[string]string{}
		for i, value := range record {
			if i < len(columns) && columns[i] != "" {
				row[columns[i]] = strings.TrimSpace(value)
			}
		}
		inputs = append(inputs, toInput(row, artists.CandidateInput{SourceTitle: defaultTitle}))
	}
}

// readJSON accepts a neutral document {"source":{...},"candidates":[...]}, a
// bare array of candidates, or the generated audit document with
// grupos_ausentes, artistas_ausentes and variantes_equivalentes.
func readJSON(data []byte, defaultTitle string) ([]artists.CandidateInput, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	defaults := artists.CandidateInput{SourceTitle: defaultTitle}
	var items []any
	var variants map[string]any
	switch doc := raw.(type) {
	case []any:
		items = doc
	case map[string]any:
		if source, ok := doc["source"].(map[string]any); ok {
			defaults.SourceTitle = firstNonEmpty(asString(source["title"]), defaults.SourceTitle)
			defaults.SourceURL = asString(source["url"])
		} else if title := asString(doc["origen_referencia"]); title != "" {
			defaults.SourceTitle = title
		}
		for _, key := range []string{"candidates", "grupos_ausentes", "artistas_ausentes"} {
			if list, ok := doc[key].([]any); ok {
				items = append(items, list...)
			}
		}
		variants, _ = doc["variantes_equivalentes"].(map[string]any)
	default:
		return nil, fmt.Errorf("parse JSON: expected an object or an array")
	}

	var inputs []artists.CandidateInput
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parse JSON: candidate entries must be objects")
		}
		row := map[string]string{}
		var externalIDs map[string]string
		for key, value := range object {
			field, known := fieldNames[strings.ToLower(key)]
			if !known {
				continue
			}
			if field == "external_ids" {
				if ids, ok := value.(map[string]any); ok {
					externalIDs = map[string]string{}
					for k, v := range ids {
						externalIDs[k] = asString(v)
					}
				}
				continue
			}
			row[field] = asString(value)
		}
		inputs = append(inputs, toInput(row, withIDs(defaults, externalIDs)))
	}

	// Equivalent variants are alias claims about names; they stay unverified.
	names := make([]string, 0, len(variants))
	for name := range variants {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		claim := defaults
		claim.Name = name
		claim.AliasOf = asString(variants[name])
		claim.Kind = "group"
		claim.Note = "Variant claim from the source; not verified."
		inputs = append(inputs, claim)
	}
	return inputs, nil
}

func withIDs(input artists.CandidateInput, ids map[string]string) artists.CandidateInput {
	input.ExternalIDs = ids
	return input
}

func toInput(row map[string]string, base artists.CandidateInput) artists.CandidateInput {
	input := base
	input.Name = row["name"]
	input.Kind = row["kind"]
	input.Aliases = splitList(row["aliases"])
	input.Confidence = row["confidence"]
	input.EvidenceCategory = row["evidence_category"]
	input.RelationClaim = row["relation_claim"]
	input.Functions = splitList(row["functions"])
	input.SourceTitle = firstNonEmpty(row["source_title"], base.SourceTitle)
	input.SourceURL = firstNonEmpty(row["source_url"], base.SourceURL)
	input.ProposedSlug = row["proposed_slug"]
	input.ProposedCategory = row["proposed_category"]
	input.AliasOf = row["alias_of"]
	input.Note = row["note"]
	if raw := row["external_ids"]; raw != "" {
		input.ExternalIDs = map[string]string{}
		for _, pair := range splitList(raw) {
			if key, value, ok := strings.Cut(pair, "="); ok {
				input.ExternalIDs[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
	}
	return input
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == '|' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func asString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, asString(item))
		}
		return strings.Join(parts, ";")
	default:
		return fmt.Sprint(v)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
