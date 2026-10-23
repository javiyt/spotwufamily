package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

const (
	gzaID = "6i9pqDLnLT1fWQRDdR3sXe"
	rzaID = "7n2wHs1TKAczGzO7Dd2rGr"
)

const threeArtistCatalogYAML = `
version: 1
profile_sync_policy: allow_unreviewed
artists:
  - slug: gza
    name: GZA
    spotify_id: ` + gzaID + `
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 2
    notes: ""
  - slug: rza
    name: RZA
    spotify_id: ` + rzaID + `
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 3
    notes: ""
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: ` + wuTangID + `
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
`

// seedThreeArtists links wu-tang-clan to a homonym album and gza to a
// homonym track; rza only has its own album and track.
func seedThreeArtists(t *testing.T, cfg MaintenanceActionsConfig) {
	t.Helper()
	require.NoError(t, os.WriteFile(cfg.CatalogPath, []byte(threeArtistCatalogYAML), 0o644))
	migrateDB(t, cfg.DBPath)
	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	_, err = database.DB().Exec(`
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order) VALUES
  ('wu-tang-clan', 'Wu-Tang Clan', '` + wuTangID + `', 'core', 1, 1),
  ('gza', 'GZA', '` + gzaID + `', 'core', 1, 2),
  ('rza', 'RZA', '` + rzaID + `', 'core', 1, 3);
INSERT INTO artists(spotify_id, name) VALUES
  ('` + wuTangID + `', 'Wu-Tang Clan'), ('` + gzaID + `', 'GZA'), ('` + rzaID + `', 'RZA'), ('homonym', 'Homonym');
INSERT INTO albums(spotify_id, name, album_type) VALUES
  ('al-ok', 'Enter the Wu-Tang', 'album'), ('al-bad', 'Wrong Album', 'album'),
  ('al-gza', 'Liquid Swords', 'album'), ('al-rza', 'Bobby Digital', 'album');
INSERT INTO album_artists(album_id, artist_id, position) VALUES
  ('al-ok', '` + wuTangID + `', 0), ('al-bad', 'homonym', 0), ('al-gza', '` + gzaID + `', 0), ('al-rza', '` + rzaID + `', 0);
INSERT INTO tracks(spotify_id, name) VALUES ('tr-gza', 'Shadowboxin'), ('tr-bad', 'Wrong Track'), ('tr-rza', 'Holocaust');
INSERT INTO album_tracks(album_id, track_id, disc_number, track_number) VALUES
  ('al-gza', 'tr-gza', 1, 1), ('al-gza', 'tr-bad', 1, 2), ('al-rza', 'tr-rza', 1, 1);
INSERT INTO track_artists(track_id, artist_id, position) VALUES
  ('tr-gza', '` + gzaID + `', 0), ('tr-bad', 'homonym', 0), ('tr-rza', '` + rzaID + `', 0);
INSERT INTO artist_albums(configured_artist_slug, album_id) VALUES
  ('wu-tang-clan', 'al-ok'), ('wu-tang-clan', 'al-bad'), ('gza', 'al-gza'), ('rza', 'al-rza');
INSERT INTO artist_tracks(configured_artist_slug, track_id) VALUES
  ('gza', 'tr-gza'), ('gza', 'tr-bad'), ('rza', 'tr-rza');`)
	require.NoError(t, err)
}

