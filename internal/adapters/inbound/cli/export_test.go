package cli_test

import (
	"bytes"
	"os"
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

func TestExecuteExportPublishesOnlyVerifiedRelationshipsFromCatalog(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	catalogPath := filepath.Join(dir, "artists.yaml")
	outputDir := filepath.Join(dir, "generated")
	require.NoError(t, os.WriteFile(catalogPath, []byte(relationshipsCatalog), 0o644))

	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"db", "migrate", "--db", dbPath, "--snapshot", filepath.Join(dir, "snapshot.sql")}, &stdout, &stderr))
	require.Equal(t, 0, cli.Execute([]string{"artists", "relationships", "add", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan", "--catalog", catalogPath}, &stdout, &stderr), stderr.String())

	export := []string{"export", "--catalog", catalogPath, "--db", dbPath, "--output", outputDir, "--static", filepath.Join(dir, "static"), "--content", filepath.Join(dir, "content")}
	require.Equal(t, 0, cli.Execute(export, &stdout, &stderr), stderr.String())
	data, err := os.ReadFile(filepath.Join(outputDir, "relationships", "index.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"items": []`, "a proposed claim must not be exported")

	require.Equal(t, 0, cli.Execute([]string{"artists", "relationships", "review", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan",
		"--status", "verified", "--reviewer", "tester", "--confidence", "high", "--evidence-type", "wikipedia",
		"--evidence-url", "https://en.wikipedia.org/wiki/Wu-Tang_Clan", "--evidence-rationale", "Founding member.", "--catalog", catalogPath}, &stdout, &stderr), stderr.String())
	require.Equal(t, 0, cli.Execute(export, &stdout, &stderr), stderr.String())
	data, err = os.ReadFile(filepath.Join(outputDir, "relationships", "index.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"from": "rza"`)

	require.Equal(t, 1, cli.Execute(append(append([]string{}, export[:1]...), "--catalog", filepath.Join(dir, "missing.yaml"), "--db", dbPath), &stdout, &stderr), "an explicit missing catalog is an error")
}
