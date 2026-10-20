package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

const wuTangID = "34EP7KEpOjXcM2TCat1ISk"

func reconcileActionsByName(cfg MaintenanceActionsConfig) map[string]maintenance.Action {
	out := map[string]maintenance.Action{}
	for _, a := range NewReconcileActions(cfg) {
		out[a.Name()] = a
	}
	return out
}

// seedHomonymAlbum links wu-tang-clan to one album credited to its profile
// and one credited to a homonym, which the reconciliation unlinks.
func seedHomonymAlbum(t *testing.T, dbPath string) {
	t.Helper()
	migrateDB(t, dbPath)
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	_, err = database.DB().Exec(`
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order) VALUES ('wu-tang-clan', 'Wu-Tang Clan', '` + wuTangID + `', 'core', 1, 1);
INSERT INTO artists(spotify_id, name) VALUES ('` + wuTangID + `', 'Wu-Tang Clan'), ('homonym', 'Homonym');
INSERT INTO albums(spotify_id, name, album_type) VALUES ('al-ok', 'Enter the Wu-Tang', 'album'), ('al-bad', 'Wrong Album', 'album');
INSERT INTO album_artists(album_id, artist_id, position) VALUES ('al-ok', '` + wuTangID + `', 0), ('al-bad', 'homonym', 0);
INSERT INTO artist_albums(configured_artist_slug, album_id) VALUES ('wu-tang-clan', 'al-ok'), ('wu-tang-clan', 'al-bad');`)
	require.NoError(t, err)
}

func activeAlbums(t *testing.T, dbPath string) []string {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	rows, err := database.DB().Query(`SELECT album_id FROM artist_albums WHERE configured_artist_slug = 'wu-tang-clan' AND active = 1 ORDER BY album_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func databaseSnapshot(t *testing.T, dbPath string) []byte {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	snapshot, err := database.Snapshot(context.Background())
	require.NoError(t, err)
	return snapshot
}

func TestReconcileActionsAreRegistered(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	var names []string
	for _, a := range NewReconcileActions(cfg) {
		names = append(names, a.Name())
		ok, reason := a.Available()
		require.False(t, ok)
		require.Equal(t, "No database found; run the init workflow first", reason)
	}
	require.Equal(t, []string{"reconcile-dry-run", "reconcile-apply"}, names)
	require.NoFileExists(t, paths.db, "checking availability never creates the database")
}

func TestReconcileActionsEndToEnd(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	seedHomonymAlbum(t, paths.db)
	actions := reconcileActionsByName(cfg)
	p := maintenance.Params{"artist": "wu-tang-clan"}
	before := databaseSnapshot(t, paths.db)

	dry := actions["reconcile-dry-run"]
	pv, err := dry.Preview(context.Background(), p)
	require.NoError(t, err)
	out, err := runSpec(t, dry, p)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status)
	require.Contains(t, out.Result.Report.Rows, []string{"album", "wu-tang-clan", "al-bad Wrong Album", "active (orphan)", "unlinked"})
	require.Equal(t, before, databaseSnapshot(t, paths.db), "a dry run changes nothing")
	require.NoDirExists(t, cfg.BackupDir)
	require.Equal(t, []string{"al-bad", "al-ok"}, activeAlbums(t, paths.db))

	apply := actions["reconcile-apply"]
	applyPv, err := apply.Preview(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, pv.Fingerprint, applyPv.Fingerprint, "both previews carry the plan checksum")
	require.Equal(t, "reconcile wu-tang-clan", applyPv.RequiresTyped)
	for _, l := range applyPv.Scope {
		require.NotContains(t, l.Value, filepath.Dir(paths.db), "previews show names, not paths")
	}

	stale, err := apply.Spec(context.Background(), p)
	require.NoError(t, err)
	out, err = runSpec(t, apply, p)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	require.Equal(t, []string{"al-ok"}, activeAlbums(t, paths.db))
	require.NotContains(t, out.Result.Summary, filepath.Dir(paths.db))
	entries, err := os.ReadDir(cfg.BackupDir)
	require.NoError(t, err)
	var dbs, manifests int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".manifest.json"):
			manifests++
		case strings.HasSuffix(e.Name(), ".db"):
			dbs++
		}
	}
	require.Equal(t, 1, dbs, "one backup")
	require.Equal(t, 1, manifests, "one manifest")
	require.Len(t, entries, 2)

	// A job planned before the apply is now stale and changes nothing.
	after := databaseSnapshot(t, paths.db)
	_, err = stale.Run(context.Background(), nopReporter{})
	var se *maintenance.SafeError
	require.ErrorAs(t, err, &se)
	require.Equal(t, "stale_preview", se.Code)
	require.Equal(t, after, databaseSnapshot(t, paths.db))

	// A fresh preview of the reconciled artist is a no-op.
	out, err = runSpec(t, apply, p)
	require.NoError(t, err)
	require.Contains(t, out.Result.Summary, "Nothing to apply")
	entries, err = os.ReadDir(cfg.BackupDir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "a no-op takes no backup")
}

func TestReconcileApplyRefusesWhileDatabaseIsLocked(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	seedHomonymAlbum(t, paths.db)
	release, err := filesystem.NewResourceLock(paths.db + ".lock").TryLock()
	require.NoError(t, err)
	defer release()

	_, err = runSpec(t, reconcileActionsByName(cfg)["reconcile-apply"], maintenance.Params{"artist": "wu-tang-clan"})
	var se *maintenance.SafeError
	require.ErrorAs(t, err, &se)
	require.Equal(t, "conflict", se.Code)
	require.Equal(t, []string{"al-bad", "al-ok"}, activeAlbums(t, paths.db))
	require.NoDirExists(t, cfg.BackupDir)
}

func TestReconcileRejectsArtistNotInCatalog(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	migrateDB(t, paths.db)
	a := reconcileActionsByName(cfg)["reconcile-dry-run"]
	p := maintenance.Params{"artist": "cappadonna"}
	require.NoError(t, a.Validate(p))
	_, err := a.Preview(context.Background(), p)
	var se *maintenance.SafeError
	require.ErrorAs(t, err, &se)
	require.Equal(t, "validation", se.Code)
}
