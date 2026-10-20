package yamlcatalog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"gopkg.in/yaml.v3"
)

type Store struct{}

type document struct {
	Version           int                       `yaml:"version"`
	ProfileSyncPolicy catalog.ProfileSyncPolicy `yaml:"profile_sync_policy,omitempty"`
	Artists           []artistRecord            `yaml:"artists"`
	Relationships     []relationshipRecord      `yaml:"relationships,omitempty"`
}

type relationshipRecord struct {
	Type       catalog.RelationshipType    `yaml:"type"`
	From       string                      `yaml:"from"`
	To         string                      `yaml:"to"`
	Start      string                      `yaml:"start,omitempty"`
	End        string                      `yaml:"end,omitempty"`
	Era        string                      `yaml:"era,omitempty"`
	Status     catalog.RelationshipStatus  `yaml:"status"`
	Confidence catalog.CandidateConfidence `yaml:"confidence,omitempty"`
	Evidence   []evidenceRecord            `yaml:"evidence,omitempty"`
	Commentary string                      `yaml:"commentary,omitempty"`
	Reviewer   string                      `yaml:"reviewer,omitempty"`
	ReviewedAt string                      `yaml:"reviewed_at,omitempty"`
}

type artistRecord struct {
	Slug            string             `yaml:"slug"`
	Name            string             `yaml:"name"`
	PublicName      string             `yaml:"public_name,omitempty"`
	SpotifyID       string             `yaml:"spotify_id"`
	SpotifyIDs      []string           `yaml:"spotify_ids,omitempty"`
	SpotifyProfiles []profileRecord    `yaml:"spotify_profiles,omitempty"`
	Genres          []string           `yaml:"genres,omitempty"`
	Category        catalog.Category   `yaml:"category"`
	Roles           []catalog.Category `yaml:"roles,omitempty"`
	Aliases         []string           `yaml:"aliases"`
	Enabled         bool               `yaml:"enabled"`
	EditorialOrder  int                `yaml:"editorial_order,omitempty"`
	Notes           string             `yaml:"notes"`
	ExternalURL     string             `yaml:"external_url,omitempty"`
	ImageURL        string             `yaml:"image_url,omitempty"`
	AddedAt         string             `yaml:"added_at,omitempty"`
}

type profileRecord struct {
	ID            string                `yaml:"id"`
	Status        catalog.ProfileStatus `yaml:"status"`
	Evidence      []evidenceRecord      `yaml:"evidence,omitempty"`
	ReviewedAt    string                `yaml:"reviewed_at,omitempty"`
	Reviewer      string                `yaml:"reviewer,omitempty"`
	LastCheckedAt string                `yaml:"last_checked_at,omitempty"`
}

type evidenceRecord struct {
	Type      catalog.EvidenceType `yaml:"type"`
	URL       string               `yaml:"url,omitempty"`
	ReleaseID string               `yaml:"release_id,omitempty"`
	TrackID   string               `yaml:"track_id,omitempty"`
	Rationale string               `yaml:"rationale"`
}

func NewStore() Store {
	return Store{}
}

func (s Store) Load(ctx context.Context, path string) (catalog.EditorialCatalog, error) {
	select {
	case <-ctx.Done():
		return catalog.EditorialCatalog{}, ctx.Err()
	default:
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return catalog.EditorialCatalog{}, fmt.Errorf("read %s: %w", path, err)
	}

	var doc document
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		return catalog.EditorialCatalog{}, fmt.Errorf("parse %s: %w", path, err)
	}

	artists := make([]catalog.Artist, 0, len(doc.Artists))
	for _, record := range doc.Artists {
		artists = append(artists, record.toDomain())
	}

	return catalog.EditorialCatalog{
		Version:           doc.Version,
		ProfileSyncPolicy: doc.ProfileSyncPolicy,
		Artists:           artists,
		Relationships:     relationshipsToDomain(doc.Relationships),
	}, nil
}

