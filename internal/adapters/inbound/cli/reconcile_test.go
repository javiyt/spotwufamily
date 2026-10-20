package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
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
	require.Contains(t, stderr.String(), "--artist is required")
	stderr.Reset()
	require.Equal(t, 2, cli.Execute([]string{"artists", "reconcile", "--artist", "rza", "--apply", "--restore", "x"}, &stdout, &stderr))
	require.Equal(t, 2, cli.Execute([]string{"artists", "reconcile", "--artist", "rza", "--apply", "--dry-run"}, &stdout, &stderr))
	require.Equal(t, 2, cli.Execute([]string{"artists", "reconcile", "--artist", "rza", "--format", "xml"}, &stdout, &stderr))
}

const rzaID = "34EP7KEpOjXcM2TCat1ISk"

func seedRzaHomonymHistory(t *testing.T, dbPath string) {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	_, err = database.DB().Exec(`
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order) VALUES ('rza', 'RZA', '` + rzaID + `', 'core', 1, 1);
INSERT INTO artists(spotify_id, name) VALUES ('` + rzaID + `', 'RZA'), ('homonym', 'Homonym');
INSERT INTO albums(spotify_id, name, album_type) VALUES ('al-ok', 'Bobby Digital', 'album'), ('al-bad', 'Wrong Album', 'album');
INSERT INTO album_artists(album_id, artist_id, position) VALUES ('al-ok', '` + rzaID + `', 0), ('al-bad', 'homonym', 0);
INSERT INTO artist_albums(configured_artist_slug, album_id) VALUES ('rza', 'al-ok'), ('rza', 'al-bad');`)
	require.NoError(t, err)
}

func TestExecuteArtistsReconcileApplyFlowAndPendingWarnings(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(reviewCatalog), 0o644))
	dbPath := filepath.Join(dir, "catalog.db")
	backupDir := filepath.Join(dir, "backups")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"db", "migrate", "--db", dbPath}, &stdout, &stderr), stderr.String())
	seedRzaHomonymHistory(t, dbPath)
	common := []string{"--artist", "rza", "--catalog", catalogPath, "--db", dbPath}
	run := func(extra ...string) (int, string, string) {
		stdout.Reset()
		stderr.Reset()
		code := cli.Execute(append(append([]string{"artists", "reconcile"}, common...), extra...), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, first, errOut := run("--dry-run", "--format", "json")
	require.Equal(t, 0, code, errOut)
	_, second, _ := run("--format", "json")
	require.Equal(t, first, second, "the JSON impact report is deterministic")
	var report struct {
		PlanChecksum string `json:"plan_checksum"`
		DryRun       bool   `json:"dry_run"`
	}
	require.NoError(t, json.Unmarshal([]byte(first), &report))
	require.True(t, report.DryRun)
	code, markdown, _ := run("--format", "markdown")
	require.Equal(t, 0, code)
	require.Contains(t, markdown, "# Reconciliation impact: rza")
	require.Contains(t, markdown, "`"+report.PlanChecksum+"`")

	code, _, errOut = run("--apply", "--backup-dir", backupDir)
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "--confirm "+report.PlanChecksum)
	require.NoFileExists(t, backupDir)

	code, out, errOut := run("--apply", "--confirm", report.PlanChecksum, "--backup-dir", backupDir)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "backup="+backupDir)
	require.Contains(t, out, "--restore")
	entries, err := os.ReadDir(backupDir)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	code, out, errOut = run("--apply", "--confirm", report.PlanChecksum, "--backup-dir", backupDir)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "already reconciled")
}

func TestExecuteArtistsReconcileStatusAndDBVerifyWarnAboutDrift(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(reviewCatalog), 0o644))
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql.gz")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"artists", "seed-db", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr), stderr.String())
	require.Empty(t, stderr.String(), "a first seed has nothing to reconcile")

	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	_, err = database.DB().Exec(`UPDATE artist_mapping_snapshots SET spotify_ids = '["old-profile"]' WHERE artist_slug = 'rza'`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	stdout.Reset()
	require.Equal(t, 0, cli.Execute([]string{"artists", "reconcile", "--status", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "warning: 1 pending reconciliation(s)")
	require.Contains(t, stdout.String(), "rza drifted previous=old-profile target="+rzaID)

	stdout.Reset()
	require.Equal(t, 0, cli.Execute([]string{"artists", "reconcile", "--status", "--format", "json", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr))
	require.Contains(t, stdout.String(), `"slug": "rza"`)

	// Reseeding records the drift; db verify and snapshot still pass but keep warning.
	stderr.Reset()
	require.Equal(t, 0, cli.Execute([]string{"artists", "seed-db", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stderr.String(), "pending reconciliation")
	require.Equal(t, 0, cli.Execute([]string{"db", "snapshot", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr), stderr.String())
	stderr.Reset()
	require.Equal(t, 0, cli.Execute([]string{"db", "verify", "--db", dbPath, "--snapshot", snapshotPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stderr.String(), "rza drifted")
}
