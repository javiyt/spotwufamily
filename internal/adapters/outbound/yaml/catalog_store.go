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
	Version int            `yaml:"version"`
	Artists []artistRecord `yaml:"artists"`
}

type artistRecord struct {
	Slug           string             `yaml:"slug"`
	Name           string             `yaml:"name"`
	PublicName     string             `yaml:"public_name,omitempty"`
	SpotifyID      string             `yaml:"spotify_id"`
	SpotifyIDs     []string           `yaml:"spotify_ids,omitempty"`
	Genres         []string           `yaml:"genres,omitempty"`
	Category       catalog.Category   `yaml:"category"`
	Roles          []catalog.Category `yaml:"roles,omitempty"`
	Aliases        []string           `yaml:"aliases"`
	Enabled        bool               `yaml:"enabled"`
	EditorialOrder int                `yaml:"editorial_order,omitempty"`
	Notes          string             `yaml:"notes"`
	ExternalURL    string             `yaml:"external_url,omitempty"`
	AddedAt        string             `yaml:"added_at,omitempty"`
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

	return catalog.EditorialCatalog{Version: doc.Version, Artists: artists}, nil
}

func (s Store) Save(ctx context.Context, path string, c catalog.EditorialCatalog) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	doc := document{Version: c.Version, Artists: make([]artistRecord, 0, len(c.Artists))}
	for _, artist := range c.Artists {
		doc.Artists = append(doc.Artists, fromDomain(artist))
	}

	var buf bytes.Buffer
	buf.WriteString("# Generated from data/groups.txt.\n")
	buf.WriteString("# Spotify IDs are intentionally empty until each entry is resolved and reviewed.\n")
	buf.WriteString("# Entries remain disabled so validation can reject accidental syncs without a Spotify ID.\n")
	buf.WriteString("# `roles` preserves cases where an entry appeared in more than one section of the TXT.\n\n")

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
		Slug:           r.Slug,
		Name:           r.Name,
		PublicName:     r.PublicName,
		SpotifyID:      r.SpotifyID,
		SpotifyIDs:     r.SpotifyIDs,
		Genres:         r.Genres,
		Category:       r.Category,
		Roles:          r.Roles,
		Aliases:        r.Aliases,
		Enabled:        r.Enabled,
		EditorialOrder: r.EditorialOrder,
		Notes:          r.Notes,
		ExternalURL:    r.ExternalURL,
		AddedAt:        r.AddedAt,
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
		Slug:           a.Slug,
		Name:           a.Name,
		PublicName:     a.PublicName,
		SpotifyID:      a.SpotifyID,
		SpotifyIDs:     a.SpotifyIDs,
		Genres:         a.Genres,
		Category:       a.Category,
		Roles:          a.Roles,
		Aliases:        a.Aliases,
		Enabled:        a.Enabled,
		EditorialOrder: a.EditorialOrder,
		Notes:          a.Notes,
		ExternalURL:    a.ExternalURL,
		AddedAt:        a.AddedAt,
	}
}
