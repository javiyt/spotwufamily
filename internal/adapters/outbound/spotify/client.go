package spotify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	defaultAPIBaseURL    = "https://api.spotify.com"
	defaultTokenURL      = "https://accounts.spotify.com/api/token"
	defaultMarket        = "ES"
	defaultTimeout       = 15 * time.Second
	defaultRetries       = 3
	defaultMaxRetryAfter = 10 * time.Minute
)

var (
	ErrPermanent = errors.New("permanent Spotify error")
	ErrTemporary = errors.New("temporary Spotify error")
)

type Config struct {
	ClientID      string
	ClientSecret  string
	Market        string
	APIBaseURL    string
	TokenURL      string
	HTTPClient    *http.Client
	MaxRetries    int
	MaxRetryAfter time.Duration
	Sleep         func(context.Context, time.Duration) error
	Now           func() time.Time
	Progress      func(ProgressEvent)
}

type Client struct {
	clientID      string
	clientSecret  string
	market        string
	apiBaseURL    string
	tokenURL      string
	httpClient    *http.Client
	maxRetries    int
	maxRetryAfter time.Duration
	sleep         func(context.Context, time.Duration) error
	now           func() time.Time
	progress      func(ProgressEvent)

	tokenMu sync.Mutex
	token   accessToken
}

type retryOptions struct {
	expireTokenOnUnauthorized bool
}

type ProgressEvent struct {
	Stage      string
	Method     string
	URL        string
	Attempt    int
	MaxRetries int
	StatusCode int
	Wait       time.Duration
	Err        error
}

type accessToken struct {
	value     string
	tokenType string
	expiresAt time.Time
}

