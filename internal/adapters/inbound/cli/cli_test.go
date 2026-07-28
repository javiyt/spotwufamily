package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestExecuteUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{"unknown"}, &stdout, &stderr)

	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "unknown command")
	require.Empty(t, stdout.String())
}
