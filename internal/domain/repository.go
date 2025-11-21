//go:generate go run github.com/vektra/mockery/v2/ --inpackage --name=ArtistRepository

package domain

// ArtistRepository defines operations to search artists from a backing store or API.
type ArtistRepository interface {
	SearchArtist(name string) ([]Artist, error)
}
