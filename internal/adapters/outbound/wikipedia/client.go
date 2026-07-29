package wikipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	defaultAPIURL = "https://en.wikipedia.org/w/api.php"
	defaultPage   = "List of Wu-Tang Clan affiliates"
)

type Config struct {
	APIURL     string
	Page       string
	UserAgent  string
	HTTPClient *http.Client
}

type Client struct {
	apiURL     string
	page       string
	userAgent  string
	httpClient *http.Client
}

func NewClient(config Config) *Client {
	apiURL := config.APIURL
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	page := config.Page
	if page == "" {
		page = defaultPage
	}
	userAgent := config.UserAgent
	if userAgent == "" {
		userAgent = "spotwufamily/2-dev (https://github.com/javiyt/spotwufamily)"
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{apiURL: apiURL, page: page, userAgent: userAgent, httpClient: httpClient}
}

func (c *Client) DiscoverWuFamilyArtists(ctx context.Context) ([]artists.DiscoveredArtist, error) {
	sections, err := c.parseSections(ctx)
	if err != nil {
		return nil, err
	}

	discovered := make([]artists.DiscoveredArtist, 0, len(sections))
	currentCategory := catalog.CategoryCollaborator
	currentSection := ""
	for _, section := range sections {
		if section.TocLevel == 1 {
			currentCategory = categoryForWikipediaSection(section.Line)
			currentSection = section.Line
			continue
		}
		if section.TocLevel != 2 || !isArtistSection(currentSection) {
			continue
		}
		name := cleanSectionLine(section.Line)
		if name == "" || shouldSkipArtistHeading(name) {
			continue
		}
		discovered = append(discovered, artists.DiscoveredArtist{
			Name:       name,
			Category:   currentCategory,
			Source:     "Wikipedia",
			SourceURL:  c.pageURL(section.Anchor),
			SourceNote: currentSection,
		})
	}

	return discovered, nil
}

func (c *Client) parseSections(ctx context.Context) ([]parseSection, error) {
	values := url.Values{}
	values.Set("action", "parse")
	values.Set("format", "json")
	values.Set("formatversion", "2")
	values.Set("page", c.page)
	values.Set("prop", "sections")

	endpoint := c.apiURL + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build Wikipedia request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Wikipedia request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("Wikipedia request failed: %s", response.Status)
	}

	var payload parseResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Wikipedia response: %w", err)
	}
	if payload.Error.Code != "" {
		return nil, fmt.Errorf("Wikipedia error %s: %s", payload.Error.Code, payload.Error.Info)
	}
	return payload.Parse.Sections, nil
}

func (c *Client) pageURL(anchor string) string {
	base := "https://en.wikipedia.org/wiki/" + strings.ReplaceAll(url.PathEscape(c.page), "+", "%20")
	if anchor == "" {
		return base
	}
	return base + "#" + anchor
}

type parseResponse struct {
	Error struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
	Parse struct {
		Sections []parseSection `json:"sections"`
	} `json:"parse"`
}

type parseSection struct {
	TocLevel int    `json:"toclevel"`
	Line     string `json:"line"`
	Anchor   string `json:"anchor"`
}

func (s *parseSection) UnmarshalJSON(data []byte) error {
	type rawSection struct {
		TocLevel json.RawMessage `json:"toclevel"`
		Line     string          `json:"line"`
		Anchor   string          `json:"anchor"`
	}
	var raw rawSection
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	level, err := parseFlexibleInt(raw.TocLevel)
	if err != nil {
		return err
	}
	s.TocLevel = level
	s.Line = raw.Line
	s.Anchor = raw.Anchor
	return nil
}

func parseFlexibleInt(data json.RawMessage) (int, error) {
	var number int
	if err := json.Unmarshal(data, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return 0, err
	}
	return strconv.Atoi(text)
}

func categoryForWikipediaSection(section string) catalog.Category {
	switch strings.ToLower(cleanSectionLine(section)) {
	case "groups":
		return catalog.CategoryAffiliateGroup
	case "rappers", "singers":
		return catalog.CategoryAffiliateArtist
	case "producers":
		return catalog.CategoryProducer
	default:
		return catalog.CategoryCollaborator
	}
}

func isArtistSection(section string) bool {
	switch strings.ToLower(cleanSectionLine(section)) {
	case "groups", "rappers", "singers", "producers", "djs":
		return true
	default:
		return false
	}
}

func cleanSectionLine(line string) string {
	line = html.UnescapeString(stripTags(line))
	line = strings.ReplaceAll(line, "\u00a0", " ")
	return strings.TrimSpace(line)
}

var tagPattern = regexp.MustCompile(`<[^>]+>`)

func stripTags(value string) string {
	return tagPattern.ReplaceAllString(value, "")
}

func shouldSkipArtistHeading(name string) bool {
	switch strings.ToLower(name) {
	case "recordings", "references", "external links":
		return true
	default:
		return false
	}
}
