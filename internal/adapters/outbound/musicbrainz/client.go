package musicbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	defaultBaseURL   = "https://musicbrainz.org"
	defaultTimeout   = 15 * time.Second
	defaultLimit     = 100
	defaultUserAgent = "spotwufamily/2-dev (https://github.com/javiyt/spotwufamily)"
)

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
	Limit      int
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
	limit      int
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

	return &Client{baseURL: baseURL, httpClient: httpClient, userAgent: userAgent, limit: limit}
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MusicBrainz request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode MusicBrainz response: %w", err)
	}

	return nil
}

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
