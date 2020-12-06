//go:generate go run github.com/vektra/mockery/v2/ --inpackage --name=ArtistRepository

package domain

type ArtistRepository interface {
	SearchArtist(name string) ([]Artist, error)
}
