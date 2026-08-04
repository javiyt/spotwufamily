package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

func (d *Database) GetCachedArtistAlbums(ctx context.Context, spotifyID string, groups []string) ([]catalog.Release, error) {
	if spotifyID == "" {
		return nil, artists.ErrCacheMiss
	}

	groupSet := map[string]struct{}{}
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group != "" {
			groupSet[group] = struct{}{}
		}
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT DISTINCT
  a.spotify_id,
  a.name,
  a.album_type,
  a.release_date,
  a.release_date_precision,
  a.label,
  a.total_tracks,
  COALESCE(eu.url, '')
FROM albums a
LEFT JOIN artist_albums aa ON aa.album_id = a.spotify_id AND aa.active = 1
LEFT JOIN configured_artist_spotify_ids casi ON casi.artist_slug = aa.configured_artist_slug
LEFT JOIN album_artists ar ON ar.album_id = a.spotify_id
LEFT JOIN external_urls eu ON eu.owner_type = 'album' AND eu.owner_id = a.spotify_id AND eu.provider = 'spotify'
WHERE a.active = 1
  AND (casi.spotify_id = ? OR ar.artist_id = ?)
ORDER BY a.release_date DESC, a.name, a.spotify_id`,
		spotifyID,
		spotifyID,
	)
	if err != nil {
		return nil, fmt.Errorf("read cached Spotify artist albums %s: %w", spotifyID, err)
	}
	defer func() { _ = rows.Close() }()

	var releases []catalog.Release
	for rows.Next() {
		var release catalog.Release
		if err := rows.Scan(
			&release.SpotifyID,
			&release.Name,
			&release.AlbumType,
			&release.ReleaseDate,
			&release.ReleaseDatePrecision,
			&release.Label,
			&release.TotalTracks,
			&release.URL,
		); err != nil {
			return nil, fmt.Errorf("scan cached Spotify artist album: %w", err)
		}
		if len(groupSet) > 0 {
			if _, ok := groupSet[release.AlbumType]; !ok {
				continue
			}
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cached Spotify artist albums: %w", err)
	}
	if len(releases) == 0 {
		return nil, artists.ErrCacheMiss
	}

	return releases, nil
}

func (d *Database) GetCachedAlbumTracks(ctx context.Context, spotifyID string) ([]catalog.Track, error) {
	if spotifyID == "" {
		return nil, artists.ErrCacheMiss
	}

	credits, err := d.cachedTrackArtists(ctx, spotifyID)
	if err != nil {
		return nil, err
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT
  t.spotify_id,
  t.name,
  at.disc_number,
  at.track_number,
  t.duration_ms,
  t.explicit,
  t.isrc,
  t.preview_url,
  COALESCE(eu.url, '')
FROM album_tracks at
JOIN tracks t ON t.spotify_id = at.track_id
LEFT JOIN external_urls eu ON eu.owner_type = 'track' AND eu.owner_id = t.spotify_id AND eu.provider = 'spotify'
WHERE at.album_id = ?
ORDER BY at.disc_number, at.track_number, t.name, t.spotify_id`,
		spotifyID,
	)
	if err != nil {
		return nil, fmt.Errorf("read cached Spotify album tracks %s: %w", spotifyID, err)
	}
	defer func() { _ = rows.Close() }()

	var tracks []catalog.Track
	for rows.Next() {
		var track catalog.Track
		var explicit int
		if err := rows.Scan(
			&track.SpotifyID,
			&track.Name,
			&track.DiscNumber,
			&track.TrackNumber,
			&track.DurationMS,
			&explicit,
			&track.ISRC,
			&track.PreviewURL,
			&track.URL,
		); err != nil {
			return nil, fmt.Errorf("scan cached Spotify album track: %w", err)
		}
		track.Explicit = explicit == 1
		track.Artists = credits[track.SpotifyID]
		tracks = append(tracks, track)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cached Spotify album tracks: %w", err)
	}
	if len(tracks) == 0 {
		return nil, artists.ErrCacheMiss
	}

	return tracks, nil
}

func (d *Database) cachedTrackArtists(ctx context.Context, albumID string) (map[string][]catalog.ArtistCandidate, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
  ta.track_id,
  ar.spotify_id,
  ar.name,
  COALESCE(eu.url, ''),
  COALESCE(img.url, ''),
  COALESCE(ar.popularity, 0),
  COALESCE(ar.followers, 0)
FROM album_tracks at
JOIN track_artists ta ON ta.track_id = at.track_id
JOIN artists ar ON ar.spotify_id = ta.artist_id
LEFT JOIN external_urls eu ON eu.owner_type = 'artist' AND eu.owner_id = ar.spotify_id AND eu.provider = 'spotify'
LEFT JOIN images img ON img.owner_type = 'artist' AND img.owner_id = ar.spotify_id AND img.position = 0
WHERE at.album_id = ?
ORDER BY ta.track_id, ta.position, ar.name`,
		albumID,
	)
	if err != nil {
		return nil, fmt.Errorf("read cached Spotify track artists %s: %w", albumID, err)
	}
	defer func() { _ = rows.Close() }()

	credits := map[string][]catalog.ArtistCandidate{}
	for rows.Next() {
		var trackID string
		var candidate catalog.ArtistCandidate
		if err := rows.Scan(
			&trackID,
			&candidate.SpotifyID,
			&candidate.Name,
			&candidate.URL,
			&candidate.ImageURL,
			&candidate.Popularity,
			&candidate.Followers,
		); err != nil {
			return nil, fmt.Errorf("scan cached Spotify track artist: %w", err)
		}
		credits[trackID] = append(credits[trackID], candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cached Spotify track artists: %w", err)
	}

	return credits, nil
}

var _ artists.SpotifyAlbumCache = (*Database)(nil)
