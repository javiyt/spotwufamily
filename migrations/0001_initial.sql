CREATE TABLE IF NOT EXISTS sync_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  status TEXT NOT NULL,
  market TEXT NOT NULL,
  full_sync INTEGER NOT NULL DEFAULT 0,
  summary TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS configured_artists (
  slug TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  public_name TEXT NOT NULL DEFAULT '',
  spotify_id TEXT,
  category TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  editorial_order INTEGER,
  external_url TEXT NOT NULL DEFAULT '',
  added_at TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  active INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT,
  last_seen_at TEXT,
  missing_since TEXT,
  UNIQUE (spotify_id),
  UNIQUE (editorial_order)
);

CREATE TABLE IF NOT EXISTS artist_aliases (
  artist_slug TEXT NOT NULL,
  alias TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY (artist_slug, alias),
  FOREIGN KEY (artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS artists (
  spotify_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  popularity INTEGER,
  followers INTEGER,
  active INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT,
  last_seen_at TEXT,
  missing_since TEXT
);

CREATE TABLE IF NOT EXISTS albums (
  spotify_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  album_type TEXT NOT NULL,
  release_date TEXT NOT NULL DEFAULT '',
  release_date_precision TEXT NOT NULL DEFAULT '',
  label TEXT NOT NULL DEFAULT '',
  total_tracks INTEGER NOT NULL DEFAULT 0,
  active INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT,
  last_seen_at TEXT,
  missing_since TEXT
);

CREATE TABLE IF NOT EXISTS tracks (
  spotify_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  explicit INTEGER NOT NULL DEFAULT 0,
  isrc TEXT NOT NULL DEFAULT '',
  preview_url TEXT NOT NULL DEFAULT '',
  active INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT,
  last_seen_at TEXT,
  missing_since TEXT
);

CREATE TABLE IF NOT EXISTS album_artists (
  album_id TEXT NOT NULL,
  artist_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY (album_id, artist_id),
  FOREIGN KEY (album_id) REFERENCES albums(spotify_id) ON DELETE CASCADE,
  FOREIGN KEY (artist_id) REFERENCES artists(spotify_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS track_artists (
  track_id TEXT NOT NULL,
  artist_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY (track_id, artist_id),
  FOREIGN KEY (track_id) REFERENCES tracks(spotify_id) ON DELETE CASCADE,
  FOREIGN KEY (artist_id) REFERENCES artists(spotify_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS album_tracks (
  album_id TEXT NOT NULL,
  track_id TEXT NOT NULL,
  disc_number INTEGER NOT NULL,
  track_number INTEGER NOT NULL,
  PRIMARY KEY (album_id, track_id),
  FOREIGN KEY (album_id) REFERENCES albums(spotify_id) ON DELETE CASCADE,
  FOREIGN KEY (track_id) REFERENCES tracks(spotify_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS artist_albums (
  configured_artist_slug TEXT NOT NULL,
  album_id TEXT NOT NULL,
  discovered_in_sync_run_id INTEGER,
  first_seen_at TEXT,
  last_seen_at TEXT,
  missing_since TEXT,
  active INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (configured_artist_slug, album_id),
  FOREIGN KEY (configured_artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE,
  FOREIGN KEY (album_id) REFERENCES albums(spotify_id) ON DELETE CASCADE,
  FOREIGN KEY (discovered_in_sync_run_id) REFERENCES sync_runs(id)
);

CREATE TABLE IF NOT EXISTS artist_tracks (
  configured_artist_slug TEXT NOT NULL,
  track_id TEXT NOT NULL,
  discovered_in_sync_run_id INTEGER,
  first_seen_at TEXT,
  last_seen_at TEXT,
  missing_since TEXT,
  active INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (configured_artist_slug, track_id),
  FOREIGN KEY (configured_artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE,
  FOREIGN KEY (track_id) REFERENCES tracks(spotify_id) ON DELETE CASCADE,
  FOREIGN KEY (discovered_in_sync_run_id) REFERENCES sync_runs(id)
);

CREATE TABLE IF NOT EXISTS images (
  owner_type TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  url TEXT NOT NULL,
  height INTEGER,
  width INTEGER,
  position INTEGER NOT NULL,
  PRIMARY KEY (owner_type, owner_id, url)
);

CREATE TABLE IF NOT EXISTS external_urls (
  owner_type TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  url TEXT NOT NULL,
  PRIMARY KEY (owner_type, owner_id, provider)
);

CREATE TABLE IF NOT EXISTS copyrights (
  album_id TEXT NOT NULL,
  text TEXT NOT NULL,
  type TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY (album_id, text, type),
  FOREIGN KEY (album_id) REFERENCES albums(spotify_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_configured_artists_category ON configured_artists(category);
CREATE INDEX IF NOT EXISTS idx_configured_artists_enabled ON configured_artists(enabled);
CREATE INDEX IF NOT EXISTS idx_artist_aliases_alias ON artist_aliases(alias);
CREATE INDEX IF NOT EXISTS idx_albums_release_date ON albums(release_date);
CREATE INDEX IF NOT EXISTS idx_tracks_isrc ON tracks(isrc);
CREATE INDEX IF NOT EXISTS idx_album_tracks_track ON album_tracks(track_id);
CREATE INDEX IF NOT EXISTS idx_artist_albums_album ON artist_albums(album_id);
CREATE INDEX IF NOT EXISTS idx_artist_tracks_track ON artist_tracks(track_id);
