# Spot Wu Family

Spot Wu Family is being rebuilt as a static music catalog for Wu-Tang Clan and related Wu Family artists, producers and collaborators.

The v2 direction is:

- Go CLI and collector.
- Editorial artist catalog in `data/artists.yaml`.
- Spotify Web API collection through server-side automation only.
- SQLite database versioned in Git.
- Deterministic JSON exports for Hugo.
- Static Hugo site deployed to GitHub Pages.

## Current Status

The current build has the CLI, editorial YAML validation, Spotify sync, SQLite persistence, deterministic exports, a Hugo site, CI, GitHub Pages deployment, and guarded catalog automation.

## Commands

```bash
go run ./cmd/spotwufamily version
go run ./cmd/spotwufamily artists validate
go run ./cmd/spotwufamily artists import-groups
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily artists resolve --non-interactive --report resolve.md
go run ./cmd/spotwufamily artists resolve --non-interactive --candidates candidates.json --report resolve.md
go run ./cmd/spotwufamily db migrate
go run ./cmd/spotwufamily db verify
go run ./cmd/spotwufamily db snapshot
go run ./cmd/spotwufamily db rebuild
go run ./cmd/spotwufamily sync --dry-run
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily sync --artist wu-tang-clan
go run ./cmd/spotwufamily export
go run ./cmd/spotwufamily site build
make ci
```

## Architecture

The project follows a pragmatic hexagonal architecture:

- Domain: catalog rules and validation.
- Application: explicit use cases.
- Ports: defined by application needs.
- Adapters: CLI, YAML, Spotify, SQLite, JSON export and Hugo build orchestration.

See [docs/architecture.md](docs/architecture.md).

## Catalog

`data/groups.txt` is the original prototype list. `data/artists.yaml` is the v2 editorial catalog and keeps entries disabled until Spotify IDs are reviewed.

Run:

```bash
go run ./cmd/spotwufamily artists validate
```

The non-interactive resolver uses Spotify when credentials are present. It also accepts a local JSON candidate file so ranking and report generation can be tested without Spotify credentials or network access.

## Sync

`sync` reads enabled artists from `data/artists.yaml`, fetches Spotify artist metadata, albums, singles, compilations, appearances and album tracks, then stores normalized rows in SQLite.

Current catalog entries are disabled until reviewed Spotify IDs are assigned, so this is expected:

```bash
go run ./cmd/spotwufamily sync --dry-run
```

Use real sync only after enabling reviewed artists:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily sync --artist wu-tang-clan
```

## Database

`data/catalog.db` is the versioned SQLite catalog. `data/catalog.snapshot.sql` is the deterministic logical snapshot used for reviewable diffs and rebuild verification.

Run:

```bash
go run ./cmd/spotwufamily db verify
```

## Export

`export` reads SQLite and writes deterministic JSON for Hugo:

```bash
go run ./cmd/spotwufamily export
```

Outputs:

- `site/data/generated/catalog-summary.json`
- `site/data/generated/artists/index.json`
- `site/data/generated/albums/index.json`
- `site/data/generated/tracks/index.json`
- `site/static/search-index.json`

## Site

The Hugo site lives in `site/` and is configured for GitHub Pages under:

```text
https://javiyt.github.io/spotwufamily/
```

Build it with:

```bash
go run ./cmd/spotwufamily site build
```

## Documentation

- [Architecture](docs/architecture.md)
- [Database](docs/database.md)
- [Spotify](docs/spotify.md)
- [Automation](docs/automation.md)
- [Catalog policy](docs/catalog-policy.md)
