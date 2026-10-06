# Security

## Spotify Credentials

- `SPOTIFY_CLIENT_ID` and `SPOTIFY_CLIENT_SECRET` are read only by CLI commands and GitHub Actions.
- Spotify credentials must never be written to `site/`, generated JSON, logs or PR bodies.
- The static site must never call Spotify directly.

## GitHub Automation

- `catalog-sync.yml` writes only catalog artifacts to `automation/catalog-sync-*` branches.
- `catalog-pr-review.yml` checks PR metadata using trusted scripts from `main`, not code from the PR branch.
- Automatic approval is restricted to same-repository PRs targeting `main`, with expected labels and an allowlist of generated paths.
- Code, workflow, migration, script, template, dependency and editorial YAML changes are blocked from automatic approval.
- Use separate sync and review identities when branch protection requires reviews.

## Catalog release backups

- SQLite sidecar files are ignored and should not be committed.
- `data/catalog.release.json` is the reviewable release pointer with asset and complete-content SHA-256 checksums.
- Complete SQLite backups and snapshots inherit repository visibility. Release downloads verify checksums before installing local files.
- `db verify` and `audit` must pass before merging generated database updates.

## Frontend

- Hugo renders static HTML from generated JSON.
- Search uses `site/static/search-index.json` and does not require a backend.
- `site/hugo.yaml` disables unsafe Goldmark rendering.
