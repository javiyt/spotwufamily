package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteDBLifecycle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql.gz")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"db", "migrate", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "database migrated")

	stdout.Reset()
	code = cli.Execute([]string{"db", "snapshot", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.FileExists(t, snapshotPath)

	stdout.Reset()
	code = cli.Execute([]string{"db", "verify", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "database verified")

	require.NoError(t, os.Remove(dbPath))
	stdout.Reset()
	code = cli.Execute([]string{"db", "rebuild", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.FileExists(t, dbPath)
}
