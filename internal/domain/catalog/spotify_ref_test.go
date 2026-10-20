package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func TestParseSpotifyArtistRef(t *testing.T) {
	const id = "7CrVv4zLga71gdlMOMfjXL"
	valid := []string{
		id,
		"  " + id + "  ",
		"spotify:artist:" + id,
		"https://open.spotify.com/artist/" + id,
		"https://open.spotify.com/artist/" + id + "?si=abc123",
		"https://open.spotify.com/intl-es/artist/" + id + "/",
		"https://OPEN.spotify.com:443/artist/" + id,
	}
	for _, in := range valid {
		got, err := catalog.ParseSpotifyArtistRef(in)
		require.NoError(t, err, in)
		require.Equal(t, id, got, in)
	}

	invalid := []string{
		"", "short", id + "x", "spotify:album:" + id, "spotify:artist:short",
		"http://open.spotify.com/artist/" + id,
		"https://open.spotify.com.evil.example/artist/" + id,
		"https://evil.example/artist/" + id,
		"https://user@open.spotify.com/artist/" + id,
		"https://open.spotify.com:8443/artist/" + id,
		"https://open.spotify.com/album/" + id,
		"https://open.spotify.com/artist/" + id + "/albums",
		"https://open.spotify.com/artist/\n" + id,
		"https://169.254.169.254/artist/" + id,
		"file:///etc/passwd",
		"javascript:alert(1)",
	}
	for _, in := range invalid {
		_, err := catalog.ParseSpotifyArtistRef(in)
		require.ErrorIs(t, err, catalog.ErrInvalidSpotifyRef, in)
	}
}

func TestSafeSpotifyImageURL(t *testing.T) {
	require.Equal(t, "https://i.scdn.co/image/ab67", catalog.SafeSpotifyImageURL("https://i.scdn.co/image/ab67"))
	require.Equal(t, "https://image-cdn-ak.spotifycdn.com/x", catalog.SafeSpotifyImageURL("https://image-cdn-ak.spotifycdn.com/x"))
	for _, bad := range []string{
		"", "http://i.scdn.co/x", "https://evil.example/x", "https://i.scdn.co.evil.example/x",
		"https://evilscdn.co/x", "javascript:alert(1)", "data:image/png;base64,AAAA", "https://u:p@i.scdn.co/x", "//i.scdn.co/x",
	} {
		require.Empty(t, catalog.SafeSpotifyImageURL(bad), bad)
	}
}
