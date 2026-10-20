package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/stretchr/testify/require"
)

const lockedMessage = "another operation holds the database\n"

func holdDatabaseLock(t *testing.T, dbPath string) {
	t.Helper()
	release, err := filesystem.NewResourceLock(dbPath + ".lock").TryLock()
	require.NoError(t, err)
	t.Cleanup(release)
}

func TestSyncRefusesWhileDatabaseIsLocked(t *testing.T) {
	dir, catalogPath := writeCharacterisationCatalog(t)
	dbPath := filepath.Join(dir, "catalog.db")
	holdDatabaseLock(t, dbPath)
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"sync", "--catalog", catalogPath, "--db", dbPath, "--snapshot", filepath.Join(dir, "s.sql.gz")}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(t, lockedMessage, stderr.String())
	require.NoFileExists(t, dbPath, "the database is not touched while locked")
}

func TestSyncDryRunIgnoresDatabaseLock(t *testing.T) {
	dir, catalogPath := writeCharacterisationCatalog(t)
	holdDatabaseLock(t, filepath.Join(dir, "catalog.db"))
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"sync", "--dry-run", "--catalog", catalogPath, "--db", filepath.Join(dir, "catalog.db")}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
}

func TestReconcileApplyRefusesWhileDatabaseIsLocked(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(reviewCatalog), 0o644))
	dbPath := filepath.Join(dir, "catalog.db")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"db", "migrate", "--db", dbPath}, &stdout, &stderr), stderr.String())
	seedRzaHomonymHistory(t, dbPath)
	holdDatabaseLock(t, dbPath)
	common := []string{"artists", "reconcile", "--artist", "rza", "--catalog", catalogPath, "--db", dbPath}

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, cli.Execute(append(common, "--dry-run"), &stdout, &stderr), "dry runs do not take the lock: %s", stderr.String())
	require.Empty(t, stderr.String())

	stdout.Reset()
	stderr.Reset()
	code := cli.Execute(append(common, "--apply", "--confirm", "whatever", "--backup-dir", filepath.Join(dir, "backups")), &stdout, &stderr)
	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(t, lockedMessage, stderr.String())
	require.NoDirExists(t, filepath.Join(dir, "backups"))

	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	stdout.Reset()
	stderr.Reset()
	code = cli.Execute(append(common, "--restore", "2026-10-20T07:08:07Z"), &stdout, &stderr)
	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(t, lockedMessage, stderr.String())
	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "a refused restore leaves the database untouched")
}

func TestReconcileApplyReleasesDatabaseLock(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(reviewCatalog), 0o644))
	dbPath := filepath.Join(dir, "catalog.db")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"db", "migrate", "--db", dbPath}, &stdout, &stderr), stderr.String())
	seedRzaHomonymHistory(t, dbPath)
	common := []string{"artists", "reconcile", "--artist", "rza", "--catalog", catalogPath, "--db", dbPath}

	stdout.Reset()
	require.Equal(t, 0, cli.Execute(append(common, "--dry-run", "--format", "json"), &stdout, &stderr), stderr.String())
	var report struct {
		PlanChecksum string `json:"plan_checksum"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))

	stderr.Reset()
	code := cli.Execute(append(common, "--apply", "--confirm", report.PlanChecksum, "--backup-dir", filepath.Join(dir, "backups")), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())

	release, err := filesystem.NewResourceLock(dbPath + ".lock").TryLock()
	require.NoError(t, err, "the lock is released once the command returns")
	release()
}
