# Automation

Workflows:

- `ci.yml`: validates the artist catalog, verifies SQLite, regenerates ignored exports, builds Hugo, tests, vets and builds the CLI.
- `catalog-sync.yml`: runs scheduled or manual Spotify sync, regenerates SQLite and the snapshot, and opens or updates one catalog PR.
- `catalog-pr-review.yml`: approves generated catalog PRs only after checking trusted metadata, labels and changed paths.
- `pages.yml`: verifies exports, builds Hugo and deploys GitHub Pages after merge to `main`.

Current local build command:

```bash
go run ./cmd/spotwufamily site build
```

The site is configured for `https://javiyt.github.io/spotwufamily/` and must keep links working under that subpath.

Required repository secrets:

- `SPOTIFY_CLIENT_ID`
- `SPOTIFY_CLIENT_SECRET`

Recommended repository secrets for automation identities:

- `CATALOG_SYNC_APP_ID`
- `CATALOG_SYNC_APP_PRIVATE_KEY`
- `CATALOG_REVIEW_APP_ID`
- `CATALOG_REVIEW_APP_PRIVATE_KEY`
- `CATALOG_SYNC_EXPECTED_AUTHOR`

`CATALOG_SYNC_TOKEN` and `CATALOG_REVIEW_TOKEN` are supported as fallback bot tokens when GitHub Apps are not available.

The sync identity and review identity must be separate GitHub Apps or bot credentials when branch protection requires review approval. The review guard only approves same-repository PRs targeting `main`, with `automation`, `catalog-update` and `spotify` labels, from `automation/catalog-sync-*` branches, and with changes restricted to versioned catalog artifacts:

- `data/catalog.db`
- `data/catalog.snapshot.sql`

Hugo JSON exports are ignored by Git and regenerated during CI and Pages deployment.

`catalog-sync.yml` runs `sync --resume`. If Spotify returns a 429 that cannot be waited out inside the job, the sync command records completed artists in SQLite, writes a partial snapshot, and the workflow still opens or updates the catalog PR with that checkpoint before failing the job. The next scheduled or manual run resumes from the latest compatible partial run and skips artists whose Spotify IDs have not changed.

Mergify is configured only to keep matching PRs up to date. It does not merge PRs; merge decisions are left to GitHub branch protection and the explicit auto-merge request created by `catalog-sync.yml`.

See [Security](security.md), [End-to-end verification](e2e.md) and [Release readiness](release-readiness.md) for the approval and release checklist.
