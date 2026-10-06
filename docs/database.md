# Database

The working catalog lives at `data/catalog.db`. Complete backups are compressed and stored in GitHub Releases; `data/catalog.release.json` pins the release tag, repository, asset sizes, SHA-256 checksums and catalog content checksum. The database and logical snapshot are ignored by Git.

Constraints:

- SQL migrations live in `migrations/`.
- `PRAGMA foreign_keys = ON`.
- `PRAGMA journal_mode = DELETE` avoids persistent WAL sidecar files.
- `PRAGMA synchronous = NORMAL`.
- `PRAGMA optimize` runs after migration/rebuild commands.
- Migration application records deterministic `applied_at` values so logical snapshots do not churn.
- Temporary SQLite files are ignored: `data/catalog.db-wal`, `data/catalog.db-shm`, `data/catalog.db-journal`.
- The logical snapshot is generated at `data/catalog.snapshot.sql.gz` beside the binary database for rebuild verification. Both are release assets; catalog PRs change only the manifest.

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
- snapshot freshness when `data/catalog.snapshot.sql.gz` exists

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

## Release backups and restoration

Requires Python 3 (standard library only), Go and GitHub CLI. Authenticate with `gh auth login`; private repositories require read access for downloads. Releases have the same visibility as the repository.

After cloning:

```bash
make catalog-fetch
make db-verify
make export
```

The download uses the tag pinned in Git, never the latest release. Asset size/checksum, complete catalog content, SQLite integrity and foreign keys are checked before local files are replaced. Existing local files are protected by default. To deliberately replace them, first save your local changes and close SQLite clients, then run:

```bash
python3 scripts/automation/catalog-release.py fetch --force
make db-verify
```

Create a backup from the current local database:

```bash
make catalog-release-prepare RELEASE_PACKAGE=build/catalog-backup-YYYYMMDD-HHMMSS
make catalog-release-publish RELEASE_PACKAGE=build/catalog-backup-YYYYMMDD-HHMMSS
git add data/catalog.release.json
```

Preparation uses SQLite's backup API, including committed WAL data and every table. It generates and verifies a fresh logical snapshot against that copy, then compresses the database. The logical snapshot omits `artist_metadata_refreshes`; restoring the complete database preserves that table too. Packages never overwrite an existing directory. Publishing creates a new dated release without replacing older assets, and updates the local manifest only after upload succeeds. An unchanged complete content hash skips publication. Catalog releases do not become the latest software release.

Commit the updated manifest through the normal PR flow. The release tag targets remote `main`; the manifest, rather than the tag's source archive, identifies the database version. Retain releases referenced by Git history or open PRs. To roll back, restore an older manifest from Git and fetch it. Keep a second copy of release assets on separate storage for an independent backup.

CI, production Pages and PR previews fetch the manifest's release before verification/export. Scheduled sync also fetches first, then publishes a backup and proposes a manifest-only PR. Uploading a backup alone does not deploy it; merging the manifest changes the production version.

## Initial migration from Git

Publish and test the initial release before removing the tracked database and snapshot. `git rm --cached data/catalog.db data/catalog.snapshot.sql.gz` stops tracking them while preserving local files. Old commits retain their existing binaries; this migration does not rewrite shared Git history or reduce the size of old clones. Cleaning that history is a separate coordinated operation.