func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, fmt.Errorf("spotify client ID is required")
	}
	if strings.TrimSpace(config.ClientSecret) == "" {
		return nil, fmt.Errorf("spotify client secret is required")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	apiBaseURL := strings.TrimRight(config.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	tokenURL := config.TokenURL
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	market := config.Market
	if market == "" {
		market = defaultMarket
	}
	maxRetries := config.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultRetries
	}
	maxRetryAfter := config.MaxRetryAfter
	if maxRetryAfter == 0 {
		maxRetryAfter = defaultMaxRetryAfter
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &Client{
		clientID:      config.ClientID,
		clientSecret:  config.ClientSecret,
		market:        market,
		apiBaseURL:    apiBaseURL,
		tokenURL:      tokenURL,
		httpClient:    httpClient,
		maxRetries:    maxRetries,
		maxRetryAfter: maxRetryAfter,
		sleep:         sleep,
		now:           now,
		progress:      config.Progress,
	}, nil
}

func (c *Client) SearchArtistCandidates(ctx context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	return c.SearchArtists(ctx, artist.Name, 20)
}

func (c *Client) SearchArtists(ctx context.Context, query string, limit int) ([]catalog.ArtistCandidate, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	values := url.Values{}
	values.Set("q", query)
	values.Set("type", "artist")
	values.Set("limit", strconv.Itoa(limit))
	values.Set("market", c.market)

	var response searchResponse
	if err := c.getJSON(ctx, "/v1/search?"+values.Encode(), &response); err != nil {
		return nil, fmt.Errorf("search artists %q: %w", query, err)
	}

	candidates := make([]catalog.ArtistCandidate, 0, len(response.Artists.Items))
	for _, item := range response.Artists.Items {
		candidates = append(candidates, item.toCandidate())
	}

	return candidates, nil
}

func (c *Client) GetArtist(ctx context.Context, spotifyID string) (catalog.ArtistCandidate, error) {
	var response artistObject
	if err := c.getJSON(ctx, "/v1/artists/"+url.PathEscape(spotifyID), &response); err != nil {
		return catalog.ArtistCandidate{}, fmt.Errorf("get artist %s: %w", spotifyID, err)
	}

	return response.toCandidate(), nil
}

func (c *Client) GetArtistAlbums(ctx context.Context, spotifyID string, groups []string) ([]catalog.Release, error) {
	values := url.Values{}
	values.Set("limit", "50")
	values.Set("market", c.market)
	if len(groups) > 0 {
		values.Set("include_groups", strings.Join(groups, ","))
	}

	var albums []catalog.Release
	path := "/v1/artists/" + url.PathEscape(spotifyID) + "/albums?" + values.Encode()
	for path != "" {
		var page albumsPage
		if err := c.getJSON(ctx, path, &page); err != nil {
			return nil, fmt.Errorf("get artist albums %s: %w", spotifyID, err)
		}
		for _, item := range page.Items {
			albums = append(albums, item.toAlbum())
		}
		path = nextPath(page.Next)
	}

	return albums, nil
}

func (c *Client) GetAlbum(ctx context.Context, spotifyID string) (catalog.Release, error) {
	values := url.Values{}
	values.Set("market", c.market)

	var response albumObject
	if err := c.getJSON(ctx, "/v1/albums/"+url.PathEscape(spotifyID)+"?"+values.Encode(), &response); err != nil {
		return catalog.Release{}, fmt.Errorf("get album %s: %w", spotifyID, err)
	}

	return response.toAlbum(), nil
}

func (c *Client) GetAlbumTracks(ctx context.Context, spotifyID string) ([]catalog.Track, error) {
	values := url.Values{}
	values.Set("limit", "50")
	values.Set("market", c.market)

	var tracks []catalog.Track
	path := "/v1/albums/" + url.PathEscape(spotifyID) + "/tracks?" + values.Encode()
	for path != "" {
		var page tracksPage
		if err := c.getJSON(ctx, path, &page); err != nil {
			return nil, fmt.Errorf("get album tracks %s: %w", spotifyID, err)
		}
		for _, item := range page.Items {
			tracks = append(tracks, item.toTrack())
		}
		path = nextPath(page.Next)
	}

	return tracks, nil
}

func (c *Client) GetTracks(ctx context.Context, spotifyIDs []string) ([]catalog.Track, error) {
	if len(spotifyIDs) == 0 {
		return []catalog.Track{}, nil
	}

	var tracks []catalog.Track
	for start := 0; start < len(spotifyIDs); start += 50 {
		end := start + 50
		if end > len(spotifyIDs) {
			end = len(spotifyIDs)
		}

		values := url.Values{}
		values.Set("ids", strings.Join(spotifyIDs[start:end], ","))
		values.Set("market", c.market)

		var response tracksResponse
		if err := c.getJSON(ctx, "/v1/tracks?"+values.Encode(), &response); err != nil {
			return nil, fmt.Errorf("get tracks batch: %w", err)
		}
		for _, item := range response.Tracks {
			tracks = append(tracks, item.toTrack())
		}
	}

	return tracks, nil
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	body, err := c.doAPI(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode Spotify response: %w", err)
	}

	return nil
}

func (c *Client) doAPI(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	requestURL := c.apiURL(path)
	return c.doWithRetry(ctx, retryOptions{expireTokenOnUnauthorized: true}, func() (*http.Request, error) {
		token, err := c.bearerToken(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
		if err != nil {
			return nil, fmt.Errorf("build Spotify request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
}

func (c *Client) bearerToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token.value != "" && c.now().Before(c.token.expiresAt.Add(-30*time.Second)) {
		return c.token.value, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(c.clientID + ":" + c.clientSecret))
	body, err := c.doWithRetry(ctx, retryOptions{}, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, fmt.Errorf("build Spotify token request: %w", err)
		}
		req.Header.Set("Authorization", "Basic "+encodedCredentials)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return "", fmt.Errorf("request Spotify token: %w", err)
	}

	var response tokenResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode Spotify token response: %w", err)
	}
	if response.AccessToken == "" {
		return "", fmt.Errorf("Spotify token response missing access_token")
	}
	if response.TokenType == "" {
		response.TokenType = "Bearer"
	}
	if !strings.EqualFold(response.TokenType, "Bearer") {
		return "", fmt.Errorf("unsupported Spotify token type %q", response.TokenType)
	}

	c.token = accessToken{
		value:     response.AccessToken,
		tokenType: response.TokenType,
		expiresAt: c.now().Add(time.Duration(response.ExpiresIn) * time.Second),
	}

	return c.token.value, nil
}

func (c *Client) doWithRetry(ctx context.Context, options retryOptions, buildRequest func() (*http.Request, error)) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}

		req, err := buildRequest()
		if err != nil {
			return nil, err
		}
		event := ProgressEvent{
			Stage:      "request_started",
			Method:     req.Method,
			URL:        requestLogURL(req.URL),
			Attempt:    attempt + 1,
			MaxRetries: c.maxRetries,
		}
		c.emitProgress(event)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: request failed: %w", ErrTemporary, err)
			event.Stage = "request_failed"
			event.Err = err
			c.emitProgress(event)
			continue
		}

		data, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		event.StatusCode = resp.StatusCode
		if readErr != nil {
			event.Stage = "request_failed"
			event.Err = readErr
			c.emitProgress(event)
			return nil, fmt.Errorf("read Spotify response: %w", readErr)
		}
		if closeErr != nil {
			event.Stage = "request_failed"
			event.Err = closeErr
			c.emitProgress(event)
			return nil, fmt.Errorf("close Spotify response: %w", closeErr)
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			event.Stage = "request_finished"
			c.emitProgress(event)
			return data, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = spotifyHTTPError(resp.StatusCode, data, ErrTemporary)
			wait := retryAfter(resp.Header)
			event.Stage = "request_retrying"
			event.Wait = wait
			event.Err = lastErr
			c.emitProgress(event)
			if c.maxRetryAfter > 0 && wait > c.maxRetryAfter {
				event.Stage = "request_failed"
				event.Err = fmt.Errorf("%w: Spotify requested retry after %s, above max wait %s: %w", ErrTemporary, wait, c.maxRetryAfter, lastErr)
				c.emitProgress(event)
				return nil, event.Err
			}
			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
		case resp.StatusCode >= 500:
			lastErr = spotifyHTTPError(resp.StatusCode, data, ErrTemporary)
			event.Stage = "request_retrying"
			event.Wait = backoff(attempt + 1)
			event.Err = lastErr
			c.emitProgress(event)
		case resp.StatusCode == http.StatusUnauthorized && options.expireTokenOnUnauthorized && attempt == 0:
			c.expireToken()
			lastErr = spotifyHTTPError(resp.StatusCode, data, ErrTemporary)
			event.Stage = "request_retrying"
			event.Wait = backoff(attempt + 1)
			event.Err = lastErr
			c.emitProgress(event)
		default:
			event.Stage = "request_failed"
			event.Err = spotifyHTTPError(resp.StatusCode, data, ErrPermanent)
			c.emitProgress(event)
			return nil, event.Err
		}
	}

	return nil, lastErr
}

