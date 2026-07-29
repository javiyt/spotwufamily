package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteArtistsSeedDBSeedsConfiguredArtistsFromYAML(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	dbPath := filepath.Join(dir, "catalog.db")
	outputDir := filepath.Join(dir, "site", "data", "generated")
	staticDir := filepath.Join(dir, "site", "static")
	contentDir := filepath.Join(dir, "site", "content", "generated")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: "34EP7KEpOjXcM2TCat1ISk"
    spotify_ids:
      - "0H8YCcvC3MPLKnbDRasGiG"
    category: core
    roles: [core]
    aliases: ["Wu Tang Clan"]
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "seed-db", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "seeded configured artists: artists=1 spotify_ids=2")

	stdout.Reset()
	code = cli.Execute([]string{"export", "--db", dbPath, "--output", outputDir, "--static", staticDir, "--content", contentDir}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())

	artistsIndex, err := os.ReadFile(filepath.Join(outputDir, "artists", "index.json"))
	require.NoError(t, err)
	require.Contains(t, string(artistsIndex), `"slug": "wu-tang-clan"`)
	require.Contains(t, string(artistsIndex), `"34EP7KEpOjXcM2TCat1ISk"`)
	require.Contains(t, string(artistsIndex), `"0H8YCcvC3MPLKnbDRasGiG"`)
	require.FileExists(t, filepath.Join(contentDir, "artists", "wu-tang-clan.md"))

	summary, err := os.ReadFile(filepath.Join(outputDir, "catalog-summary.json"))
	require.NoError(t, err)
	require.Contains(t, string(summary), `"artists": 1`)
	require.Contains(t, string(summary), `"groups": 1`)
}
