package spotify_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	spotifyadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/spotify"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestSearchArtistCandidatesUsesClientCredentialsAndMapsArtists(t *testing.T) {
	var tokenCalls atomic.Int32
	var searchCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			tokenCalls.Add(1)
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("client:secret")), r.Header.Get("Authorization"))
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client_credentials", r.Form.Get("grant_type"))
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		case "/v1/search":
			searchCalls.Add(1)
			require.Equal(t, "Bearer token-1", r.Header.Get("Authorization"))
			require.Equal(t, "artist", r.URL.Query().Get("type"))
			require.Equal(t, "Gravediggaz", r.URL.Query().Get("q"))
			require.Equal(t, "ES", r.URL.Query().Get("market"))
			_, _ = fmt.Fprint(w, `{
				"artists": {
					"items": [
						{
							"id": "0CH4f9m2L3TRaA5oErU2p0",
							"name": "Gravediggaz",
							"external_urls": {"spotify": "https://open.spotify.com/artist/0CH4f9m2L3TRaA5oErU2p0"},
							"images": [{"url": "https://image/large.jpg", "height": 640, "width": 640}],
							"popularity": 45,
							"followers": {"total": 1234},
							"genres": ["hip hop"]
						}
					]
				}
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)

	candidates, err := client.SearchArtistCandidates(context.Background(), catalog.Artist{Name: "Gravediggaz"})

	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "Gravediggaz", candidates[0].Name)
	require.Equal(t, "0CH4f9m2L3TRaA5oErU2p0", candidates[0].SpotifyID)
	require.Equal(t, "https://image/large.jpg", candidates[0].ImageURL)
	require.Equal(t, []catalog.Image{
		{URL: "https://image/large.jpg", Height: 640, Width: 640},
	}, candidates[0].Images)
	require.Equal(t, 45, candidates[0].Popularity)
	require.Equal(t, 1234, candidates[0].Followers)
	require.Equal(t, []string{"hip hop"}, candidates[0].Genres)
	require.Equal(t, int32(1), tokenCalls.Load())
	require.Equal(t, int32(1), searchCalls.Load())
}

func TestGetArtistAlbumsFollowsPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		case "/v1/artists/artist-1/albums":
			offset := r.URL.Query().Get("offset")
			if offset == "" {
				next := r.Host + "/v1/artists/artist-1/albums?offset=1"
				_, _ = fmt.Fprintf(w, `{"items":[{"id":"album-1","name":"First","album_type":"album","album_group":"album","total_tracks":10}],"next":"http://%s"}`, next)
				return
			}
			_, _ = fmt.Fprint(w, `{"items":[{"id":"album-2","name":"Second","album_type":"single","album_group":"single","total_tracks":1}],"next":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)

	albums, err := client.GetArtistAlbums(context.Background(), "artist-1", []string{"album", "single"})

	require.NoError(t, err)
	require.Len(t, albums, 2)
	require.Equal(t, "album-1", albums[0].SpotifyID)
	require.Equal(t, "album-2", albums[1].SpotifyID)
}

