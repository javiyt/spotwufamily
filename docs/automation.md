# Automation

Workflows:

- `ci.yml`: validates the artist catalog, verifies SQLite, regenerates ignored exports, builds Hugo, tests, vets and builds the CLI.
- `catalog-sync.yml`: runs scheduled or manual Spotify sync, regenerates SQLite and the snapshot, and opens or updates one catalog PR.
- `catalog-pr-review.yml`: approves generated catalog PRs only after checking trusted metadata, labels and changed paths.
- `pages.yml`: verifies exports, builds Hugo and deploys GitHub Pages after merge to `main`.
- `pages-preview.yml`: deploys same-repository PR branches to a Pages preview under `/pr-preview/pr-<number>/` and removes the preview when the PR closes.

Current local build command:

```bash
go run ./cmd/spotwufamily site build
```

The site is configured for `https://javiyt.github.io/spotwufamily/` and must keep links working under that subpath.

GitHub Pages must be configured as `Deploy from branch` with branch `gh-pages` and folder `/ (root)`. Repository Actions workflow permissions must allow read and write access so `GITHUB_TOKEN` can push to `gh-pages`. Production deploys publish to the branch root and preserve `pr-preview/`; PR preview deploys publish only under the PR-specific preview path.

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

`catalog-sync.yml` runs `sync --resume`. If Spotify returns a 429 that cannot be waited out inside the job, the sync command records completed artists in SQLite, writes a partial snapshot, emits a warning, and exits successfully so the workflow can open or update the catalog PR with that checkpoint. The next scheduled or manual run resumes from the latest compatible partial run and skips artists whose Spotify IDs have not changed.

Mergify queues matching PRs and squash-merges them after CI passes. The queue is configured for in-place checks (`max_parallel_checks: 1`, `batch_size: 1`, identical queue and merge conditions) so it remains compatible with `main` requiring branches to be up to date before merging.

See [Security](security.md), [End-to-end verification](e2e.md) and [Release readiness](release-readiness.md) for the approval and release checklist.
