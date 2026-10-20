-- Last-synced Spotify identity mapping per configured artist. Written the first
-- time an artist is seeded/synced and after every completed reconciliation, so a
-- later change of data/artists.yaml can be diffed against what SQLite holds.
CREATE TABLE IF NOT EXISTS artist_mapping_snapshots (
  artist_slug TEXT PRIMARY KEY,
  spotify_ids TEXT NOT NULL,
  checksum TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  source TEXT NOT NULL,
  FOREIGN KEY (artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE
);

-- Reconciliation journal, one row per configured artist (the latest). Status
-- 'drifted' means YAML and SQLite disagree, 'awaiting_resync' means orphans were
-- unlinked and the replacement IDs still have to finish syncing, 'completed' is
-- kept as the audit trail (and rollback source) once consistency is restored.
-- Only the first two statuses are reported as pending.
CREATE TABLE IF NOT EXISTS artist_reconciliations (
  artist_slug TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  previous_ids TEXT NOT NULL DEFAULT '[]',
  target_ids TEXT NOT NULL DEFAULT '[]',
  plan_checksum TEXT NOT NULL DEFAULT '',
  resync_fingerprint TEXT NOT NULL DEFAULT '',
  retired_at TEXT NOT NULL DEFAULT '',
  backup_path TEXT NOT NULL DEFAULT '',
  backup_checksum TEXT NOT NULL DEFAULT '',
  snapshot_checksum_before TEXT NOT NULL DEFAULT '',
  invalidated TEXT NOT NULL DEFAULT '',
  detected_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (artist_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_artist_reconciliations_status ON artist_reconciliations(status);
