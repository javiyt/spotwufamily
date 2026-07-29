CREATE TABLE IF NOT EXISTS artist_metadata_refreshes (
  artist_slug TEXT PRIMARY KEY,
  refreshed_at TEXT NOT NULL,
  spotify_ids TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_artist_metadata_refreshes_refreshed_at ON artist_metadata_refreshes(refreshed_at);
