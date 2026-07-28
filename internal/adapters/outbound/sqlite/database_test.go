package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func TestMigrateVerifyAndSnapshot(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")

	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, spotify_id, category, enabled, editorial_order)
VALUES ('wu-tang-clan', 'Wu-Tang Clan', '34EP7KEpOjXcM2TCat1ISk', 'core', 1, 1);
INSERT INTO artist_aliases(artist_slug, alias, position)
VALUES ('wu-tang-clan', 'Wu Tang Clan', 1);`)
	require.NoError(t, err)

	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	report, err := database.Verify(ctx, migrations)
	require.NoError(t, err)
	require.Equal(t, 1, report.Migrations)
	require.Contains(t, report.Checks, "integrity_check")
	require.Contains(t, report.Checks, "foreign_key_check")

	snapshot, err := database.Snapshot(ctx)
	require.NoError(t, err)
	snapshotText := string(snapshot)
	require.Contains(t, snapshotText, `DELETE FROM "configured_artists";`)
	require.Contains(t, snapshotText, `'wu-tang-clan'`)
	require.Contains(t, snapshotText, `'Wu Tang Clan'`)
}

func TestSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql")

	database := openMigratedDatabase(t, ctx, dbPath)
	_, err := database.DB().ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, category, enabled, editorial_order)
VALUES ('gravediggaz', 'Gravediggaz', 'affiliate_group', 0, 2);`)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	require.NoError(t, database.Close())

	rebuiltPath := filepath.Join(dir, "rebuilt.db")
	rebuilt := openMigratedDatabase(t, ctx, rebuiltPath)
	defer func() { require.NoError(t, rebuilt.Close()) }()

	require.NoError(t, sqliteadapter.RestoreSnapshot(ctx, rebuilt.DB(), snapshotPath))

	var name string
	err = rebuilt.DB().QueryRowContext(ctx, `SELECT name FROM configured_artists WHERE slug = 'gravediggaz'`).Scan(&name)
	require.NoError(t, err)
	require.Equal(t, "Gravediggaz", name)
}

func TestVerifyFailsWhenMigrationMissing(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")

	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	_, err := database.DB().ExecContext(ctx, `DELETE FROM schema_migrations`)
	require.NoError(t, err)

	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	_, err = database.Verify(ctx, migrations)
	require.ErrorContains(t, err, "is not applied")
}

func TestWriteSnapshotDoesNotRewriteUnchangedFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	snapshotPath := filepath.Join(dir, "catalog.snapshot.sql")

	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	first, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.NoError(t, database.WriteSnapshot(ctx, snapshotPath))
	second, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.True(t, strings.HasPrefix(string(second), "-- Spot Wu Family catalog snapshot"))
}

func TestSaveArtistCatalogPersistsNormalizedCatalog(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	database := openMigratedDatabase(t, ctx, dbPath)
	defer func() { require.NoError(t, database.Close()) }()

	configuredArtist := catalog.Artist{
		Slug:           "wu-tang-clan",
		Name:           "Wu-Tang Clan",
		SpotifyID:      "artist-1",
		Category:       catalog.CategoryCore,
		Roles:          []catalog.Category{catalog.CategoryCore},
		Aliases:        []string{"Wu Tang Clan"},
		Enabled:        true,
		EditorialOrder: 1,
	}
	require.NoError(t, database.SaveConfiguredArtists(ctx, []catalog.Artist{configuredArtist}))
	runID, err := database.BeginSyncRun(ctx, catalogsync.SyncRun{StartedAt: fixedTime(), Market: "ES"})
	require.NoError(t, err)

	stats, err := database.SaveArtistCatalog(
		ctx,
		runID,
		configuredArtist,
		catalog.ArtistCandidate{SpotifyID: "artist-1", Name: "Wu-Tang Clan", URL: "https://artist", ImageURL: "https://artist/image.jpg", Popularity: 90, Followers: 100},
		[]catalog.ReleaseTracks{{
			Release: catalog.Release{
				SpotifyID:            "album-1",
				Name:                 "Album One",
				AlbumType:            "album",
				ReleaseDate:          "1993-11-09",
				ReleaseDatePrecision: "day",
				Label:                "Loud",
				TotalTracks:          1,
				URL:                  "https://album",
				Images:               []catalog.Image{{URL: "https://album/image.jpg", Height: 640, Width: 640}},
				Artists:              []catalog.ArtistCandidate{{SpotifyID: "artist-1", Name: "Wu-Tang Clan"}},
				Copyrights:           []catalog.Copyright{{Text: "1993 Loud", Type: "C"}},
			},
			Tracks: []catalog.Track{{
				SpotifyID:   "track-1",
				Name:        "Track One",
				DiscNumber:  1,
				TrackNumber: 1,
				DurationMS:  120000,
				Explicit:    true,
				ISRC:        "USRC10000001",
				URL:         "https://track",
				Artists:     []catalog.ArtistCandidate{{SpotifyID: "artist-1", Name: "Wu-Tang Clan"}},
			}},
		}},
		fixedTime(),
	)
	require.NoError(t, err)
	require.Equal(t, 1, stats.AlbumsUpserted)
	require.Equal(t, 1, stats.TracksUpserted)
	require.Equal(t, 1, stats.ArtistAlbumsUpserted)
	require.Equal(t, 1, stats.ArtistTracksUpserted)

	requireRowCount(t, database, "configured_artists", 1)
	requireRowCount(t, database, "artist_aliases", 1)
	requireRowCount(t, database, "artists", 1)
	requireRowCount(t, database, "albums", 1)
	requireRowCount(t, database, "tracks", 1)
	requireRowCount(t, database, "album_tracks", 1)
	requireRowCount(t, database, "artist_albums", 1)
	requireRowCount(t, database, "artist_tracks", 1)
	requireRowCount(t, database, "track_artists", 1)
	requireRowCount(t, database, "album_artists", 1)

	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	_, err = database.Verify(ctx, migrations)
	require.NoError(t, err)

	exported, err := database.LoadExportCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, exported.Artists, 1)
	require.Len(t, exported.Albums, 1)
	require.Len(t, exported.Tracks, 1)
	require.Equal(t, "Wu-Tang Clan", exported.Artists[0].Name)
	require.Equal(t, "Album One", exported.Albums[0].Name)
	require.Equal(t, "Track One", exported.Albums[0].Tracks[0].Name)
	require.Equal(t, "Wu-Tang Clan", exported.Tracks[0].Artists[0].Name)
}

func openMigratedDatabase(t *testing.T, ctx context.Context, path string) *sqliteadapter.Database {
	t.Helper()

	database, err := sqliteadapter.Open(path)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(ctx))

	return database
}

func requireRowCount(t *testing.T, database *sqliteadapter.Database, table string, want int) {
	t.Helper()

	var got int
	err := database.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func fixedTime() time.Time {
	return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
}
