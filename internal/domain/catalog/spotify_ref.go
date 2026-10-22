package catalog

import (
	"errors"
	"net/url"
	"strings"
)

// ErrInvalidSpotifyRef means the input is not a Spotify artist ID, URI or link.
var ErrInvalidSpotifyRef = errors.New("not a Spotify artist ID, URI or open.spotify.com artist link")

// ParseSpotifyArtistRef extracts the 22-character artist ID from a bare ID, a
// spotify:artist: URI or an https://open.spotify.com/artist/ link. It only
// parses text: the input is never used as a request target, so a hostile URL
// cannot make the server fetch anything.
func ParseSpotifyArtistRef(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 300 || strings.ContainsAny(input, " \t\r\n") {
		return "", ErrInvalidSpotifyRef
	}
	if spotifyIDPattern.MatchString(input) {
		return input, nil
	}
	if id, ok := strings.CutPrefix(input, "spotify:artist:"); ok {
		if spotifyIDPattern.MatchString(id) {
			return id, nil
		}
		return "", ErrInvalidSpotifyRef
	}

	u, err := url.Parse(input)
	if err != nil || u.Scheme != "https" || u.User != nil || strings.ToLower(u.Hostname()) != "open.spotify.com" ||
		(u.Port() != "" && u.Port() != "443") {
		return "", ErrInvalidSpotifyRef
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	// Locale prefixes such as /intl-es/ are cosmetic.
	if len(segments) > 0 && strings.HasPrefix(segments[0], "intl-") {
		segments = segments[1:]
	}
	if len(segments) != 2 || segments[0] != "artist" || !spotifyIDPattern.MatchString(segments[1]) {
		return "", ErrInvalidSpotifyRef
	}
	return segments[1], nil
}

// IsSpotifyID reports whether id has the shape of a Spotify object ID.
func IsSpotifyID(id string) bool { return spotifyIDPattern.MatchString(id) }

// SpotifyArtistURL is the canonical public link of an artist profile.
func SpotifyArtistURL(id string) string { return spotifyURLForID(id) }

// SpotifyAlbumURL is the canonical public link of a release. Spotify singles
// are album entities, so they share this form. It returns "" for an id that is
// not a Spotify ID.
func SpotifyAlbumURL(id string) string {
	if !IsSpotifyID(id) {
		return ""
	}
	return "https://open.spotify.com/album/" + id
}

// SafeSpotifyReleaseURL returns the canonical album link for a release. raw is
// trusted only when it is an https open.spotify.com/album/<id> link; anything
// else falls back to the link derived from a valid id, and to "" when neither
// is safe. The result is rebuilt, so queries and fragments never survive.
func SafeSpotifyReleaseURL(raw, id string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Scheme == "https" && u.User == nil &&
		strings.ToLower(u.Hostname()) == "open.spotify.com" && (u.Port() == "" || u.Port() == "443") {
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segments) > 0 && strings.HasPrefix(segments[0], "intl-") {
			segments = segments[1:]
		}
		if len(segments) == 2 && segments[0] == "album" {
			return SpotifyAlbumURL(segments[1])
		}
	}
	return SpotifyAlbumURL(id)
}

// SafeSpotifyImageURL returns raw only when it is an https URL on a Spotify
// image CDN, and "" otherwise, so the admin never points a browser at an
// arbitrary host.
func SafeSpotifyImageURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "scdn.co" || strings.HasSuffix(host, ".scdn.co") || host == "spotifycdn.com" || strings.HasSuffix(host, ".spotifycdn.com") {
		return u.String()
	}
	return ""
}
