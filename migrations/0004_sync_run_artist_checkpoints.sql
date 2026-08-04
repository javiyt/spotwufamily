CREATE TABLE IF NOT EXISTS sync_run_artists (
  sync_run_id INTEGER NOT NULL,
  artist_slug TEXT NOT NULL,
  spotify_ids TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  finished_at TEXT,
  summary TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (sync_run_id, artist_slug),
  FOREIGN KEY (sync_run_id) REFERENCES sync_runs(id) ON DELETE CASCADE,
  FOREIGN KEY (artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sync_run_artists_status ON sync_run_artists(status);
