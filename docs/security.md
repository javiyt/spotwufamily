# Security

## Spotify Credentials

- `SPOTIFY_CLIENT_ID` and `SPOTIFY_CLIENT_SECRET` are read only by CLI commands and GitHub Actions.
- Spotify credentials must never be written to `site/`, generated JSON, logs or PR bodies.
- The static site must never call Spotify directly.

## Local admin

- `admin serve` is local-only by default, requires a session token, CSRF tokens and Host/Origin checks, and is never deployed with the static site. See [Local admin](admin.md).
- Catalog writes are same-origin + CSRF protected, serialised by a file lock, validated against the whole catalog and replaced atomically; Spotify profile mappings cannot be edited from the UI.
- Spotify credentials stay in the Go process; the admin only reports whether they are present.

### Admin maintenance workflows

Threats considered and how the admin answers them (details in [Local admin](admin.md) and [Operations](operations.md#admin-maintenance-workflows)):

- **Cross-site requests / DNS rebinding.** Every request needs the session cookie (`HttpOnly`, `SameSite=Strict`, in memory only); every `POST` (preview, run, cancel, retry, freshness, publish re-check) also needs a same-origin `Origin`/`Sec-Fetch-Site`, a session-bound CSRF token and a `Host` matching the listen address. `make admin-smoke` checks that a preview without a session and a cross-origin freshness `POST` are rejected.
- **Replayed or forged confirmations.** A run needs the preview token issued for exactly that action, parameters and previewed state (HMAC-SHA256, 10-minute expiry). The secret is 32 bytes from `crypto/rand`, generated at every start and never stored, so tokens die on restart; the admin refuses to start without it. A token for a state that changed since the preview is refused as stale, and `sync`, `reconcile-apply`, `db-snapshot`, `export`, `refresh-export` and the release actions re-check it inside the job under their lock (`refresh-metadata` and `discover-import` rely on the check made when the run is confirmed).
- **Accidental destructive runs.** Large syncs, reconcile apply and release publish need a typed phrase; double submits are answered `409` because one job holds each resource (`catalog-yaml`, `database`, `release`).
- **Data loss.** Every database write is preceded by a verified `VACUUM INTO` backup in `build/admin/backups/` (never overwritten) and YAML writes keep `<catalog>.bak`. A job cut off by a restart is recorded as `interrupted`, never as succeeded.
- **Command injection.** The only subprocess is the release adapter: fixed `python3`, fixed script path `scripts/automation/catalog-release.py`, fixed subcommands (`preflight`, `prepare`, `publish`), a `--repo` validated as `owner/name`, no user-supplied arguments, and an environment reduced to `PATH`, `HOME`, `LANG` and an already set `GH_TOKEN`/`GITHUB_TOKEN`.
- **Information leaks.** Responses, job messages, reports, the operations log and API errors never contain filesystem paths, SQL, stack traces, tokens or credentials; unexpected errors show a generic message and are logged server-side only. Spotify credentials are read from the environment when an action runs and never leave the process.
- **Unwanted publication.** Nothing publishes automatically. Only the explicit step 2 of `/publish` (typed `publish release`, after a prepared package that matches the local content) runs `gh release create`; once started it is not cancellable, so an interrupted publish must be checked on the Releases page before retrying.
- The admin is never deployed: nothing in `site/` references its routes (a test checks this), and only the static site and the pinned release reach Pages.

## GitHub Automation

- `catalog-sync.yml` writes only catalog artifacts to `automation/catalog-sync-*` branches.
- `catalog-pr-review.yml` checks PR metadata using trusted scripts from `main`, not code from the PR branch.
- Automatic approval is restricted to same-repository PRs targeting `main`, with expected labels and an allowlist of generated paths.
- Code, workflow, migration, script, template, dependency and editorial YAML changes are blocked from automatic approval.
- The privileged `Pages Deploy` workflow runs from the default branch and reads only generated static files from `gh-pages`; it never executes code from the triggering PR.
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
