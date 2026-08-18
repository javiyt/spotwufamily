package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteAudit(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql.gz")
	outputDir := filepath.Join(dir, "site", "data", "generated")
	staticDir := filepath.Join(dir, "site", "static")
	contentDir := filepath.Join(dir, "site", "content", "generated")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"db", "migrate", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())

	stdout.Reset()
	code = cli.Execute([]string{"db", "snapshot", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())

	stdout.Reset()
	code = cli.Execute([]string{
		"audit",
		"--catalog", catalogPath,
		"--db", dbPath,
		"--snapshot", snapshotPath,
		"--output", outputDir,
		"--static", staticDir,
		"--content", contentDir,
		"--skip-site",
		"--skip-git-diff",
	}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "audit: catalog ok")
	require.Contains(t, stdout.String(), "audit: database ok")
	require.Contains(t, stdout.String(), "audit: export ok")
	require.Contains(t, stdout.String(), "audit: ok")
}

func TestExecuteAuditFailsWhenSnapshotIsStale(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"db", "migrate", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.NoError(t, os.WriteFile(snapshotPath, []byte("stale"), 0o644))

	stdout.Reset()
	code = cli.Execute([]string{
		"audit",
		"--catalog", catalogPath,
		"--db", dbPath,
		"--snapshot", snapshotPath,
		"--output", filepath.Join(dir, "generated"),
		"--static", filepath.Join(dir, "static"),
		"--skip-site",
		"--skip-git-diff",
	}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "snapshot is out of date")
}
