# Database

The catalog database lives at `data/catalog.db` and is versioned in Git.

Constraints:

- SQL migrations live in `migrations/`.
- `PRAGMA foreign_keys = ON`.
- `PRAGMA journal_mode = DELETE` keeps Git from seeing persistent WAL sidecar files.
- `PRAGMA synchronous = NORMAL`.
- `PRAGMA optimize` runs after migration/rebuild commands.
- Migration application records deterministic `applied_at` values so logical snapshots do not churn.
- Temporary SQLite files are ignored: `data/catalog.db-wal`, `data/catalog.db-shm`, `data/catalog.db-journal`.
- The logical snapshot is generated at `data/catalog.snapshot.sql` beside the binary database for reviewable pull requests.

Commands:

```bash
go run ./cmd/spotwufamily db migrate
go run ./cmd/spotwufamily db verify
go run ./cmd/spotwufamily db snapshot
go run ./cmd/spotwufamily db rebuild
```

`db verify` currently checks:

- applied migrations and checksums
- `PRAGMA integrity_check`
- `PRAGMA foreign_key_check`
- snapshot freshness when `data/catalog.snapshot.sql` exists

The initial schema includes configured artists, aliases, Spotify artists, albums, tracks, album/track credits, discovery relationships, images, external URLs, copyrights and sync run metadata.

Sync persistence:

- `configured_artists` and `artist_aliases` are populated from `data/artists.yaml`.
- `configured_artist_spotify_ids` stores the primary Spotify ID plus any additional Spotify profile IDs configured for the same editorial artist or group.
- Spotify artist, album and track rows are deduplicated by Spotify ID.
- `artist_albums` and `artist_tracks` record which configured artist discovered a release or track.
- `album_tracks`, `album_artists` and `track_artists` preserve album placement and credited artists.
- The current policy is conservative: sync upserts observed data and does not delete rows simply because they are absent from one run.

Export:

- Reads the normalized SQLite schema.
- Writes deterministic JSON to `site/data/generated`.
- Writes a compact static search index to `site/static/search-index.json`.
- Treats those JSON files as ignored build artifacts; CI and Pages regenerate them from SQLite.
- Does not rewrite unchanged files.
- Removes obsolete generated JSON files under the export directories.
