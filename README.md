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

Recommended Make targets:

```bash
make help
make init-from-yaml
make artists-resolve-offline
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-apply
make artists-enable-with-ids
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-interactive
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-review-interactive
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-audit-albums ARTIST=wu-tang-clan
make sync-dry-run
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make sync-artist ARTIST=wu-tang-clan
make db-rebuild
make ci
```

`make init-from-yaml` is the local bootstrap path from `data/artists.yaml`: it validates the YAML, migrates SQLite, seeds configured artists and Spotify IDs into the database, refreshes the snapshot, exports JSON, builds Hugo and runs the audit gate. Album and track content still requires a Spotify sync.

Generated site JSON under `site/data/generated` and `site/static/search-index.json` is not committed. Run `make export` locally when serving the site; the Pages workflow exports these files during deployment.

Useful variables:

```bash
make sync-artist ARTIST=gravediggaz MARKET=ES
make artists-resolve-apply REPORT=resolve.md MARKET=ES
make artists-audit-albums ARTIST=wu-tang-clan ALBUM_REPORT=albums-audit.md
make init-from-yaml CATALOG=data/artists.yaml DB=data/catalog.db
```

Equivalent CLI commands:

```bash
go run ./cmd/spotwufamily version
go run ./cmd/spotwufamily artists validate
go run ./cmd/spotwufamily artists import-groups
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily artists resolve --non-interactive --report resolve.md
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily artists resolve --non-interactive --apply --report resolve.md
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily artists resolve
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily artists audit-albums --artist wu-tang-clan --report albums-audit.md
go run ./cmd/spotwufamily artists resolve --non-interactive --candidates data/artist-candidates.example.json --report resolve.md
go run ./cmd/spotwufamily db migrate
go run ./cmd/spotwufamily db verify
go run ./cmd/spotwufamily db snapshot
go run ./cmd/spotwufamily db rebuild
go run ./cmd/spotwufamily sync --dry-run
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily sync --artist wu-tang-clan
go run ./cmd/spotwufamily export
go run ./cmd/spotwufamily site build
go run ./cmd/spotwufamily audit
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

Use `spotify_id` for the primary Spotify profile and `spotify_ids` for additional Spotify profiles that represent the same editorial artist or group.

Run:

```bash
make artists-validate
```

To discover possible new Wu Family entries from Wikipedia without changing the YAML:

```bash
make artists-discover-wu
```

Review `wu-discovery.md`, then apply newly discovered entries as disabled YAML artists:

```bash
make artists-discover-wu-apply
```

Discovery is intentionally conservative: it never removes artists, never enables artists and never assigns Spotify IDs. Follow it with Spotify resolution and interactive review.

Spotify artist metadata can be refreshed into the YAML for artists that already have Spotify IDs:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-refresh-metadata
```

This stores Spotify genres, profile links and artist image URLs. `make init-from-yaml` can then seed that metadata into SQLite and export it to the Hugo site without requiring a full album sync. The resolver also uses stored genres as compatibility evidence when ranking Spotify candidates.

The non-interactive resolver uses Spotify when credentials are present. It also accepts a local JSON candidate file so ranking and report generation can be tested without Spotify credentials or network access:

```bash
make artists-resolve-offline
```

To write strong, unambiguous matches back to `data/artists.yaml` automatically:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-apply
```

By default, `--apply` only writes matches scoring at least `95` with a score gap of at least `10` over the next candidate. It leaves artists disabled; use `--enable-applied` only after deciding that resolved artists should be included in sync.

For manual review with less typing, run interactive mode and pick the candidate number for each artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  make artists-resolve-interactive
```

To audit the whole YAML, including artists that already have a Spotify ID:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  make artists-review-interactive
```

To compare the albums returned by configured Spotify IDs with MusicBrainz album release groups:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  make artists-audit-albums ARTIST=wu-tang-clan
```

The report is advisory. It lists matched albums, MusicBrainz albums missing from Spotify results, and Spotify albums that look suspicious or unmatched.

Live Spotify artist resolution also uses MusicBrainz album evidence to discard Spotify candidates that do not match any MusicBrainz album release group for the editorial artist. Offline candidate files are not filtered this way.

## Sync

`sync` reads enabled artists from `data/artists.yaml`, fetches Spotify artist metadata, albums, singles, compilations, appearances and album tracks, then stores normalized rows in SQLite.

Current catalog entries are disabled until reviewed Spotify IDs are assigned, so this is expected:

```bash
make sync-dry-run
```

Use real sync only after enabling reviewed artists:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make sync-artist ARTIST=wu-tang-clan
```

To populate releases and tracks for one reviewed artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make sync-artist ARTIST=cilvaringz
make export
```

To populate releases and tracks for every enabled artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make sync-all
make export
```

For a full refresh from Spotify into the local site:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make refresh-all-from-spotify
```

## Database

`data/catalog.db` is the versioned SQLite catalog. `data/catalog.snapshot.sql` is the deterministic logical snapshot used for reviewable diffs and rebuild verification.

Run:

```bash
make db-verify
```

## Export

`export` reads SQLite and writes deterministic JSON for Hugo. These files are build artifacts and are ignored by Git:

```bash
make export
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
make site-build
```

## Documentation

- [Architecture](docs/architecture.md)
- [Database](docs/database.md)
- [Spotify](docs/spotify.md)
- [Automation](docs/automation.md)
- [Operations](docs/operations.md)
- [Security](docs/security.md)
- [End-to-end verification](docs/e2e.md)
- [Release readiness](docs/release-readiness.md)
- [Catalog policy](docs/catalog-policy.md)
