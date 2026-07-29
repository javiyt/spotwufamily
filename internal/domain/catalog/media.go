package catalog

type Image struct {
	URL    string
	Height int
	Width  int
}

type Copyright struct {
	Text string
	Type string
}

type Release struct {
	SpotifyID            string
	Name                 string
	AlbumType            string
	AlbumGroup           string
	ReleaseDate          string
	ReleaseDatePrecision string
	Label                string
	TotalTracks          int
	URL                  string
	Images               []Image
	Artists              []ArtistCandidate
	Copyrights           []Copyright
}

type Track struct {
	SpotifyID   string
	Name        string
	DiscNumber  int
	TrackNumber int
	DurationMS  int
	Explicit    bool
	ISRC        string
	PreviewURL  string
	URL         string
	Artists     []ArtistCandidate
}

type ReleaseTracks struct {
	Release Release
	Tracks  []Track
}
