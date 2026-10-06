# Automation

Workflows:

- `ci.yml`: validates the artist catalog, verifies SQLite, regenerates ignored exports, builds Hugo, tests, vets and builds the CLI.
- `catalog-sync.yml`: runs scheduled or manual Spotify sync, downloads the pinned catalog, runs Spotify sync, uploads a full backup to Releases, and opens or updates one manifest-only catalog PR.
- `catalog-pr-review.yml`: approves generated catalog PRs only after checking trusted metadata, labels and changed paths.
- `pages.yml`: verifies exports, builds Hugo and updates the production files in `gh-pages` after merge to `main`.
- `pages-preview.yml`: writes same-repository PR previews under `/pr-preview/pr-<number>/` in `gh-pages`; closing a PR removes its preview and marks its preview deployments inactive.
- `pages-deploy.yml`: after a successful Pages or Pages Preview run, uploads the complete `gh-pages` tree and deploys it through the GitHub Pages API. It also supports manual dispatch.

Current local build command:

```bash
go run ./cmd/spotwufamily site build
```

The site is configured for `https://javiyt.github.io/spotwufamily/` and must keep links working under that subpath.

GitHub Pages must be configured with source `GitHub Actions`. The production and preview workflows assemble static files in `gh-pages`; writing that branch with `GITHUB_TOKEN` does not itself publish a site. `Pages Deploy` runs on the default branch when either producer succeeds, uploads the complete tree using `actions/upload-pages-artifact`, and publishes it using `actions/deploy-pages` with `pages: write` and `id-token: write`. The `github-pages` environment can therefore remain restricted to `main`. The deployment workflow executes no code from PR branches.

Production updates preserve `pr-preview/`; preview updates change only their PR directory. Actual deployments include both production and all retained previews. Closing or merging a PR removes its preview, so historical deployment URLs for closed PRs can return 404. A successful build is followed by a separate `Pages Deploy` run; verify that run to confirm the site is live. The domain `javi.yt` is inherited from the user Pages site; the `github.io` URLs redirect there.

After introducing `pages-deploy.yml`, merge it into `main`, then run `Pages Deploy` manually once to publish the existing assembled site. Later producer completions trigger it automatically.

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

- `data/catalog.release.json`

CI, Pages and previews download the exact release pinned by the checked-out manifest using `GITHUB_TOKEN`. No storage service or additional storage credential is required. A catalog release is uploaded before its PR is created, allowing CI to verify it; production uses it only after the manifest merges. Upload failures leave the manifest unchanged.

Hugo JSON exports are ignored by Git and regenerated during CI and Pages deployment.

`catalog-sync.yml` runs `sync --resume`. If Spotify returns a 429 that cannot be waited out inside the job, the sync command records completed artists in SQLite, writes a partial snapshot, emits a warning, and exits successfully so the workflow can open or update the catalog PR with that checkpoint. The next scheduled or manual run resumes from the latest compatible partial run and skips artists whose Spotify IDs have not changed.

Mergify queues matching PRs and squash-merges them after CI passes. The queue is configured for in-place checks (`max_parallel_checks: 1`, `batch_size: 1`, identical queue and merge conditions) so it remains compatible with `main` requiring branches to be up to date before merging.

See [Security](security.md), [End-to-end verification](e2e.md) and [Release readiness](release-readiness.md) for the approval and release checklist.
