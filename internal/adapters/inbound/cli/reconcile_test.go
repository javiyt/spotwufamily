package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteArtistsReconcileDryRunAndUsage(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(reviewCatalog), 0o644))
	dbPath := filepath.Join(dir, "catalog.db")

	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"db", "migrate", "--db", dbPath}, &stdout, &stderr), stderr.String())

	stdout.Reset()
	code := cli.Execute([]string{"artists", "reconcile", "--slug", "rza", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.Contains(t, stdout.String(), "artists reconcile dry-run: slug=rza")
	require.Contains(t, stdout.String(), "nothing changed")

	stderr.Reset()
	require.Equal(t, 2, cli.Execute([]string{"artists", "reconcile", "--catalog", catalogPath}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "--slug is required")
	stderr.Reset()
	require.Equal(t, 2, cli.Execute([]string{"artists", "reconcile", "--slug", "rza", "--apply", "--restore", "x"}, &stdout, &stderr))
}
