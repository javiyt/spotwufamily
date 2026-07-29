package spotify

import "github.com/javiyt/spotwufamily/internal/domain/catalog"

type searchResponse struct {
	Artists artistPage `json:"artists"`
}

type artistPage struct {
	Items []artistObject `json:"items"`
}

type artistObject struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ExternalURLs map[string]string `json:"external_urls"`
	Images       []imageObject     `json:"images"`
	Popularity   int               `json:"popularity"`
	Followers    followersObject   `json:"followers"`
	Genres       []string          `json:"genres"`
}

type followersObject struct {
	Total int `json:"total"`
}

type imageObject struct {
	URL    string `json:"url"`
	Height int    `json:"height"`
	Width  int    `json:"width"`
}

type albumsPage struct {
	Items []albumObject `json:"items"`
	Next  string        `json:"next"`
}

type albumObject struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	AlbumType            string            `json:"album_type"`
	AlbumGroup           string            `json:"album_group"`
	ReleaseDate          string            `json:"release_date"`
	ReleaseDatePrecision string            `json:"release_date_precision"`
	Label                string            `json:"label"`
	TotalTracks          int               `json:"total_tracks"`
	ExternalURLs         map[string]string `json:"external_urls"`
	Images               []imageObject     `json:"images"`
	Artists              []artistObject    `json:"artists"`
	Copyrights           []copyrightObject `json:"copyrights"`
}

type copyrightObject struct {
	Text string `json:"text"`
	Type string `json:"type"`
}

type tracksPage struct {
	Items []trackObject `json:"items"`
	Next  string        `json:"next"`
}

type tracksResponse struct {
	Tracks []trackObject `json:"tracks"`
}

type trackObject struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	DiscNumber   int               `json:"disc_number"`
	TrackNumber  int               `json:"track_number"`
	DurationMS   int               `json:"duration_ms"`
	Explicit     bool              `json:"explicit"`
	PreviewURL   string            `json:"preview_url"`
	ExternalIDs  map[string]string `json:"external_ids"`
	ExternalURLs map[string]string `json:"external_urls"`
	Artists      []artistObject    `json:"artists"`
}

func (a artistObject) toCandidate() catalog.ArtistCandidate {
	imageURL := ""
	if len(a.Images) > 0 {
		imageURL = a.Images[0].URL
	}
	images := make([]catalog.Image, 0, len(a.Images))
	for _, image := range a.Images {
		images = append(images, catalog.Image{URL: image.URL, Height: image.Height, Width: image.Width})
	}

	return catalog.ArtistCandidate{
		Name:       a.Name,
		SpotifyID:  a.ID,
		URL:        a.ExternalURLs["spotify"],
		ImageURL:   imageURL,
		Images:     images,
		Popularity: a.Popularity,
		Followers:  a.Followers.Total,
		Genres:     append([]string(nil), a.Genres...),
	}
}

func (a albumObject) toAlbum() catalog.Release {
	artists := make([]catalog.ArtistCandidate, 0, len(a.Artists))
	for _, artist := range a.Artists {
		artists = append(artists, artist.toCandidate())
	}

	images := make([]catalog.Image, 0, len(a.Images))
	for _, image := range a.Images {
		images = append(images, catalog.Image{URL: image.URL, Height: image.Height, Width: image.Width})
	}

	copyrights := make([]catalog.Copyright, 0, len(a.Copyrights))
	for _, item := range a.Copyrights {
		copyrights = append(copyrights, catalog.Copyright{Text: item.Text, Type: item.Type})
	}

	return catalog.Release{
		SpotifyID:            a.ID,
		Name:                 a.Name,
		AlbumType:            a.AlbumType,
		AlbumGroup:           a.AlbumGroup,
		ReleaseDate:          a.ReleaseDate,
		ReleaseDatePrecision: a.ReleaseDatePrecision,
		Label:                a.Label,
		TotalTracks:          a.TotalTracks,
		URL:                  a.ExternalURLs["spotify"],
		Images:               images,
		Artists:              artists,
		Copyrights:           copyrights,
	}
}

func (t trackObject) toTrack() catalog.Track {
	artists := make([]catalog.ArtistCandidate, 0, len(t.Artists))
	for _, artist := range t.Artists {
		artists = append(artists, artist.toCandidate())
	}

	return catalog.Track{
		SpotifyID:   t.ID,
		Name:        t.Name,
		DiscNumber:  t.DiscNumber,
		TrackNumber: t.TrackNumber,
		DurationMS:  t.DurationMS,
		Explicit:    t.Explicit,
		ISRC:        t.ExternalIDs["isrc"],
		PreviewURL:  t.PreviewURL,
		URL:         t.ExternalURLs["spotify"],
		Artists:     artists,
	}
}
