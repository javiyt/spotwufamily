package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
)

func (d *Database) LoadExportCatalog(ctx context.Context) (catalogexport.Catalog, error) {
	artists, err := d.exportArtists(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	albums, err := d.exportAlbums(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	tracks, err := d.exportTracks(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}

	albumArtists, err := d.albumCredits(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	albumRelatedArtists, err := d.albumRelatedArtists(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	trackArtists, err := d.trackCredits(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	albumTracks, err := d.albumTracks(ctx, trackArtists)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	trackAlbums, err := d.trackAlbums(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}
	copyrights, err := d.albumCopyrights(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}

	for index := range albums {
		albums[index].Artists = albumArtists[albums[index].SpotifyID]
		albums[index].Related = albumRelatedArtists[albums[index].SpotifyID]
		albums[index].Tracks = albumTracks[albums[index].SpotifyID]
		albums[index].Copyrights = copyrights[albums[index].SpotifyID]
	}
	for index := range tracks {
		tracks[index].Artists = trackArtists[tracks[index].SpotifyID]
		tracks[index].Albums = trackAlbums[tracks[index].SpotifyID]
	}

	stats, err := d.exportStats(ctx)
	if err != nil {
		return catalogexport.Catalog{}, err
	}

	return catalogexport.Catalog{Artists: artists, Albums: albums, Tracks: tracks, Stats: stats}, nil
}

func (d *Database) exportArtists(ctx context.Context) ([]catalogexport.Artist, error) {
	aliases, err := d.artistAliases(ctx)
	if err != nil {
		return nil, err
	}
	spotifyIDs, err := d.configuredArtistSpotifyIDs(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT
  ca.slug,
  ca.name,
  ca.public_name,
  COALESCE(ca.spotify_id, ''),
  ca.category,
  ca.enabled,
  COALESCE(ca.editorial_order, 0),
  COALESCE(eu.url, ''),
  COALESCE(img.url, ''),
  (
    SELECT COUNT(DISTINCT album_id)
    FROM (
      SELECT aa.album_id
      FROM artist_albums aa
      WHERE aa.configured_artist_slug = ca.slug AND aa.active = 1
      UNION
      SELECT aa2.album_id
      FROM album_artists aa2
      JOIN configured_artist_spotify_ids casi ON casi.spotify_id = aa2.artist_id
      JOIN albums al ON al.spotify_id = aa2.album_id AND al.active = 1
      WHERE casi.artist_slug = ca.slug
      UNION
      SELECT at2.album_id
      FROM album_tracks at2
      JOIN track_artists ta ON ta.track_id = at2.track_id
      JOIN configured_artist_spotify_ids casi ON casi.spotify_id = ta.artist_id
      JOIN albums al ON al.spotify_id = at2.album_id AND al.active = 1
      WHERE casi.artist_slug = ca.slug
    )
  ),
  (
    SELECT COUNT(DISTINCT track_id)
    FROM (
      SELECT at.track_id
      FROM artist_tracks at
      WHERE at.configured_artist_slug = ca.slug AND at.active = 1
      UNION
      SELECT ta.track_id
      FROM track_artists ta
      JOIN configured_artist_spotify_ids casi ON casi.spotify_id = ta.artist_id
      JOIN tracks t ON t.spotify_id = ta.track_id AND t.active = 1
      WHERE casi.artist_slug = ca.slug
    )
  )
FROM configured_artists ca
LEFT JOIN external_urls eu ON eu.owner_type = 'artist' AND eu.owner_id = ca.spotify_id AND eu.provider = 'spotify'
LEFT JOIN images img ON img.owner_type = 'artist' AND img.owner_id = ca.spotify_id AND img.position = 0
GROUP BY ca.slug
ORDER BY COALESCE(ca.editorial_order, 999999), ca.name`)
	if err != nil {
		return nil, fmt.Errorf("export artists: %w", err)
	}
	defer func() { _ = rows.Close() }()

	artists := []catalogexport.Artist{}
	for rows.Next() {
		var artist catalogexport.Artist
		var enabled int
		if err := rows.Scan(
			&artist.Slug,
			&artist.Name,
			&artist.PublicName,
			&artist.SpotifyID,
			&artist.Category,
			&enabled,
			&artist.EditorialRank,
			&artist.SpotifyURL,
			&artist.ImageURL,
			&artist.ReleaseCount,
			&artist.TrackCount,
		); err != nil {
			return nil, fmt.Errorf("scan export artist: %w", err)
		}
		artist.Enabled = enabled == 1
		artist.Aliases = aliases[artist.Slug]
		artist.SpotifyIDs = spotifyIDs[artist.Slug]
		if artist.SpotifyURL == "" && artist.SpotifyID != "" {
			artist.SpotifyURL = spotifyArtistURL(artist.SpotifyID)
		}
		artists = append(artists, artist)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export artists: %w", err)
	}

	return artists, nil
}

func spotifyArtistURL(spotifyID string) string {
	if spotifyID == "" {
		return ""
	}
	return "https://open.spotify.com/artist/" + spotifyID
}

func (d *Database) configuredArtistSpotifyIDs(ctx context.Context) (map[string][]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT artist_slug, spotify_id FROM configured_artist_spotify_ids ORDER BY artist_slug, position, spotify_id`)
	if err != nil {
		return nil, fmt.Errorf("export configured artist Spotify IDs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := map[string][]string{}
	for rows.Next() {
		var slug string
		var spotifyID string
		if err := rows.Scan(&slug, &spotifyID); err != nil {
			return nil, fmt.Errorf("scan configured artist Spotify ID: %w", err)
		}
		result[slug] = append(result[slug], spotifyID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate configured artist Spotify IDs: %w", err)
	}

	return result, nil
}

func (d *Database) exportAlbums(ctx context.Context) ([]catalogexport.Album, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
  a.spotify_id,
  a.name,
  a.album_type,
  a.release_date,
  a.label,
  a.total_tracks,
  COALESCE(eu.url, ''),
  COALESCE(img.url, '')
FROM albums a
LEFT JOIN external_urls eu ON eu.owner_type = 'album' AND eu.owner_id = a.spotify_id AND eu.provider = 'spotify'
LEFT JOIN images img ON img.owner_type = 'album' AND img.owner_id = a.spotify_id AND img.position = 0
ORDER BY a.release_date DESC, a.name, a.spotify_id`)
	if err != nil {
		return nil, fmt.Errorf("export albums: %w", err)
	}
	defer func() { _ = rows.Close() }()

	albums := []catalogexport.Album{}
	for rows.Next() {
		var album catalogexport.Album
		if err := rows.Scan(
			&album.SpotifyID,
			&album.Name,
			&album.AlbumType,
			&album.ReleaseDate,
			&album.Label,
			&album.TotalTracks,
			&album.SpotifyURL,
			&album.ImageURL,
		); err != nil {
			return nil, fmt.Errorf("scan export album: %w", err)
		}
		albums = append(albums, album)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export albums: %w", err)
	}

	return albums, nil
}

func (d *Database) exportTracks(ctx context.Context) ([]catalogexport.Track, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
  t.spotify_id,
  t.name,
  t.duration_ms,
  t.explicit,
  t.isrc,
  t.preview_url,
  COALESCE(eu.url, '')
FROM tracks t
LEFT JOIN external_urls eu ON eu.owner_type = 'track' AND eu.owner_id = t.spotify_id AND eu.provider = 'spotify'
ORDER BY t.name, t.spotify_id`)
	if err != nil {
		return nil, fmt.Errorf("export tracks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	tracks := []catalogexport.Track{}
	for rows.Next() {
		var track catalogexport.Track
		var explicit int
		if err := rows.Scan(
			&track.SpotifyID,
			&track.Name,
			&track.DurationMS,
			&explicit,
			&track.ISRC,
			&track.PreviewURL,
			&track.SpotifyURL,
		); err != nil {
			return nil, fmt.Errorf("scan export track: %w", err)
		}
		track.Explicit = explicit == 1
		tracks = append(tracks, track)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export tracks: %w", err)
	}

	return tracks, nil
}

func (d *Database) artistAliases(ctx context.Context) (map[string][]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT artist_slug, alias FROM artist_aliases ORDER BY artist_slug, position, alias`)
	if err != nil {
		return nil, fmt.Errorf("export artist aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	aliases := map[string][]string{}
	for rows.Next() {
		var slug string
		var alias string
		if err := rows.Scan(&slug, &alias); err != nil {
			return nil, fmt.Errorf("scan artist alias: %w", err)
		}
		aliases[slug] = append(aliases[slug], alias)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artist aliases: %w", err)
	}

	return aliases, nil
}

func (d *Database) albumCredits(ctx context.Context) (map[string][]catalogexport.Credit, error) {
	return credits(ctx, d.db, `
SELECT aa.album_id, ar.spotify_id, ar.name
FROM album_artists aa
JOIN artists ar ON ar.spotify_id = aa.artist_id
ORDER BY aa.album_id, aa.position, ar.name`)
}

func (d *Database) trackCredits(ctx context.Context) (map[string][]catalogexport.Credit, error) {
	return credits(ctx, d.db, `
SELECT ta.track_id, ar.spotify_id, ar.name
FROM track_artists ta
JOIN artists ar ON ar.spotify_id = ta.artist_id
ORDER BY ta.track_id, ta.position, ar.name`)
}

func (d *Database) trackAlbums(ctx context.Context) (map[string][]catalogexport.Credit, error) {
	return credits(ctx, d.db, `
SELECT at.track_id, al.spotify_id, al.name
FROM album_tracks at
JOIN albums al ON al.spotify_id = at.album_id
ORDER BY at.track_id, al.release_date, at.disc_number, at.track_number`)
}

func (d *Database) albumRelatedArtists(ctx context.Context) (map[string][]catalogexport.GroupCredit, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT DISTINCT album_id, slug, spotify_id, name, category, editorial_order
FROM (
  SELECT aa.album_id, ca.slug, COALESCE(ca.spotify_id, '') AS spotify_id, ca.name, ca.category, COALESCE(ca.editorial_order, 999999) AS editorial_order
  FROM artist_albums aa
  JOIN configured_artists ca ON ca.slug = aa.configured_artist_slug
  WHERE aa.active = 1 AND ca.active = 1

  UNION

  SELECT aa.album_id, ca.slug, COALESCE(ca.spotify_id, '') AS spotify_id, ca.name, ca.category, COALESCE(ca.editorial_order, 999999) AS editorial_order
  FROM album_artists aa
  JOIN configured_artist_spotify_ids casi ON casi.spotify_id = aa.artist_id
  JOIN configured_artists ca ON ca.slug = casi.artist_slug
  JOIN albums al ON al.spotify_id = aa.album_id
  WHERE ca.active = 1 AND al.active = 1

  UNION

  SELECT at.album_id, ca.slug, COALESCE(ca.spotify_id, '') AS spotify_id, ca.name, ca.category, COALESCE(ca.editorial_order, 999999) AS editorial_order
  FROM album_tracks at
  JOIN track_artists ta ON ta.track_id = at.track_id
  JOIN configured_artist_spotify_ids casi ON casi.spotify_id = ta.artist_id
  JOIN configured_artists ca ON ca.slug = casi.artist_slug
  JOIN albums al ON al.spotify_id = at.album_id
  WHERE ca.active = 1 AND al.active = 1
)
ORDER BY album_id, editorial_order, name`)
	if err != nil {
		return nil, fmt.Errorf("export album related artists: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := map[string][]catalogexport.GroupCredit{}
	for rows.Next() {
		var albumID string
		var artist catalogexport.GroupCredit
		var editorialOrder int
		if err := rows.Scan(&albumID, &artist.Slug, &artist.SpotifyID, &artist.Name, &artist.Category, &editorialOrder); err != nil {
			return nil, fmt.Errorf("scan album related artist: %w", err)
		}
		result[albumID] = append(result[albumID], artist)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate album related artists: %w", err)
	}

	return result, nil
}

func (d *Database) albumTracks(ctx context.Context, trackCredits map[string][]catalogexport.Credit) (map[string][]catalogexport.AlbumTrack, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
  at.album_id,
  t.spotify_id,
  t.name,
  at.disc_number,
  at.track_number,
  t.duration_ms,
  t.explicit
FROM album_tracks at
JOIN tracks t ON t.spotify_id = at.track_id
ORDER BY at.album_id, at.disc_number, at.track_number, t.name`)
	if err != nil {
		return nil, fmt.Errorf("export album tracks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	albums := map[string][]catalogexport.AlbumTrack{}
	for rows.Next() {
		var albumID string
		var track catalogexport.AlbumTrack
		var explicit int
		if err := rows.Scan(
			&albumID,
			&track.SpotifyID,
			&track.Name,
			&track.DiscNumber,
			&track.TrackNumber,
			&track.DurationMS,
			&explicit,
		); err != nil {
			return nil, fmt.Errorf("scan album track: %w", err)
		}
		track.Explicit = explicit == 1
		track.Artists = trackCredits[track.SpotifyID]
		albums[albumID] = append(albums[albumID], track)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate album tracks: %w", err)
	}

	return albums, nil
}

func (d *Database) albumCopyrights(ctx context.Context) (map[string][]catalogexport.Copyright, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT album_id, text, type FROM copyrights ORDER BY album_id, position, text`)
	if err != nil {
		return nil, fmt.Errorf("export copyrights: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := map[string][]catalogexport.Copyright{}
	for rows.Next() {
		var albumID string
		var item catalogexport.Copyright
		if err := rows.Scan(&albumID, &item.Text, &item.Type); err != nil {
			return nil, fmt.Errorf("scan copyright: %w", err)
		}
		items[albumID] = append(items[albumID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate copyrights: %w", err)
	}

	return items, nil
}

func (d *Database) exportStats(ctx context.Context) (catalogexport.Stats, error) {
	var stats catalogexport.Stats
	queries := []struct {
		target *int
		sql    string
	}{
		{&stats.Artists, `SELECT COUNT(*) FROM configured_artists WHERE active = 1`},
		{&stats.Groups, `SELECT COUNT(*) FROM configured_artists WHERE active = 1 AND category IN ('core', 'affiliate_group')`},
		{&stats.Albums, `SELECT COUNT(*) FROM albums WHERE active = 1`},
		{&stats.Tracks, `SELECT COUNT(*) FROM tracks WHERE active = 1`},
		{&stats.Appearances, `SELECT COUNT(*) FROM albums WHERE active = 1 AND album_type = 'appears_on'`},
		{&stats.LastSyncRunID, `SELECT COALESCE(MAX(id), 0) FROM sync_runs WHERE status IN ('success', 'partial')`},
	}
	for _, query := range queries {
		if err := d.db.QueryRowContext(ctx, query.sql).Scan(query.target); err != nil {
			return stats, fmt.Errorf("export stat: %w", err)
		}
	}

	return stats, nil
}

func credits(ctx context.Context, db *sql.DB, query string) (map[string][]catalogexport.Credit, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("export credits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := map[string][]catalogexport.Credit{}
	for rows.Next() {
		var ownerID string
		var credit catalogexport.Credit
		if err := rows.Scan(&ownerID, &credit.SpotifyID, &credit.Name); err != nil {
			return nil, fmt.Errorf("scan credit: %w", err)
		}
		result[ownerID] = append(result[ownerID], credit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credits: %w", err)
	}

	return result, nil
}