func (c *Client) emitProgress(event ProgressEvent) {
	if c.progress != nil {
		c.progress(event)
	}
}

func requestLogURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	value := u.Path
	if u.RawQuery != "" {
		value += "?" + u.RawQuery
	}
	if len(value) > 180 {
		value = value[:180] + "..."
	}
	return value
}

func (c *Client) apiURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if strings.HasPrefix(path, "/") {
		return c.apiBaseURL + path
	}

	return c.apiBaseURL + "/" + path
}

func (c *Client) expireToken() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = accessToken{}
}

func spotifyHTTPError(statusCode int, body []byte, kind error) error {
	message := strings.TrimSpace(string(body))
	if len(message) > 240 {
		message = message[:240]
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}

	return fmt.Errorf("%w: HTTP %d: %s", kind, statusCode, message)
}

func retryAfter(header http.Header) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

func backoff(attempt int) time.Duration {
	base := 100 * time.Millisecond
	delay := base << min(attempt-1, 5)
	jitter := time.Duration(rand.Int64N(int64(50 * time.Millisecond)))
	return delay + jitter
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextPath(next string) string {
	if next == "" {
		return ""
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return ""
	}
	if parsed.Path == "" {
		return next
	}
	if parsed.RawQuery == "" {
		return parsed.Path
	}

	return parsed.Path + "?" + parsed.RawQuery
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}
