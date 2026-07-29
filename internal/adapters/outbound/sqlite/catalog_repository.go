package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func (d *Database) SaveConfiguredArtists(ctx context.Context, artists []catalog.Artist) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin configured artists transaction: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	for _, artist := range artists {
		spotifyID := sql.NullString{String: artist.SpotifyID, Valid: artist.SpotifyID != ""}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO configured_artists(slug, name, public_name, spotify_id, category, enabled, editorial_order, external_url, added_at, notes, active)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(slug) DO UPDATE SET
  name = excluded.name,
  public_name = excluded.public_name,
  spotify_id = excluded.spotify_id,
  category = excluded.category,
  enabled = excluded.enabled,
  editorial_order = excluded.editorial_order,
  external_url = excluded.external_url,
  added_at = excluded.added_at,
  notes = excluded.notes,
  active = 1`,
			artist.Slug,
			artist.Name,
			artist.PublicName,
			spotifyID,
			artist.Category,
			boolInt(artist.Enabled),
			nullInt(artist.EditorialOrder),
			artist.ExternalURL,
			artist.AddedAt,
			artist.Notes,
		); err != nil {
			return fmt.Errorf("upsert configured artist %s: %w", artist.Slug, err)
		}

		for index, alias := range artist.Aliases {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO artist_aliases(artist_slug, alias, position)
VALUES (?, ?, ?)
ON CONFLICT(artist_slug, alias) DO UPDATE SET position = excluded.position`,
				artist.Slug,
				alias,
				index,
			); err != nil {
				return fmt.Errorf("upsert alias for %s: %w", artist.Slug, err)
			}
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM configured_artist_spotify_ids WHERE artist_slug = ?`, artist.Slug); err != nil {
			return fmt.Errorf("delete configured Spotify IDs for %s: %w", artist.Slug, err)
		}
		for index, spotifyID := range artist.AllSpotifyIDs() {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO configured_artist_spotify_ids(artist_slug, spotify_id, position, primary_id)
VALUES (?, ?, ?, ?)`,
				artist.Slug,
				spotifyID,
				index,
				boolInt(index == 0 && spotifyID == artist.SpotifyID),
			); err != nil {
				return fmt.Errorf("upsert configured Spotify ID for %s: %w", artist.Slug, err)
			}
		}

		if artist.SpotifyID != "" && artist.ImageURL != "" {
			repository := syncRepository{tx: tx}
			if _, err := repository.upsertImage(ctx, "artist", artist.SpotifyID, catalog.Image{URL: artist.ImageURL}, 0); err != nil {
				return fmt.Errorf("upsert configured artist image for %s: %w", artist.Slug, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit configured artists transaction: %w", err)
	}

	return nil
}

func (d *Database) BeginSyncRun(ctx context.Context, run catalogsync.SyncRun) (int64, error) {
	result, err := d.db.ExecContext(ctx, `
INSERT INTO sync_runs(started_at, status, market, full_sync, summary)
VALUES (?, 'running', ?, ?, '')`,
		run.StartedAt.UTC().Format(time.RFC3339),
		run.Market,
		boolInt(run.Full),
	)
	if err != nil {
		return 0, fmt.Errorf("insert sync run: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read sync run id: %w", err)
	}

	return id, nil
}

func (d *Database) FinishSyncRun(ctx context.Context, runID int64, status string, stats catalogsync.SyncStats) error {
	data, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("marshal sync summary: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `
UPDATE sync_runs
SET finished_at = ?, status = ?, summary = ?
WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339),
		status,
		string(data),
		runID,
	); err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}

	return nil
}

