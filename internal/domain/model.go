// Package domain defines domain models and interfaces used across the project.
package domain

// Artist represents a music artist with basic metadata.
type Artist struct {
	id    string
	name  string
	image string
}

// NewArtist creates a new Artist value.
func NewArtist(id, name, image string) Artist {
	return Artist{id: id, name: name, image: image}
}

// ID returns the artist identifier.
func (a Artist) ID() string {
	return a.id
}

// Name returns the artist name.
func (a Artist) Name() string {
	return a.name
}

// Image returns the artist image URL (may be empty).
func (a Artist) Image() string {
	return a.image
}
