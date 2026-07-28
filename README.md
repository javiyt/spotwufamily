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

Phase 1 is the project base. It includes the new CLI skeleton, YAML catalog validation, `groups.txt` import support, CI, and architecture documentation. Spotify sync, SQLite, Hugo and GitHub Pages automation are planned next phases.

## Commands

```bash
go run ./cmd/spotwufamily version
go run ./cmd/spotwufamily artists validate
go run ./cmd/spotwufamily artists import-groups
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

## Documentation

- [Architecture](docs/architecture.md)
- [Database](docs/database.md)
- [Spotify](docs/spotify.md)
- [Automation](docs/automation.md)
- [Catalog policy](docs/catalog-policy.md)
