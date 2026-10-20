package spotify_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/stretchr/testify/require"
)

func profileSourceServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		default:
			w.Header().Set("Retry-After", "72000")
			http.Error(w, body, status)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProfileSourceMapsSpotifyErrorsToSourceErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"404 is not found", http.StatusNotFound, `{"error":{"status":404,"message":"Non existing id"}}`, artists.ErrSourceNotFound},
		{"429 is rate limited", http.StatusTooManyRequests, `{"error":{"status":429,"message":"slow down"}}`, artists.ErrSourceRateLimited},
		{"400 is unavailable", http.StatusBadRequest, "bad", artists.ErrSourceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := profileSourceServer(t, tt.status, tt.body)
			source := spotify.NewProfileSource(newTestClient(t, server.URL, nil))

			_, err := source.GetArtist(context.Background(), "0FCYBhXjpfQIQWNKiVEDg5")
			require.ErrorIs(t, err, tt.want)
			_, err = source.GetArtistAlbums(context.Background(), "0FCYBhXjpfQIQWNKiVEDg5", []string{"album"})
			require.ErrorIs(t, err, tt.want)
		})
	}
}
