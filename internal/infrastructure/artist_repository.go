// Package infrastructure provides implementations of domain repositories.
package infrastructure

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/javiyt/spotwufamily/internal/domain"
	"github.com/zmb3/spotify"
)

// ArtistProxyRepository delegates artist lookups to an HTTP repository.
type ArtistProxyRepository struct {
	artistHTTPRepo *ArtistHTTPRepository
	fileArtistRepo *FileArtistRepository
}

// NewArtistProxyRepository constructs a new ArtistProxyRepository.
func NewArtistProxyRepository(artistHTTPRepo *ArtistHTTPRepository, fileArtistRepo *FileArtistRepository) *ArtistProxyRepository {
	return &ArtistProxyRepository{artistHTTPRepo: artistHTTPRepo, fileArtistRepo: fileArtistRepo}
}

// SearchArtist looks up artists by name using the underlying HTTP repository.
func (a *ArtistProxyRepository) SearchArtist(name string) ([]domain.Artist, error) {
	return a.artistHTTPRepo.SearchArtist(name)
}

func (a *ArtistProxyRepository) SaveArtist(artist domain.Artist) error {
	// No-op for the proxy repository.
	return a.fileArtistRepo.SaveArtist(artist)
}

// ArtistHTTPRepository performs lookups against the Spotify HTTP API.
type ArtistHTTPRepository struct {
	c spotify.Client
}

// NewArtistHTTPRepository constructs a new ArtistHTTPRepository.
func NewArtistHTTPRepository(c spotify.Client) *ArtistHTTPRepository {
	return &ArtistHTTPRepository{c: c}
}

// SearchArtist queries Spotify for artists by name and converts results to domain.Artist.
func (a *ArtistHTTPRepository) SearchArtist(name string) ([]domain.Artist, error) {
	artists := make([]domain.Artist, 0)

	search, err := a.c.Search(name, spotify.SearchTypeArtist)
	if err != nil {
		return nil, fmt.Errorf("error %w searching for artist %s", err, name)
	}

	for idx := range search.Artists.Artists {
		image := ""
		if len(search.Artists.Artists[idx].Images) > 0 {
			image = search.Artists.Artists[idx].Images[0].URL
		}

		artists = append(
			artists,
			domain.NewArtist(
				search.Artists.Artists[idx].ID.String(),
				search.Artists.Artists[idx].Name,
				image,
			),
		)
	}

	return artists, nil
}

func (a *ArtistHTTPRepository) SaveArtist(artist domain.Artist) error {
	// No-op for the HTTP repository.
	return nil
}

type FileArtistRepository struct {
	databaseFile string
	// Implementation details for file-based storage would go here.
}

type fileArtist struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
}

type fileDB struct {
	Artists []fileArtist `json:"artists"`
}

func NewFileArtistRepository(databaseFile string) *FileArtistRepository {
	return &FileArtistRepository{databaseFile: databaseFile}
}

func (f *FileArtistRepository) SearchArtist(name string) ([]domain.Artist, error) {
	// Implementation for searching an artist in file storage would go here.
	return nil, nil
}

// Implementation for saving an artist to file storage would go here.
// It should receive an artist open the given json file and append the new artist to it in case it doesn´t exist yet.
// If the artist already exists, it should update its information.
func (f *FileArtistRepository) SaveArtist(artist domain.Artist) error {

	// Try to read existing database file. If it doesn't exist, start with an empty DB.
	db := fileDB{Artists: []fileArtist{}}

	data, err := os.ReadFile(f.databaseFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("error reading database file: %w", err)
		}
		// File doesn't exist: we'll create it when writing.
	} else if len(data) > 0 {
		if err := json.Unmarshal(data, &db); err != nil {
			return fmt.Errorf("error unmarshalling database file: %w", err)
		}
	}

	// Map incoming domain.Artist to fileArtist
	incoming := fileArtist{
		ID:    artist.ID(),
		Name:  artist.Name(),
		Image: artist.Image(),
	}

	// Check if artist exists; update or append accordingly.
	updated := false
	for i := range db.Artists {
		if db.Artists[i].ID == incoming.ID {
			db.Artists[i].Name = incoming.Name
			db.Artists[i].Image = incoming.Image
			updated = true
			break
		}
	}

	if !updated {
		db.Artists = append(db.Artists, incoming)
	}

	// Marshal and write back to file.
	out, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshalling database: %w", err)
	}

	if err := os.WriteFile(f.databaseFile, out, 0o644); err != nil {
		return fmt.Errorf("error writing database file: %w", err)
	}

	return nil
}
