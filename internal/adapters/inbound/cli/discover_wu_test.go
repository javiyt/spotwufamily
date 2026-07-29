package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestArtistsDiscoverWuApplyAddsNewDisabledArtists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"parse": {
				"sections": [
					{"toclevel": 1, "line": "Groups", "anchor": "Groups"},
					{"toclevel": 2, "line": "Killarmy", "anchor": "Killarmy"},
					{"toclevel": 1, "line": "Singers", "anchor": "Singers"},
					{"toclevel": 2, "line": "Tekitha", "anchor": "Tekitha"}
				]
			}
		}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	reportPath := filepath.Join(dir, "wu-discovery.md")
	err := os.WriteFile(catalogPath, []byte(`version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles:
      - core
    aliases:
      - Wu Tang Clan
    enabled: true
    editorial_order: 1
    notes: ""
  - slug: killarmy
    name: Killarmy
    spotify_id: ""
    category: affiliate_group
    roles:
      - affiliate_group
    aliases: []
    enabled: false
    editorial_order: 2
    notes: ""
`), 0o644)
	require.NoError(t, err)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{
		"artists", "discover-wu",
		"--catalog", catalogPath,
		"--report", reportPath,
		"--wikipedia-api-url", server.URL,
		"--apply",
	}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	require.Contains(t, stdout.String(), "artists discover-wu applied: found=2 existing=1 new=1 added=1")
	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Contains(t, string(report), "Tekitha")
	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "slug: tekitha")
	require.Contains(t, string(updated), "enabled: false")
	require.Contains(t, string(updated), "review before enabling")
}
