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

Phase 3 is in progress. The project now has the CLI skeleton, editorial YAML catalog validation, `groups.txt` import support, artist-resolution reports, a `net/http` Spotify client, CI, and architecture documentation. SQLite, Hugo and GitHub Pages automation are planned next phases.

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
make ci
```

## Architecture

The project follows a pragmatic hexagonal architecture:

- Domain: catalog rules and validation.
- Application: explicit use cases.
- Ports: defined by application needs.
- Adapters: CLI, YAML, future Spotify, SQLite and JSON export.

See [docs/architecture.md](docs/architecture.md).

## Catalog

`data/groups.txt` is the original prototype list. `data/artists.yaml` is the v2 editorial catalog and keeps entries disabled until Spotify IDs are reviewed.

Run:

```bash
go run ./cmd/spotwufamily artists validate
```

The non-interactive resolver uses Spotify when credentials are present. It also accepts a local JSON candidate file so ranking and report generation can be tested without Spotify credentials or network access.

## Database

`data/catalog.db` is the versioned SQLite catalog. `data/catalog.snapshot.sql` is the deterministic logical snapshot used for reviewable diffs and rebuild verification.

Run:

```bash
go run ./cmd/spotwufamily db verify
```

## Documentation

- [Architecture](docs/architecture.md)
- [Database](docs/database.md)
- [Spotify](docs/spotify.md)
- [Automation](docs/automation.md)
- [Catalog policy](docs/catalog-policy.md)