func (d *Database) SaveArtistCatalog(
	ctx context.Context,
	runID int64,
	configuredArtist catalog.Artist,
	spotifyArtist catalog.ArtistCandidate,
	releases []catalog.ReleaseTracks,
	observedAt time.Time,
) (catalogsync.SyncStats, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogsync.SyncStats{}, fmt.Errorf("begin artist catalog transaction: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	repository := syncRepository{tx: tx, observedAt: observedAt.UTC().Format(time.RFC3339)}
	stats := catalogsync.SyncStats{}

	if spotifyArtist.SpotifyID != "" {
		changed, err := repository.upsertSpotifyArtist(ctx, spotifyArtist)
		if err != nil {
			return stats, err
		}
		stats.SpotifyArtistsUpserted += changed
		changed, err = repository.upsertExternalURL(ctx, "artist", spotifyArtist.SpotifyID, "spotify", spotifyArtist.URL)
		if err != nil {
			return stats, err
		}
		stats.ExternalURLsUpserted += changed
		artistImages := spotifyArtist.Images
		if len(artistImages) == 0 && spotifyArtist.ImageURL != "" {
			artistImages = []catalog.Image{{URL: spotifyArtist.ImageURL}}
		}
		for index, image := range artistImages {
			changed, err = repository.upsertImage(ctx, "artist", spotifyArtist.SpotifyID, image, index)
			if err != nil {
				return stats, err
			}
			stats.ImagesUpserted += changed
		}
	}

	for _, item := range releases {
		changed, err := repository.upsertRelease(ctx, item.Release)
		if err != nil {
			return stats, err
		}
		stats.AlbumsUpserted += changed
		changed, err = repository.upsertExternalURL(ctx, "album", item.Release.SpotifyID, "spotify", item.Release.URL)
		if err != nil {
			return stats, err
		}
		stats.ExternalURLsUpserted += changed
		for index, image := range item.Release.Images {
			changed, err := repository.upsertImage(ctx, "album", item.Release.SpotifyID, image, index)
			if err != nil {
				return stats, err
			}
			stats.ImagesUpserted += changed
		}
		for index, copyright := range item.Release.Copyrights {
			changed, err := repository.upsertCopyright(ctx, item.Release.SpotifyID, copyright, index)
			if err != nil {
				return stats, err
			}
			stats.CopyrightsUpserted += changed
		}
		for index, artist := range item.Release.Artists {
			changed, err := repository.upsertSpotifyArtist(ctx, artist)
			if err != nil {
				return stats, err
			}
			stats.SpotifyArtistsUpserted += changed
			changed, err = repository.upsertAlbumArtist(ctx, item.Release.SpotifyID, artist.SpotifyID, index)
			if err != nil {
				return stats, err
			}
			stats.AlbumArtistsUpserted += changed
		}

		changed, err = repository.upsertArtistAlbum(ctx, runID, configuredArtist.Slug, item.Release.SpotifyID)
		if err != nil {
			return stats, err
		}
		stats.ArtistAlbumsUpserted += changed

		for _, track := range item.Tracks {
			changed, err := repository.upsertTrack(ctx, track)
			if err != nil {
				return stats, err
			}
			stats.TracksUpserted += changed
			changed, err = repository.upsertExternalURL(ctx, "track", track.SpotifyID, "spotify", track.URL)
			if err != nil {
				return stats, err
			}
			stats.ExternalURLsUpserted += changed
			changed, err = repository.upsertAlbumTrack(ctx, item.Release.SpotifyID, track)
			if err != nil {
				return stats, err
			}
			stats.AlbumTracksUpserted += changed
			changed, err = repository.upsertArtistTrack(ctx, runID, configuredArtist.Slug, track.SpotifyID)
			if err != nil {
				return stats, err
			}
			stats.ArtistTracksUpserted += changed

			for index, artist := range track.Artists {
				changed, err := repository.upsertSpotifyArtist(ctx, artist)
				if err != nil {
					return stats, err
				}
				stats.SpotifyArtistsUpserted += changed
				changed, err = repository.upsertTrackArtist(ctx, track.SpotifyID, artist.SpotifyID, index)
				if err != nil {
					return stats, err
				}
				stats.TrackArtistsUpserted += changed
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit artist catalog transaction: %w", err)
	}

	return stats, nil
}

type syncRepository struct {
	tx         *sql.Tx
	observedAt string
}

func (r syncRepository) upsertSpotifyArtist(ctx context.Context, artist catalog.ArtistCandidate) (int, error) {
	if artist.SpotifyID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO artists(spotify_id, name, popularity, followers, active, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(spotify_id) DO UPDATE SET
  name = excluded.name,
  popularity = excluded.popularity,
  followers = excluded.followers,
  active = 1,
  last_seen_at = excluded.last_seen_at
WHERE artists.name IS NOT excluded.name
   OR artists.popularity IS NOT excluded.popularity
   OR artists.followers IS NOT excluded.followers
   OR artists.active IS NOT 1`,
		artist.SpotifyID,
		artist.Name,
		nullInt(artist.Popularity),
		nullInt(artist.Followers),
		r.observedAt,
		r.observedAt,
	)
}

func (r syncRepository) upsertRelease(ctx context.Context, release catalog.Release) (int, error) {
	if release.SpotifyID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO albums(spotify_id, name, album_type, release_date, release_date_precision, label, total_tracks, active, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(spotify_id) DO UPDATE SET
  name = excluded.name,
  album_type = excluded.album_type,
  release_date = excluded.release_date,
  release_date_precision = excluded.release_date_precision,
  label = excluded.label,
  total_tracks = excluded.total_tracks,
  active = 1,
  last_seen_at = excluded.last_seen_at
WHERE albums.name IS NOT excluded.name
   OR albums.album_type IS NOT excluded.album_type
   OR albums.release_date IS NOT excluded.release_date
   OR albums.release_date_precision IS NOT excluded.release_date_precision
   OR albums.label IS NOT excluded.label
   OR albums.total_tracks IS NOT excluded.total_tracks
   OR albums.active IS NOT 1`,
		release.SpotifyID,
		release.Name,
		release.AlbumType,
		release.ReleaseDate,
		release.ReleaseDatePrecision,
		release.Label,
		release.TotalTracks,
		r.observedAt,
		r.observedAt,
	)
}

func (r syncRepository) upsertTrack(ctx context.Context, track catalog.Track) (int, error) {
	if track.SpotifyID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO tracks(spotify_id, name, duration_ms, explicit, isrc, preview_url, active, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(spotify_id) DO UPDATE SET
  name = excluded.name,
  duration_ms = excluded.duration_ms,
  explicit = excluded.explicit,
  isrc = excluded.isrc,
  preview_url = excluded.preview_url,
  active = 1,
  last_seen_at = excluded.last_seen_at
WHERE tracks.name IS NOT excluded.name
   OR tracks.duration_ms IS NOT excluded.duration_ms
   OR tracks.explicit IS NOT excluded.explicit
   OR tracks.isrc IS NOT excluded.isrc
   OR tracks.preview_url IS NOT excluded.preview_url
   OR tracks.active IS NOT 1`,
		track.SpotifyID,
		track.Name,
		track.DurationMS,
		boolInt(track.Explicit),
		track.ISRC,
		track.PreviewURL,
		r.observedAt,
		r.observedAt,
	)
}

func (r syncRepository) upsertAlbumArtist(ctx context.Context, albumID, artistID string, position int) (int, error) {
	if albumID == "" || artistID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO album_artists(album_id, artist_id, position)
VALUES (?, ?, ?)
ON CONFLICT(album_id, artist_id) DO UPDATE SET position = excluded.position
WHERE album_artists.position IS NOT excluded.position`,
		albumID,
		artistID,
		position,
	)
}

func (r syncRepository) upsertTrackArtist(ctx context.Context, trackID, artistID string, position int) (int, error) {
	if trackID == "" || artistID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO track_artists(track_id, artist_id, position)
VALUES (?, ?, ?)
ON CONFLICT(track_id, artist_id) DO UPDATE SET position = excluded.position
WHERE track_artists.position IS NOT excluded.position`,
		trackID,
		artistID,
		position,
	)
}

func (r syncRepository) upsertAlbumTrack(ctx context.Context, albumID string, track catalog.Track) (int, error) {
	if albumID == "" || track.SpotifyID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO album_tracks(album_id, track_id, disc_number, track_number)
VALUES (?, ?, ?, ?)
ON CONFLICT(album_id, track_id) DO UPDATE SET
  disc_number = excluded.disc_number,
  track_number = excluded.track_number
WHERE album_tracks.disc_number IS NOT excluded.disc_number
   OR album_tracks.track_number IS NOT excluded.track_number`,
		albumID,
		track.SpotifyID,
		track.DiscNumber,
		track.TrackNumber,
	)
}

func (r syncRepository) upsertArtistAlbum(ctx context.Context, runID int64, slug, albumID string) (int, error) {
	if slug == "" || albumID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO artist_albums(configured_artist_slug, album_id, discovered_in_sync_run_id, first_seen_at, last_seen_at, active)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT(configured_artist_slug, album_id) DO UPDATE SET
  discovered_in_sync_run_id = excluded.discovered_in_sync_run_id,
  active = 1
WHERE artist_albums.active IS NOT 1`,
		slug,
		albumID,
		runID,
		r.observedAt,
		r.observedAt,
	)
}

func (r syncRepository) upsertArtistTrack(ctx context.Context, runID int64, slug, trackID string) (int, error) {
	if slug == "" || trackID == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO artist_tracks(configured_artist_slug, track_id, discovered_in_sync_run_id, first_seen_at, last_seen_at, active)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT(configured_artist_slug, track_id) DO UPDATE SET
  discovered_in_sync_run_id = excluded.discovered_in_sync_run_id,
  active = 1
WHERE artist_tracks.active IS NOT 1`,
		slug,
		trackID,
		runID,
		r.observedAt,
		r.observedAt,
	)
}

func (r syncRepository) upsertImage(ctx context.Context, ownerType, ownerID string, image catalog.Image, position int) (int, error) {
	if ownerType == "" || ownerID == "" || image.URL == "" {
		return 0, nil
	}

	changed, err := execChanged(ctx, r.tx, `
INSERT INTO images(owner_type, owner_id, url, height, width, position)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, url) DO UPDATE SET
  height = excluded.height,
  width = excluded.width,
  position = excluded.position
WHERE images.height IS NOT excluded.height
   OR images.width IS NOT excluded.width
   OR images.position IS NOT excluded.position`,
		ownerType,
		ownerID,
		image.URL,
		nullInt(image.Height),
		nullInt(image.Width),
		position,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert image %s/%s: %w", ownerType, ownerID, err)
	}

	return changed, nil
}

func (r syncRepository) upsertExternalURL(ctx context.Context, ownerType, ownerID, provider, url string) (int, error) {
	if ownerType == "" || ownerID == "" || provider == "" || url == "" {
		return 0, nil
	}

	changed, err := execChanged(ctx, r.tx, `
INSERT INTO external_urls(owner_type, owner_id, provider, url)
VALUES (?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, provider) DO UPDATE SET url = excluded.url
WHERE external_urls.url IS NOT excluded.url`,
		ownerType,
		ownerID,
		provider,
		url,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert external URL %s/%s/%s: %w", ownerType, ownerID, provider, err)
	}

	return changed, nil
}

func (r syncRepository) upsertCopyright(ctx context.Context, albumID string, copyright catalog.Copyright, position int) (int, error) {
	if albumID == "" || copyright.Text == "" {
		return 0, nil
	}

	return execChanged(ctx, r.tx, `
INSERT INTO copyrights(album_id, text, type, position)
VALUES (?, ?, ?, ?)
ON CONFLICT(album_id, text, type) DO UPDATE SET position = excluded.position
WHERE copyrights.position IS NOT excluded.position`,
		albumID,
		copyright.Text,
		copyright.Type,
		position,
	)
}

func execChanged(ctx context.Context, tx *sql.Tx, query string, args ...any) (int, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(affected), nil
}

func rollbackUnlessCommitted(tx *sql.Tx) {
	_ = tx.Rollback()
}

func boolInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func nullInt(value int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(value), Valid: value != 0}
}
