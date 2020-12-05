package domain

type Artist struct {
	id    string
	name  string
	image string
}

func NewArtist(id, name, image string) Artist {
	return Artist{id: id, name: name, image: image}
}

func (a Artist) ID() string {
	return a.id
}

func (a Artist) Name() string {
	return a.name
}

func (a Artist) Image() string {
	return a.image
}
