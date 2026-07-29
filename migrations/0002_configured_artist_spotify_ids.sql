CREATE TABLE IF NOT EXISTS configured_artist_spotify_ids (
  artist_slug TEXT NOT NULL,
  spotify_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  primary_id INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (artist_slug, spotify_id),
  UNIQUE (spotify_id),
  FOREIGN KEY (artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_configured_artist_spotify_ids_slug ON configured_artist_spotify_ids(artist_slug);
