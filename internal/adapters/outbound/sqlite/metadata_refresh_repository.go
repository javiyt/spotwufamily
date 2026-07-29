package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (d *Database) LastArtistMetadataRefresh(ctx context.Context, slug string) (time.Time, bool, error) {
	if err := d.withContext(ctx); err != nil {
		return time.Time{}, false, err
	}

	var value string
	err := d.db.QueryRowContext(ctx, `SELECT refreshed_at FROM artist_metadata_refreshes WHERE artist_slug = ?`, slug).Scan(&value)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read artist metadata refresh %s: %w", slug, err)
	}

	refreshedAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse artist metadata refresh %s: %w", slug, err)
	}

	return refreshedAt, true, nil
}

func (d *Database) SaveArtistMetadataRefresh(ctx context.Context, slug string, spotifyIDs []string, refreshedAt time.Time) error {
	if err := d.withContext(ctx); err != nil {
		return err
	}

	_, err := d.db.ExecContext(ctx, `
INSERT INTO artist_metadata_refreshes(artist_slug, refreshed_at, spotify_ids)
VALUES (?, ?, ?)
ON CONFLICT(artist_slug) DO UPDATE SET
  refreshed_at = excluded.refreshed_at,
  spotify_ids = excluded.spotify_ids`,
		slug,
		refreshedAt.UTC().Format(time.RFC3339),
		strings.Join(spotifyIDs, ","),
	)
	if err != nil {
		return fmt.Errorf("save artist metadata refresh %s: %w", slug, err)
	}

	return nil
}