func TestRetriesRateLimitsAndTemporaryErrors(t *testing.T) {
	var calls atomic.Int32
	var slept []time.Duration

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		case "/v1/search":
			call := calls.Add(1)
			switch call {
			case 1:
				w.Header().Set("Retry-After", "2")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
			case 2:
				http.Error(w, "temporary", http.StatusBadGateway)
			default:
				_, _ = fmt.Fprint(w, `{"artists":{"items":[]}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, func(_ context.Context, duration time.Duration) error {
		slept = append(slept, duration)
		return nil
	})

	candidates, err := client.SearchArtists(context.Background(), "Wu-Tang Clan", 20)

	require.NoError(t, err)
	require.Empty(t, candidates)
	require.Equal(t, int32(3), calls.Load())
	require.Contains(t, slept, 2*time.Second)
}

func TestRateLimitAboveMaxRetryAfterReturnsError(t *testing.T) {
	var calls atomic.Int32
	var slept []time.Duration

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		case "/v1/search":
			calls.Add(1)
			w.Header().Set("Retry-After", "72000")
			http.Error(w, `{"error":{"status":429,"message":"Too many requests","reason":"QUOTA_EXCEEDED"}}`, http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, func(_ context.Context, duration time.Duration) error {
		slept = append(slept, duration)
		return nil
	})

	_, err := client.SearchArtists(context.Background(), "Wu-Tang Clan", 20)

	require.Error(t, err)
	require.ErrorIs(t, err, spotifyadapter.ErrQuotaExceeded)
	require.ErrorIs(t, err, spotifyadapter.ErrRateLimited)
	require.ErrorIs(t, err, spotifyadapter.ErrTemporary)
	require.Contains(t, err.Error(), "above max wait")
	require.Equal(t, int32(1), calls.Load())
	require.Empty(t, slept)
}

func TestDoesNotRetryPermanentErrors(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		case "/v1/search":
			calls.Add(1)
			http.Error(w, "bad query", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)

	_, err := client.SearchArtists(context.Background(), "bad", 20)

	require.ErrorIs(t, err, spotifyadapter.ErrPermanent)
	require.Equal(t, int32(1), calls.Load())
}

func TestTokenUnauthorizedReturnsErrorWithoutDeadlock(t *testing.T) {
	var tokenCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			tokenCalls.Add(1)
			http.Error(w, "invalid client", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.GetArtist(ctx, "artist-1")

	require.Error(t, err)
	require.ErrorIs(t, err, spotifyadapter.ErrPermanent)
	require.Contains(t, err.Error(), "request Spotify token")
	require.Equal(t, int32(1), tokenCalls.Load())
}

func TestGetAlbumTracksAndGetTracksMapTrackFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			_, _ = fmt.Fprint(w, `{"access_token":"token-1","token_type":"Bearer","expires_in":3600}`)
		case "/v1/albums/album-1/tracks":
			_, _ = fmt.Fprint(w, `{"items":[{"id":"track-1","name":"Track One","disc_number":1,"track_number":2,"duration_ms":123000,"explicit":true,"external_urls":{"spotify":"https://track"},"artists":[{"id":"artist-1","name":"Artist One"}]}],"next":null}`)
		case "/v1/tracks":
			ids := strings.Split(r.URL.Query().Get("ids"), ",")
			require.Equal(t, []string{"track-1"}, ids)
			_, _ = fmt.Fprint(w, `{"tracks":[{"id":"track-1","name":"Track One","external_ids":{"isrc":"US1234567890"},"preview_url":"https://preview","artists":[{"id":"artist-1","name":"Artist One"}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)

	albumTracks, err := client.GetAlbumTracks(context.Background(), "album-1")
	require.NoError(t, err)
	require.Len(t, albumTracks, 1)
	require.Equal(t, 2, albumTracks[0].TrackNumber)
	require.True(t, albumTracks[0].Explicit)

	tracks, err := client.GetTracks(context.Background(), []string{"track-1"})
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	require.Equal(t, "US1234567890", tracks[0].ISRC)
	require.Equal(t, "https://preview", tracks[0].PreviewURL)
}

func newTestClient(t *testing.T, serverURL string, sleep func(context.Context, time.Duration) error) *spotifyadapter.Client {
	t.Helper()

	if sleep == nil {
		sleep = func(context.Context, time.Duration) error { return nil }
	}

	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)

	client, err := spotifyadapter.NewClient(spotifyadapter.Config{
		ClientID:     "client",
		ClientSecret: "secret",
		Market:       "ES",
		APIBaseURL:   parsed.String(),
		TokenURL:     parsed.String() + "/api/token",
		HTTPClient:   http.DefaultClient,
		MaxRetries:   3,
		Sleep:        sleep,
		Now:          func() time.Time { return time.Unix(1000, 0).UTC() },
	})
	require.NoError(t, err)

	return client
}
