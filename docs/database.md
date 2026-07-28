# Database

The catalog database lives at `data/catalog.db` and is versioned in Git.

Planned constraints:

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
