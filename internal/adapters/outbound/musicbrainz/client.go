package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	defaultBaseURL   = "https://musicbrainz.org"
	defaultTimeout   = 15 * time.Second
	defaultLimit     = 100
	defaultUserAgent = "spotwufamily/2-dev (https://github.com/javiyt/spotwufamily)"
	defaultInterval  = time.Second
	defaultRetries   = 3
)

type Config struct {
	BaseURL         string
	HTTPClient      *http.Client
	UserAgent       string
	Limit           int
	RequestInterval time.Duration
	MaxRetries      int
	Sleep           func(context.Context, time.Duration) error
	Now             func() time.Time
}

type Client struct {
	baseURL         string
	httpClient      *http.Client
	userAgent       string
	limit           int
	requestInterval time.Duration
	maxRetries      int
	sleep           func(context.Context, time.Duration) error
	now             func() time.Time

	mu          sync.Mutex
	lastRequest time.Time
}

func NewClient(config Config) *Client {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	userAgent := strings.TrimSpace(config.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	limit := config.Limit
	if limit <= 0 || limit > defaultLimit {
		limit = defaultLimit
	}
	requestInterval := config.RequestInterval
	if requestInterval == 0 {
		requestInterval = defaultInterval
	}
	maxRetries := config.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultRetries
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
		baseURL:         baseURL,
		httpClient:      httpClient,
		userAgent:       userAgent,
		limit:           limit,
		requestInterval: requestInterval,
		maxRetries:      maxRetries,
		sleep:           sleep,
		now:             now,
	}
}

func (c *Client) SearchArtistAlbumReleaseGroups(ctx context.Context, artist catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	values := url.Values{}
	values.Set("fmt", "json")
	values.Set("limit", strconv.Itoa(c.limit))
	values.Set("query", fmt.Sprintf(`artist:"%s" AND primarytype:album`, escapeSearchValue(artist.Name)))

	var releaseGroups []artists.MusicBrainzReleaseGroup
	offset := 0
	total := -1
	for total < 0 || offset < total {
		values.Set("offset", strconv.Itoa(offset))
		var response releaseGroupSearchResponse
		if err := c.getJSON(ctx, "/ws/2/release-group?"+values.Encode(), &response); err != nil {
			return nil, err
		}
		total = response.Count
		for _, item := range response.ReleaseGroups {
			if !strings.EqualFold(item.PrimaryType, "Album") {
				continue
			}
			releaseGroups = append(releaseGroups, artists.MusicBrainzReleaseGroup{
				ID:               item.ID,
				Title:            item.Title,
				FirstReleaseDate: item.FirstReleaseDate,
				URL:              "https://musicbrainz.org/release-group/" + item.ID,
			})
		}
		if len(response.ReleaseGroups) == 0 {
			break
		}
		offset += len(response.ReleaseGroups)
	}

	return releaseGroups, nil
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitForRateLimit(ctx); err != nil {
			return err
		}
		err := c.tryGetJSON(ctx, path, target)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, errTemporary) || attempt == c.maxRetries {
			break
		}
	}

	return lastErr
}

func (c *Client) tryGetJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build MusicBrainz request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("MusicBrainz request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read MusicBrainz response: %w", err)
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("%w: MusicBrainz request failed: status %d: %s", errTemporary, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MusicBrainz request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode MusicBrainz response: %w", err)
	}

	return nil
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	if c.requestInterval <= 0 {
		return nil
	}

	c.mu.Lock()
	now := c.now()
	wait := c.lastRequest.Add(c.requestInterval).Sub(now)
	if wait <= 0 || c.lastRequest.IsZero() {
		c.lastRequest = now
		c.mu.Unlock()
		return nil
	}
	c.lastRequest = now.Add(wait)
	c.mu.Unlock()

	return c.sleep(ctx, wait)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var errTemporary = errors.New("temporary MusicBrainz error")

type releaseGroupSearchResponse struct {
	Count         int                  `json:"count"`
	ReleaseGroups []releaseGroupObject `json:"release-groups"`
}

type releaseGroupObject struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	FirstReleaseDate string `json:"first-release-date"`
	PrimaryType      string `json:"primary-type"`
}

func escapeSearchValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