func (s Store) Save(ctx context.Context, path string, c catalog.EditorialCatalog) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	doc := document{Version: c.Version, ProfileSyncPolicy: c.ProfileSyncPolicy, Artists: make([]artistRecord, 0, len(c.Artists))}
	for _, artist := range c.Artists {
		doc.Artists = append(doc.Artists, fromDomain(artist))
	}
	doc.Relationships = relationshipsFromDomain(c.Relationships)

	var buf bytes.Buffer
	buf.WriteString("# Editorial artist catalog.\n")
	buf.WriteString("# Spotify IDs are intentionally reviewed before sync.\n")
	buf.WriteString("# Entries remain disabled so validation can reject accidental syncs without a Spotify ID.\n")
	buf.WriteString("# `roles` preserves cases where an entry belongs to more than one editorial category.\n\n")

	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("encode artist catalog: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close YAML encoder: %w", err)
	}

	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, buf.Bytes()) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
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

	tmp, err := os.CreateTemp(filepath.Dir(path), ".artists-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary catalog file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temporary catalog file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temporary catalog file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

func (r artistRecord) toDomain() catalog.Artist {
	return catalog.Artist{
		Slug:            r.Slug,
		Name:            r.Name,
		PublicName:      r.PublicName,
		SpotifyID:       r.SpotifyID,
		SpotifyIDs:      r.SpotifyIDs,
		SpotifyProfiles: profilesToDomain(r.SpotifyProfiles),
		Genres:          r.Genres,
		Category:        r.Category,
		Roles:           r.Roles,
		Aliases:         r.Aliases,
		Enabled:         r.Enabled,
		EditorialOrder:  r.EditorialOrder,
		Notes:           r.Notes,
		ExternalURL:     r.ExternalURL,
		ImageURL:        r.ImageURL,
		AddedAt:         r.AddedAt,
	}
}

func fromDomain(a catalog.Artist) artistRecord {
	if a.Aliases == nil {
		a.Aliases = []string{}
	}
	if a.SpotifyIDs == nil {
		a.SpotifyIDs = []string{}
	}
	if a.Genres == nil {
		a.Genres = []string{}
	}

	return artistRecord{
		Slug:            a.Slug,
		Name:            a.Name,
		PublicName:      a.PublicName,
		SpotifyID:       a.SpotifyID,
		SpotifyIDs:      a.SpotifyIDs,
		SpotifyProfiles: profilesFromDomain(a.SpotifyProfiles),
		Genres:          a.Genres,
		Category:        a.Category,
		Roles:           a.Roles,
		Aliases:         a.Aliases,
		Enabled:         a.Enabled,
		EditorialOrder:  a.EditorialOrder,
		Notes:           a.Notes,
		ExternalURL:     a.ExternalURL,
		ImageURL:        a.ImageURL,
		AddedAt:         a.AddedAt,
	}
}

func profilesToDomain(records []profileRecord) []catalog.SpotifyProfile {
	if len(records) == 0 {
		return nil
	}
	profiles := make([]catalog.SpotifyProfile, 0, len(records))
	for _, r := range records {
		profile := catalog.SpotifyProfile{ID: r.ID, Status: r.Status, ReviewedAt: r.ReviewedAt, Reviewer: r.Reviewer, LastCheckedAt: r.LastCheckedAt}
		for _, e := range r.Evidence {
			profile.Evidence = append(profile.Evidence, catalog.Evidence(e))
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func profilesFromDomain(profiles []catalog.SpotifyProfile) []profileRecord {
	if len(profiles) == 0 {
		return nil
	}
	records := make([]profileRecord, 0, len(profiles))
	for _, p := range profiles {
		record := profileRecord{ID: p.ID, Status: p.Status, ReviewedAt: p.ReviewedAt, Reviewer: p.Reviewer, LastCheckedAt: p.LastCheckedAt}
		for _, e := range p.Evidence {
			record.Evidence = append(record.Evidence, evidenceRecord(e))
		}
		records = append(records, record)
	}
	return records
}

// writeBackup keeps the previous catalog next to it so a bad edit is recoverable.
func writeBackup(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write catalog backup %s: %w", path, err)
	}
	return nil
}

func relationshipsToDomain(records []relationshipRecord) []catalog.Relationship {
	if len(records) == 0 {
		return nil
	}
	rels := make([]catalog.Relationship, 0, len(records))
	for _, r := range records {
		rel := catalog.Relationship{
			Type: r.Type, From: r.From, To: r.To, Start: r.Start, End: r.End, Era: r.Era,
			Status: r.Status, Confidence: r.Confidence, Commentary: r.Commentary,
			Reviewer: r.Reviewer, ReviewedAt: r.ReviewedAt,
		}
		for _, e := range r.Evidence {
			rel.Evidence = append(rel.Evidence, catalog.Evidence(e))
		}
		rels = append(rels, rel)
	}
	return rels
}

// relationshipsFromDomain writes edges in a canonical order (type, from, to)
// so the file and every export derived from it stay deterministic.
func relationshipsFromDomain(rels []catalog.Relationship) []relationshipRecord {
	if len(rels) == 0 {
		return nil
	}
	sorted := make([]catalog.Relationship, 0, len(rels))
	for _, rel := range rels {
		sorted = append(sorted, rel.Normalized())
	}
	catalog.SortRelationships(sorted)

	records := make([]relationshipRecord, 0, len(sorted))
	for _, r := range sorted {
		record := relationshipRecord{
			Type: r.Type, From: r.From, To: r.To, Start: r.Start, End: r.End, Era: r.Era,
			Status: r.Status, Confidence: r.Confidence, Commentary: r.Commentary,
			Reviewer: r.Reviewer, ReviewedAt: r.ReviewedAt,
		}
		for _, e := range r.Evidence {
			record.Evidence = append(record.Evidence, evidenceRecord(e))
		}
		records = append(records, record)
	}
	return records
}
