package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

const (
	streetlifeOriginalsID = "0FCYBhXjpfQIQWNKiVEDg5" // jazz homonym, rejected
	streetlifeWuID        = "7CrVv4zLga71gdlMOMfjXL" // verified replacement
	methodManID           = "1YfhSnxIJDXHW6bMk9yzmV"
)

type fixedCatalog struct{ c catalog.EditorialCatalog }

func (f fixedCatalog) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return f.c, nil
}

func (f fixedCatalog) Save(context.Context, string, catalog.EditorialCatalog) error { return nil }

// seedHomonymFixture models a database synced with the wrong Streetlife profile
// and then re-pointed to the Wu-related one:
//   - album "Sidewalk Stories" and its track belong only to the jazz homonym;
//   - album "Hotel Costes 2" is a compilation also referenced by another artist;
//   - album "Street Education" and its track are credited to the Wu profile;
//   - the Method Man album belongs to another configured artist and must never move.
func seedHomonymFixture(t *testing.T, ctx context.Context, dbPath string) (artists.ReconcileStore, func(string) int) {
	t.Helper()
	database := openMigratedDatabase(t, ctx, dbPath)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order) VALUES
  ('streetlife', 'Streetlife', '`+streetlifeWuID+`', 'affiliate_artist', 1, 74),
  ('method-man', 'Method Man', '`+methodManID+`', 'core', 1, 5);
INSERT INTO artists(spotify_id, name) VALUES
  ('`+streetlifeOriginalsID+`', 'Streetlife Originals'),
  ('`+streetlifeWuID+`', 'Streetlife'),
  ('`+methodManID+`', 'Method Man'),
  ('various', 'Various Artists');
INSERT INTO albums(spotify_id, name, album_type) VALUES
  ('al-sidewalk', 'Sidewalk Stories', 'album'),
  ('al-costes', 'Hotel Costes 2 La Suite', 'compilation'),
  ('al-education', 'Street Education', 'album'),
  ('al-tical', 'Tical', 'album');
INSERT INTO tracks(spotify_id, name) VALUES
  ('tr-sidewalk', 'A Sidewalk Story'),
  ('tr-costes', 'Lara''s Theme'),
  ('tr-education', 'Street Education'),
  ('tr-tical', 'Method Man');
INSERT INTO album_artists(album_id, artist_id, position) VALUES
  ('al-sidewalk', '`+streetlifeOriginalsID+`', 0),
  ('al-costes', 'various', 0),
  ('al-education', '`+streetlifeWuID+`', 0),
  ('al-tical', '`+methodManID+`', 0);
INSERT INTO album_tracks(album_id, track_id, disc_number, track_number) VALUES
  ('al-sidewalk', 'tr-sidewalk', 1, 1),
  ('al-costes', 'tr-costes', 1, 1),
  ('al-education', 'tr-education', 1, 1),
  ('al-tical', 'tr-tical', 1, 1);
INSERT INTO track_artists(track_id, artist_id, position) VALUES
  ('tr-sidewalk', '`+streetlifeOriginalsID+`', 0),
  ('tr-costes', '`+streetlifeOriginalsID+`', 0),
  ('tr-education', '`+streetlifeWuID+`', 0),
  ('tr-tical', '`+methodManID+`', 0);
INSERT INTO artist_albums(configured_artist_slug, album_id) VALUES
  ('streetlife', 'al-sidewalk'),
  ('streetlife', 'al-costes'),
  ('streetlife', 'al-education'),
  ('method-man', 'al-costes'),
  ('method-man', 'al-tical');
