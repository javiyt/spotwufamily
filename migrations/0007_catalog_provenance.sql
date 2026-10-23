-- Fingerprint of the editorial catalog last applied to SQLite (singleton row),
-- so later edits of data/artists.yaml can be diffed against what the database
-- was built from.
CREATE TABLE IF NOT EXISTS catalog_applied (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  digest TEXT NOT NULL,
  artists_json TEXT NOT NULL,
  applied_at TEXT NOT NULL
);

-- Latest completed publication stage (reconcile, sync, snapshot, export,
-- release) with the catalog and database digests it ran against.
CREATE TABLE IF NOT EXISTS stage_checkpoints (
  stage TEXT PRIMARY KEY,
  catalog_digest TEXT NOT NULL,
  db_digest TEXT NOT NULL,
  at TEXT NOT NULL
);
