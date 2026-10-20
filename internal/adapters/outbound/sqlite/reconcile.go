package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

var _ artists.ReconcileStore = (*Database)(nil)

// ActiveAssociations lists the active album and track associations of a
// configured artist with the Spotify artist IDs credited on each entity and
// the other configured artists that still reference it.
func (d *Database) ActiveAssociations(ctx context.Context, slug string) ([]artists.Association, error) {
	albums, err := d.queryAssociations(ctx, artists.AssociationAlbum, `
SELECT al.spotify_id, al.name,
  COALESCE((
    SELECT group_concat(artist_id, ',') FROM (
      SELECT artist_id FROM album_artists WHERE album_id = al.spotify_id
      UNION
      SELECT ta.artist_id FROM album_tracks atr JOIN track_artists ta ON ta.track_id = atr.track_id WHERE atr.album_id = al.spotify_id
    )
  ), ''),
  COALESCE((
    SELECT group_concat(other.configured_artist_slug, ',') FROM artist_albums other
    WHERE other.album_id = al.spotify_id AND other.active = 1 AND other.configured_artist_slug <> aa.configured_artist_slug
  ), '')
FROM artist_albums aa
JOIN albums al ON al.spotify_id = aa.album_id
WHERE aa.configured_artist_slug = ? AND aa.active = 1`, slug)
	if err != nil {
		return nil, err
	}
	tracks, err := d.queryAssociations(ctx, artists.AssociationTrack, `
SELECT t.spotify_id, t.name,
  COALESCE((SELECT group_concat(artist_id, ',') FROM track_artists WHERE track_id = t.spotify_id), ''),
  COALESCE((
    SELECT group_concat(other.configured_artist_slug, ',') FROM artist_tracks other
    WHERE other.track_id = t.spotify_id AND other.active = 1 AND other.configured_artist_slug <> at.configured_artist_slug
  ), '')
FROM artist_tracks at
JOIN tracks t ON t.spotify_id = at.track_id
WHERE at.configured_artist_slug = ? AND at.active = 1`, slug)
	if err != nil {
		return nil, err
	}

	return append(albums, tracks...), nil
}

func (d *Database) queryAssociations(ctx context.Context, kind artists.AssociationKind, query, slug string) ([]artists.Association, error) {
	rows, err := d.db.QueryContext(ctx, query, slug)
	if err != nil {
		return nil, fmt.Errorf("read %s associations of %s: %w", kind, slug, err)
	}
	defer func() { _ = rows.Close() }()

	var out []artists.Association
	for rows.Next() {
		var a artists.Association
		var credited, shared string
		if err := rows.Scan(&a.SpotifyID, &a.Name, &credited, &shared); err != nil {
			return nil, fmt.Errorf("scan %s association: %w", kind, err)
		}
		a.Kind = kind
		a.CreditedArtistIDs = splitList(credited)
		a.SharedWith = splitList(shared)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s associations of %s: %w", kind, slug, err)
	}

	return out, nil
}

func splitList(raw string) []string {
	if raw == "" {
		return nil
	}
	set := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			set[part] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for part := range set {
		out = append(out, part)
	}
	sort.Strings(out)

	return out
}

// RetireAssociations soft-retires the given associations of one configured
// artist in a single transaction. It never touches albums, tracks or artists.
func (d *Database) RetireAssociations(ctx context.Context, slug string, albumIDs, trackIDs []string, retiredAt string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retire associations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range albumIDs {
		if _, err := tx.ExecContext(ctx, `
UPDATE artist_albums SET active = 0, missing_since = ?
WHERE configured_artist_slug = ? AND album_id = ? AND active = 1`, retiredAt, slug, id); err != nil {
			return fmt.Errorf("retire album association %s: %w", id, err)
		}
	}
	for _, id := range trackIDs {
		if _, err := tx.ExecContext(ctx, `
UPDATE artist_tracks SET active = 0, missing_since = ?
WHERE configured_artist_slug = ? AND track_id = ? AND active = 1`, retiredAt, slug, id); err != nil {
			return fmt.Errorf("retire track association %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// RestoreAssociations reverts a RetireAssociations call identified by retiredAt.
func (d *Database) RestoreAssociations(ctx context.Context, slug, retiredAt string) (int, int, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin restore associations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	albums, err := execChanged(ctx, tx, `
UPDATE artist_albums SET active = 1, missing_since = NULL
WHERE configured_artist_slug = ? AND active = 0 AND missing_since = ?`, slug, retiredAt)
	if err != nil {
		return 0, 0, fmt.Errorf("restore album associations: %w", err)
	}
	tracks, err := execChanged(ctx, tx, `
UPDATE artist_tracks SET active = 1, missing_since = NULL
WHERE configured_artist_slug = ? AND active = 0 AND missing_since = ?`, slug, retiredAt)
	if err != nil {
		return 0, 0, fmt.Errorf("restore track associations: %w", err)
	}

	return albums, tracks, tx.Commit()
}
