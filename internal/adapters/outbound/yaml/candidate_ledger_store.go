package yamlcatalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"gopkg.in/yaml.v3"
)

// LedgerStore persists the candidate ledger as YAML. Output is deterministic
// (candidates are sorted by the caller, map keys by the encoder).
type LedgerStore struct{}

type ledgerDocument struct {
	Version    int               `yaml:"version"`
	Candidates []candidateRecord `yaml:"candidates"`
}

type candidateRecord struct {
	Name                string                      `yaml:"name"`
	Kind                catalog.CandidateKind       `yaml:"kind"`
	Status              catalog.CandidateStatus     `yaml:"status"`
	Confidence          catalog.CandidateConfidence `yaml:"confidence,omitempty"`
	EvidenceCategory    string                      `yaml:"evidence_category,omitempty"`
	RelationClaim       string                      `yaml:"relation_claim,omitempty"`
	Relation            catalog.WuRelation          `yaml:"relation,omitempty"`
	RelationGroups      []string                    `yaml:"relation_groups,omitempty"`
	Category            catalog.Category            `yaml:"category,omitempty"`
	Roles               []catalog.Category          `yaml:"roles,omitempty"`
	ProposedSlug        string                      `yaml:"proposed_slug,omitempty"`
	AliasOf             string                      `yaml:"alias_of,omitempty"`
	Aliases             []string                    `yaml:"aliases,omitempty"`
	ExternalIDs         map[string]string           `yaml:"external_ids,omitempty"`
	Sources             []sourceRecord              `yaml:"sources,omitempty"`
	Evidence            []evidenceRecord            `yaml:"evidence,omitempty"`
	Reason              string                      `yaml:"reason,omitempty"`
	Reviewer            string                      `yaml:"reviewer,omitempty"`
	ReviewedAt          string                      `yaml:"reviewed_at,omitempty"`
	PossibleDuplicateOf string                      `yaml:"possible_duplicate_of,omitempty"`
	StagedSlug          string                      `yaml:"staged_slug,omitempty"`
	AliasApplied        bool                        `yaml:"alias_applied,omitempty"`
}

type sourceRecord struct {
	Title string `yaml:"title,omitempty"`
	URL   string `yaml:"url,omitempty"`
}

func NewLedgerStore() LedgerStore { return LedgerStore{} }

// LoadLedger reads the ledger; a missing file is an empty ledger.
func (LedgerStore) LoadLedger(ctx context.Context, path string) (catalog.CandidateLedger, error) {
	if err := ctx.Err(); err != nil {
		return catalog.CandidateLedger{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return catalog.CandidateLedger{Version: 1}, nil
	}
	if err != nil {
		return catalog.CandidateLedger{}, fmt.Errorf("read %s: %w", path, err)
	}
	var doc ledgerDocument
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		return catalog.CandidateLedger{}, fmt.Errorf("parse %s: %w", path, err)
	}
	ledger := catalog.CandidateLedger{Version: doc.Version, Candidates: make([]catalog.Candidate, 0, len(doc.Candidates))}
	for _, record := range doc.Candidates {
		ledger.Candidates = append(ledger.Candidates, record.toDomain())
	}
	return ledger, nil
}

// SaveLedger writes atomically and keeps the previous file as path+".bak".
func (LedgerStore) SaveLedger(ctx context.Context, path string, ledger catalog.CandidateLedger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ledger.Version == 0 {
		ledger.Version = 1
	}
	doc := ledgerDocument{Version: ledger.Version, Candidates: make([]candidateRecord, 0, len(ledger.Candidates))}
	for _, candidate := range ledger.Candidates {
		doc.Candidates = append(doc.Candidates, candidateFromDomain(candidate))
	}

	var buf bytes.Buffer
	buf.WriteString("# Wu Family candidate ledger (see docs/catalog-policy.md).\n")
	buf.WriteString("# Imports only add `pending` candidates and provenance. Every other status is a manual decision.\n")
	buf.WriteString("# Only `accepted` candidates are staged into data/artists.yaml, always disabled and without a Spotify ID.\n\n")
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("encode candidate ledger: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close YAML encoder: %w", err)
	}

	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, buf.Bytes()) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	if err == nil {
		if err := writeBackup(path+".bak", current); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".candidates-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary ledger file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temporary ledger file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temporary ledger file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func (r candidateRecord) toDomain() catalog.Candidate {
	candidate := catalog.Candidate{
		Name: r.Name, Kind: r.Kind, Status: r.Status, Confidence: r.Confidence,
		EvidenceCategory: r.EvidenceCategory, RelationClaim: r.RelationClaim,
		Relation: r.Relation, RelationGroups: r.RelationGroups, Category: r.Category, Roles: r.Roles,
		ProposedSlug: r.ProposedSlug, AliasOf: r.AliasOf, Aliases: r.Aliases, ExternalIDs: r.ExternalIDs,
		Reason: r.Reason, Reviewer: r.Reviewer, ReviewedAt: r.ReviewedAt,
		PossibleDuplicateOf: r.PossibleDuplicateOf, StagedSlug: r.StagedSlug, AliasApplied: r.AliasApplied,
	}
	for _, source := range r.Sources {
		candidate.Sources = append(candidate.Sources, catalog.CandidateSource{Title: source.Title, URL: source.URL})
	}
	for _, item := range r.Evidence {
		candidate.Evidence = append(candidate.Evidence, catalog.Evidence{Type: item.Type, URL: item.URL, ReleaseID: item.ReleaseID, TrackID: item.TrackID, Rationale: item.Rationale})
	}
	return candidate
}

func candidateFromDomain(c catalog.Candidate) candidateRecord {
	record := candidateRecord{
		Name: c.Name, Kind: c.Kind, Status: c.Status, Confidence: c.Confidence,
		EvidenceCategory: c.EvidenceCategory, RelationClaim: c.RelationClaim,
		Relation: c.Relation, RelationGroups: c.RelationGroups, Category: c.Category, Roles: c.Roles,
		ProposedSlug: c.ProposedSlug, AliasOf: c.AliasOf, Aliases: c.Aliases, ExternalIDs: c.ExternalIDs,
		Reason: c.Reason, Reviewer: c.Reviewer, ReviewedAt: c.ReviewedAt,
		PossibleDuplicateOf: c.PossibleDuplicateOf, StagedSlug: c.StagedSlug, AliasApplied: c.AliasApplied,
	}
	for _, source := range c.Sources {
		record.Sources = append(record.Sources, sourceRecord{Title: source.Title, URL: source.URL})
	}
	for _, item := range c.Evidence {
		record.Evidence = append(record.Evidence, evidenceRecord{Type: item.Type, URL: item.URL, ReleaseID: item.ReleaseID, TrackID: item.TrackID, Rationale: item.Rationale})
	}
	return record
}