INSERT INTO artist_tracks(configured_artist_slug, track_id) VALUES
  ('streetlife', 'tr-sidewalk'),
  ('streetlife', 'tr-costes'),
  ('streetlife', 'tr-education'),
  ('method-man', 'tr-tical');`)
	require.NoError(t, err)

	active := func(query string) int {
		var n int
		require.NoError(t, database.DB().QueryRow(query).Scan(&n))
		return n
	}

	return database, active
}

func streetlifeCatalog() fixedCatalog {
	return fixedCatalog{c: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "streetlife", Name: "Streetlife", SpotifyID: streetlifeWuID, Enabled: true},
		{Slug: "method-man", Name: "Method Man", SpotifyID: methodManID, Enabled: true},
	}}}
}

func TestReconcileHomonymDryRunApplyAndRestore(t *testing.T) {
	ctx := context.Background()
	store, count := seedHomonymFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	reconcile := artists.NewReconcileArtist(streetlifeCatalog(), store)

	activeAlbums := `SELECT COUNT(*) FROM artist_albums WHERE active = 1`
	activeTracks := `SELECT COUNT(*) FROM artist_tracks WHERE active = 1`
	require.Equal(t, 5, count(activeAlbums))
	require.Equal(t, 4, count(activeTracks))

	dry, err := reconcile.Run(ctx, artists.ReconcileInput{Slug: "streetlife"})
	require.NoError(t, err)
	require.True(t, dry.DryRun)
	require.Equal(t, artists.ReconcileCounts{Albums: 3, Tracks: 3}, dry.Before)
	require.Equal(t, artists.ReconcileCounts{Albums: 1, Tracks: 1}, dry.Supported)
	require.Equal(t, artists.ReconcileCounts{Albums: 2, Tracks: 2}, dry.Orphan)
	require.Equal(t, artists.ReconcileCounts{Albums: 1}, dry.SharedOrphans, "the compilation stays referenced by Method Man")
	require.Equal(t, artists.ReconcileCounts{Albums: 1, Tracks: 2}, dry.UnreferencedAfter)
	require.Equal(t, artists.ReconcileCounts{Albums: 1, Tracks: 1}, dry.After)
	require.Equal(t, 5, count(activeAlbums), "a dry run must not change anything")

	retiredAt := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	applied, err := reconcile.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, Now: retiredAt})
	require.NoError(t, err)
	require.False(t, applied.DryRun)
	require.Equal(t, "2026-10-20T09:00:00Z", applied.RetiredAt)
	require.Equal(t, 3, count(activeAlbums), "only the two orphan streetlife albums are retired")
	require.Equal(t, 2, count(activeTracks))
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_albums WHERE configured_artist_slug = 'streetlife' AND active = 1`))
	require.Equal(t, 2, count(`SELECT COUNT(*) FROM artist_albums WHERE configured_artist_slug = 'method-man' AND active = 1`), "other artists are untouched")
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM artist_tracks WHERE configured_artist_slug = 'method-man' AND active = 1`))
	for table, want := range map[string]int{"albums": 4, "tracks": 4, "artists": 4, "album_artists": 4, "track_artists": 4} {
		require.Equal(t, want, count(`SELECT COUNT(*) FROM `+table), "%s rows are never deleted", table)
	}

	again, err := reconcile.Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, Now: retiredAt})
	require.NoError(t, err)
	require.Equal(t, artists.ReconcileCounts{}, again.Orphan, "reconciliation is idempotent")
	require.Empty(t, again.RetiredAt)

	restored, err := reconcile.Run(ctx, artists.ReconcileInput{Slug: "streetlife", RestoreRetiredAt: applied.RetiredAt})
	require.NoError(t, err)
	require.Equal(t, &artists.ReconcileCounts{Albums: 2, Tracks: 2}, restored.Restored)
	require.Equal(t, 5, count(activeAlbums), "rollback restores the previous state")
	require.Equal(t, 4, count(activeTracks))
	require.Equal(t, 0, count(`SELECT COUNT(*) FROM artist_albums WHERE missing_since IS NOT NULL`))
}

func TestReconcileQuarantinedArtistRetiresEverything(t *testing.T) {
	ctx := context.Background()
	store, count := seedHomonymFixture(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	quarantined := fixedCatalog{c: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "streetlife", Name: "Streetlife"},
	}}}

	report, err := artists.NewReconcileArtist(quarantined, store).Run(ctx, artists.ReconcileInput{Slug: "streetlife", Apply: true, Now: time.Now()})
	require.NoError(t, err)
	require.Empty(t, report.LinkedIDs)
	require.Equal(t, artists.ReconcileCounts{}, report.After)
	require.Equal(t, 0, count(`SELECT COUNT(*) FROM artist_albums WHERE configured_artist_slug = 'streetlife' AND active = 1`))
	require.Equal(t, 2, count(`SELECT COUNT(*) FROM artist_albums WHERE configured_artist_slug = 'method-man' AND active = 1`))
}
