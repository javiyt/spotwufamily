# Release Readiness

This is the acceptance checklist for the v2 rebuild.

| # | Criterion | Status | Verification |
|---|---|---|---|
| 1 | `data/artists.yaml` is the editorial catalog source. | Done | `artists validate` |
| 2 | Artists can carry stable Spotify IDs. | Done | `data/artists.yaml` validation |
| 3 | Spotify sync follows pagination. | Done | Spotify client tests and sync fetcher |
| 4 | Albums, singles, compilations and appearances are requested. | Done | `sync` include groups |
| 5 | Tracks and credited artists are collected. | Done | sync and SQLite tests |
| 6 | Data is normalized in SQLite. | Done | migrations and repository tests |
| 7 | SQLite database is versioned in Git. | Done | `data/catalog.db` |
| 8 | Logical snapshot is reviewable. | Done | `data/catalog.snapshot.sql.gz` |
| 9 | A second unchanged sync leaves Git clean. | Needs live Spotify run | `sync`, then `audit` |
| 10 | Generated JSON is deterministic. | Done | export tests and `audit` |
| 11 | Hugo builds the web. | Done | `site build`, `make ci` |
| 12 | GitHub Pages subpath works. | Done | `site/hugo.yaml`, Pages workflow |
| 13 | Artist, album and track pages exist. | Done | Hugo layouts and generated indexes |
| 14 | Search works without backend. | Done | static search index and JS |
| 15 | CI runs without Spotify. | Done | `.github/workflows/ci.yml` |
| 16 | Scheduled sync uses Spotify secrets. | Done | `catalog-sync.yml` |
| 17 | No-change sync skips PR creation. | Implemented, needs GitHub run | `catalog-sync.yml` change detection |
| 18 | Changed sync creates or updates one PR. | Implemented, needs GitHub run | `catalog-sync.yml` branch and PR logic |
| 19 | PR includes readable summary. | Done | `scripts/automation/catalog-pr-body.sh` |
| 20 | Automatic approval only for allowed paths. | Done | `catalog-review-guard.sh` |
| 21 | Code changes are never auto-approved. | Done | blocked path guard |
| 22 | Auto-merge respects `main` protection. | Done | Mergify in-place merge queue |
| 23 | Merge to `main` deploys Pages. | Done | `pages.yml` |
| 24 | Secrets are not leaked. | Done | server-side Spotify only; no secrets in exports |
| 25 | Documentation reproduces the system from zero. | Done | README, operations, automation, e2e docs |

Live Spotify and GitHub workflow behavior cannot be fully proven offline. The implemented local release gate is:

```bash
go run ./cmd/spotwufamily audit
make ci
```
