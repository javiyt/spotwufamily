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

func TestSafeSpotifyReleaseURL(t *testing.T) {
	const id = "4eSQgbY6VJglgaRVlNPa8Q"
	want := "https://open.spotify.com/album/" + id
	for name, c := range map[string]struct{ raw, id, want string }{
		"canonical url":       {want, id, want},
		"query is dropped":    {want + "?si=abc", id, want},
		"locale prefix":       {"https://open.spotify.com/intl-es/album/" + id, "", want},
		"missing url uses id": {"", id, want},
		"padded url":          {"  " + want + " ", "", want},
		"url wins over id":    {want, "3wA9siXD12mPM77pU0567n", want},
		"no url and no id":    {"", "", ""},
		"malformed id":        {"", "short", ""},
		"javascript scheme":   {"javascript:alert(1)", "", ""},
		"http scheme":         {"http://open.spotify.com/album/" + id, "", ""},
		"foreign host":        {"https://evil.example/album/" + id, "", ""},
		"lookalike host":      {"https://open.spotify.com.evil.example/album/" + id, "", ""},
		"userinfo":            {"https://open.spotify.com@evil.example/album/" + id, "", ""},
		"track url":           {"https://open.spotify.com/track/" + id, "", ""},
		"artist url":          {"https://open.spotify.com/artist/" + id, "", ""},
		"bad id in url":       {"https://open.spotify.com/album/not-an-id", "", ""},
		"bad url good id":     {"https://evil.example/x", id, want},
		"odd port":            {"https://open.spotify.com:8443/album/" + id, "", ""},
		"extra path":          {want + "/../../x", "", ""},
		"data url":            {"data:text/html,<script>1</script>", "", ""},
		"malicious url no id": {"https://open.spotify.com/album/" + id + `"><script>`, "", ""},
	} {
		require.Equal(t, c.want, catalog.SafeSpotifyReleaseURL(c.raw, c.id), name)
	}
}