// artistRows dumps every association row of one configured artist and the
// albums and tracks it links to.
func artistRows(t *testing.T, dbPath, slug string) string {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	var b strings.Builder
	for _, q := range []string{
		`SELECT aa.album_id, aa.active, COALESCE(aa.missing_since, ''), a.name, a.album_type FROM artist_albums aa JOIN albums a ON a.spotify_id = aa.album_id WHERE aa.configured_artist_slug = ? ORDER BY 1`,
		`SELECT at.track_id, at.active, COALESCE(at.missing_since, ''), t.name FROM artist_tracks at JOIN tracks t ON t.spotify_id = at.track_id WHERE at.configured_artist_slug = ? ORDER BY 1`,
	} {
		rows, err := database.DB().Query(q, slug)
		require.NoError(t, err)
		cols, err := rows.Columns()
		require.NoError(t, err)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			fmt.Fprintln(&b, vals...)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return b.String()
}

func activeCount(t *testing.T, dbPath, query string) int {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	var n int
	require.NoError(t, database.DB().QueryRow(query).Scan(&n))
	return n
}

func TestReconcileAllEndToEndLeavesUnrelatedArtistsUntouched(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	seedThreeArtists(t, cfg)
	actions := reconcileActionsByName(cfg)
	before := databaseSnapshot(t, paths.db)
	rzaBefore := artistRows(t, paths.db, "rza")
	require.Contains(t, rzaBefore, "al-rza")

	dry := actions["reconcile-all-dry-run"]
	out, err := runSpec(t, dry, maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status)
	require.Contains(t, out.Result.Report.Rows, []string{"album", "wu-tang-clan", "al-bad Wrong Album", "active (orphan)", "unlinked"})
	require.Contains(t, out.Result.Report.Rows, []string{"track", "gza", "tr-bad Wrong Track", "active (orphan)", "unlinked"})
	for _, r := range out.Result.Report.Rows {
		require.NotEqual(t, "rza", r[1], "a clean artist is not listed")
	}
	require.Contains(t, out.Result.Summary, "1 artist already reconciled")
	require.Equal(t, before, databaseSnapshot(t, paths.db), "a dry run changes nothing")
	require.NoDirExists(t, cfg.BackupDir)

	apply := actions["reconcile-all-apply"]
	pv, err := apply.Preview(context.Background(), maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, "reconcile all", pv.RequiresTyped)
	for _, l := range pv.Scope {
		require.NotContains(t, l.Value, filepath.Dir(paths.db), "previews show names, not paths")
	}
	out, err = runSpec(t, apply, maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	require.NotContains(t, out.Result.Summary, filepath.Dir(paths.db))
	require.ElementsMatch(t, [][]string{
		{"gza", "applied"}, {"wu-tang-clan", "applied"},
	}, [][]string{out.Result.Report.Rows[0][:2], out.Result.Report.Rows[1][:2]})
	require.Len(t, out.Result.Report.Rows, 2)

	require.Equal(t, 0, activeCount(t, paths.db, `SELECT COUNT(*) FROM artist_albums WHERE album_id = 'al-bad' AND active = 1`))
	require.Equal(t, 0, activeCount(t, paths.db, `SELECT COUNT(*) FROM artist_tracks WHERE track_id = 'tr-bad' AND active = 1`))
	require.Equal(t, []string{"al-ok"}, activeAlbums(t, paths.db))
	require.Equal(t, rzaBefore, artistRows(t, paths.db, "rza"), "the unrelated artist's releases and tracks are byte-identical")

	entries, err := os.ReadDir(cfg.BackupDir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "one backup and one manifest for the whole run")

	// Everything is reconciled now: a fresh run is a no-op without a backup.
	out, err = runSpec(t, apply, maintenance.Params{})
	require.NoError(t, err)
	require.Contains(t, out.Result.Summary, "Nothing to reconcile")
	entries, err = os.ReadDir(cfg.BackupDir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestReconcileAllApplyFailureLeavesDatabaseAsBefore(t *testing.T) {
	cfg, paths := newActionsConfig(t, envMap(nil))
	seedThreeArtists(t, cfg)
	// wu-tang-clan is applied after gza; it fails, so gza must roll back too.
	database, err := sqliteadapter.Open(paths.db)
	require.NoError(t, err)
	_, err = database.DB().Exec(`
CREATE TRIGGER fail_wu BEFORE UPDATE ON artist_albums WHEN NEW.configured_artist_slug = 'wu-tang-clan'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, database.RecordApplied(ctx, artists.AppliedCatalog{Digest: "d", AppliedAt: "2026-10-22T10:00:00Z"}))
	require.NoError(t, database.Close())
	before := databaseSnapshot(t, paths.db)

	out, err := runSpec(t, reconcileActionsByName(cfg)["reconcile-all-apply"], maintenance.Params{})
	require.Error(t, err)
	var se *maintenance.SafeError
	require.ErrorAs(t, err, &se)
	require.Equal(t, "reconcile_failed", se.Code)
	require.NotContains(t, se.Message, filepath.Dir(paths.db))
	require.Equal(t, [][]string{
		{"gza", maintenance.OutcomeRolledBack},
		{"wu-tang-clan", maintenance.OutcomeFailed},
	}, [][]string{out.Result.Report.Rows[0][:2], out.Result.Report.Rows[1][:2]})
	for _, r := range out.Result.Report.Rows {
		for _, cell := range r {
			require.NotContains(t, cell, filepath.Dir(paths.db))
			require.NotContains(t, cell, "injected")
		}
	}
	require.Equal(t, string(before), string(databaseSnapshot(t, paths.db)), "a failed run leaves the database as it was")
	require.Equal(t, 1, activeCount(t, paths.db, `SELECT COUNT(*) FROM artist_tracks WHERE track_id = 'tr-bad' AND active = 1`))

	database, err = sqliteadapter.Open(paths.db)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	_, ok, err := database.AppliedCatalog(ctx)
	require.NoError(t, err)
	require.False(t, ok, "a rolled back run reads as unknown")
}
