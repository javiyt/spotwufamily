package domain

type Artist struct {
	id string
	name string
}

func NewArtist(id string, name string) Artist {
	return Artist{id: id, name: name}
}

func (a Artist) Id() string {
	return a.id
}

func (a Artist) Name() string {
	return a.name
}
