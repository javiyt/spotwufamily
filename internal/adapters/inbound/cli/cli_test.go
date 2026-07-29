package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{"version"}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "spotwufamily v2-dev")
	require.Empty(t, stderr.String())
}

func TestExecuteArtistsResolveNonInteractive(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")
	reportPath := filepath.Join(dir, "report.md")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0"}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{
		"artists",
		"resolve",
		"--non-interactive",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
		"--report",
		reportPath,
	}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "wrote artist resolution report")

	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Contains(t, string(report), "Gravediggaz")
	require.Contains(t, string(report), "`0CH4f9m2L3TRaA5oErU2p0`")
}

func TestExecuteArtistsResolveApply(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")
	reportPath := filepath.Join(dir, "report.md")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0"}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{
		"artists",
		"resolve",
		"--non-interactive",
		"--apply",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
		"--report",
		reportPath,
	}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "applied resolved Spotify IDs: 1")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 0CH4f9m2L3TRaA5oErU2p0")
	require.Contains(t, string(updated), "enabled: false")
}

func TestExecuteArtistsResolveInteractiveAppliesSelectedCandidate(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0", "popularity": 45, "followers": 1000}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("1\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "select candidate number")
	require.Contains(t, stdout.String(), "https://open.spotify.com/artist/0CH4f9m2L3TRaA5oErU2p0")
	require.Contains(t, stdout.String(), "interactive resolve: applied=1 skipped=0")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 0CH4f9m2L3TRaA5oErU2p0")
	require.Contains(t, string(updated), "enabled: false")
}

func TestExecuteArtistsResolveInteractiveReviewAllCanReplaceExistingID(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: "1111111111111111111111"
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0", "popularity": 45, "followers": 1000}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--review-all",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("1\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "current: 1111111111111111111111")
	require.Contains(t, stdout.String(), "k=keep current")
	require.Contains(t, stdout.String(), "interactive resolve: applied=1 skipped=0 kept=0 cleared=0")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 0CH4f9m2L3TRaA5oErU2p0")
	require.NotContains(t, string(updated), "spotify_id: \"1111111111111111111111\"")
}

func TestExecuteUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{"unknown"}, &stdout, &stderr)

	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "unknown command")
	require.Empty(t, stdout.String())
}
