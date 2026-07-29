package cli_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteExport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	outputDir := filepath.Join(dir, "site", "data", "generated")
	staticDir := filepath.Join(dir, "site", "static")
	contentDir := filepath.Join(dir, "site", "content", "generated")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"db", "migrate", "--db", dbPath, "--snapshot", filepath.Join(dir, "snapshot.sql")}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())

	stdout.Reset()
	code = cli.Execute([]string{"export", "--db", dbPath, "--output", outputDir, "--static", staticDir, "--content", contentDir}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "exported catalog")
	require.FileExists(t, filepath.Join(outputDir, "catalog-summary.json"))
	require.FileExists(t, filepath.Join(outputDir, "artists", "index.json"))
	require.FileExists(t, filepath.Join(staticDir, "search-index.json"))
}
