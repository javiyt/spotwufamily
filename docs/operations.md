# Operations

## Local Audit

Run the full local audit before merging catalog or automation changes:

```bash
make audit
```

After a fresh clone, run `make catalog-fetch` to download the catalog pinned by `data/catalog.release.json`. This refuses to overwrite existing local files.

The audit validates `data/artists.yaml`, verifies `data/catalog.db`, checks that `data/catalog.snapshot.sql.gz` is fresh, regenerates ignored JSON exports, verifies generated artifacts and builds the Hugo site into `/tmp/spotwufamily-site`.

For unit-test style runs without Hugo or Git checks:

```bash
make audit-fast
```

## Editing The Catalog In The Admin

`make admin` serves a local editor for artists, groups and relationships (see [Local admin](admin.md)). After editing:

```bash
make artists-validate     # the admin already validated, this is the CLI gate
make artists-resolve-report   # if a slug changed, then `artists reconcile` (see below)
make export site-build    # regenerate the static data from the saved YAML
```

Every save keeps the previous file as `data/artists.yaml.bak` and serialises writers through `data/artists.yaml.lock` (both git-ignored). To undo the last save: `cp data/artists.yaml.bak data/artists.yaml`. The catalog Release backups are separate and never overwritten by the admin.

## Bootstrap From YAML

Initialize the local project state from `data/artists.yaml`:

```bash
make init-from-yaml
```

This validates the YAML, migrates SQLite, seeds configured artists and Spotify IDs into the database, writes a fresh logical snapshot, verifies the database, exports JSON, builds Hugo and runs the audit gate.

This bootstrap populates configured artists in SQLite and generated JSON. Album and track rows remain empty until a Spotify sync runs for enabled artists.

Generated site JSON is ignored by Git. Run `make export` before local Hugo work if `site/data/generated` or `site/static/search-index.json` is missing. GitHub Pages runs the export step during deployment.

## Catalog Sync

Resolve reviewed artist IDs with as much automation as possible:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-apply
make artists-validate
```

Review `resolve.md` for skipped or ambiguous entries before manually editing YAML.

Enable every reviewed artist that has at least one Spotify ID:

```bash
make artists-enable-with-ids-dry-run
make artists-enable-with-ids
```

If an artist is skipped because candidates are ambiguous, use interactive mode:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-interactive
```

Select the candidate number to write its Spotify ID to YAML, `s` to skip, or `q` to save and stop.

To review the full YAML, including artists that already have a Spotify ID:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-review-interactive
```

For artists with an existing ID, the prompt shows current Spotify profiles and MusicBrainz warnings when configured Spotify albums do not match known MusicBrainz release groups. Candidate scoring also highlights `track_evidence` when a candidate appears on tracks from albums by configured groups with reviewed Spotify IDs. Live resolution reads cached albums and tracks from SQLite before calling Spotify, so a freshly synced catalog reduces API quota pressure. The prompt lets you keep the current IDs, replace the primary ID with a candidate, add one or more candidates as additional IDs with `aN` or `aN,aM` such as `a2,a1`, clear all IDs, skip, or save and quit.

Audit reviewed Spotify IDs against MusicBrainz album release groups:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-audit-albums ARTIST=wu-tang-clan
```

This writes `albums-audit.md` by default and does not modify YAML or SQLite. Use it to find configured Spotify profiles that miss canonical albums or return suspicious extra albums.

Manual sync for one reviewed artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make sync-artist ARTIST=wu-tang-clan
```

After sync, run:

```bash
make db-verify
make export
make audit
```

## Reconcile A Corrected Spotify Mapping

When a reviewed decision rejects or replaces a Spotify ID (`artists review-profile`), rows already synced for that configured artist stay in SQLite: sync only adds or reactivates `artist_albums` / `artist_tracks` rows, it never retires them. `artists reconcile` closes that gap for one configured artist at a time, with a reviewed plan, a local backup and a transaction.

SQLite keeps a durable **last-synced mapping** per artist (`artist_mapping_snapshots`). `artists seed-db` and `sync` record it the first time an artist is seen (from the IDs already projected in `configured_artist_spotify_ids`) and compare it with `data/artists.yaml` every time. A difference opens a `drifted` entry in `artist_reconciliations` and a warning is printed by `seed-db`, `sync` and `db verify` until it is resolved. `db verify`, `db snapshot` and `export` keep passing while a reconciliation is pending; they only warn.

```bash
# 0. Which artists are not aligned? (read-only)
spotwufamily artists reconcile --status [--format json]

# 1. Re-seed the corrected IDs so the drift is recorded (writes only configured_* and journal rows)
spotwufamily artists seed-db

# 2. Dry run: write-free impact report (default). Also --format json|markdown, both deterministic.
spotwufamily artists reconcile --artist streetlife --dry-run --format markdown

# 3. Apply the reviewed plan: needs the plan_checksum printed by the dry run
spotwufamily artists reconcile --artist streetlife --apply --confirm <plan_checksum> [--backup-dir build/reconcile-backups]

# 4. Resync the replacement IDs (resumable; safe to repeat after a 429 or failure)
spotwufamily sync --artist streetlife --resume

# 5. Verify, snapshot, export
make db-verify db-snapshot export audit-fast
```

The report shows the old/new identity diff (`previous`, `current`, `added`, `removed`), every active `artist_albums` / `artist_tracks` association, the Spotify artists credited on the affected ones, other configured artists sharing the media, completed sync checkpoints tied to other IDs and the stale metadata cache. Each association is `supported` (a credit points at a currently linked profile), `orphan` (credits exist but none points at a linked profile, or the artist has no linked profile) or `unverifiable` (no stored credits; reported and kept). Group membership or identical artist names are never evidence; only stored Spotify credit IDs are.

The proposed decisions are all reversible:

| Action | Effect |
| --- | --- |
| `unlink_association` | soft-retires the orphan edge of **this** artist only: `active = 0`, `missing_since = <retired_at>` |
| `preserve_shared_media` / `report_unreferenced_media` | albums, tracks, artists and credits are never deleted; media other artists use is untouched and unreferenced media is only reported for a separate reviewed prune |
| `invalidate_checkpoint` | completed `sync_run_artists` rows bound to other IDs become `invalidated@<retired_at>`, so neither freshness skipping nor `--resume` trusts them |
| `invalidate_metadata_cache` | the stale `artist_metadata_refreshes` row is removed (its value is journaled for rollback) |
| `resync_replacement_ids` | the artist stays `awaiting_resync` until a completed sync checkpoint for the replacement IDs advances the snapshot |

`--apply` refuses to run without `--confirm <plan_checksum>`, and rejects a checksum that no longer matches the current state, so only the reviewed plan is executed. Before changing anything it writes `<backup-dir>/<slug>-<timestamp>.db` (SQLite `VACUUM INTO`, never replacing an existing file) and `.manifest.json` with the plan, the backup SHA-256 and the logical snapshot SHA-256; these checksums are also journaled in `artist_reconciliations`. All changes then run in one SQLite transaction: any failure leaves the database untouched and the backup in place. Repeating `--apply` after it succeeded changes nothing (no second backup).

Failures and rate limits: a failed or 429-aborted sync leaves the reconciliation `awaiting_resync`, records the last error and keeps warning; rerun `sync --artist SLUG --resume`. YAML and SQLite are never considered aligned until the replacement sync completes (or the YAML is reverted and re-seeded, which clears a never-applied drift).

Rollback: `--apply` prints a `retired_at` token. `spotwufamily artists reconcile --artist SLUG --restore <retired_at>` reactivates exactly those edges, re-validates the invalidated checkpoints and cache, restores the previous snapshot and reopens the entry as `drifted`.

Read-only impact data for the web UI: `export` writes `site/data/generated/reconciliation/index.json` (slug, status, previous and target IDs of every pending reconciliation; no local paths or errors).

### Publishing the corrected catalog

The local database is not tracked by Git. A reconciliation changes `data/catalog.db` / `data/catalog.snapshot.sql.gz` only, and `data/catalog.release.json` keeps pinning the previous GitHub Release (which still contains the rejected mapping) until a **new** release is published. Existing release tags and assets are never overwritten or deleted; the old release remains the rollback target. After the plan was reviewed and applied and the replacement sync completed:

```bash
make db-verify db-snapshot export audit-fast
make catalog-release-prepare RELEASE_PACKAGE=build/catalog-backup-$(date -u +%Y%m%d-%H%M%S)
make catalog-release-publish RELEASE_PACKAGE=build/catalog-backup-<same>
git add data/catalog.release.json data/artists.yaml   # the manifest pins the new tag, sizes and checksums
```

Open the PR with the manifest change (the reconcile JSON/Markdown report makes a good description). `make catalog-fetch` on a fresh clone keeps returning the old contents until that manifest is merged.

## Publication Checklist

The local admin's `/publish` page is the guided way to publish a catalog release (see `docs/admin.md`, *Publish*). It shows, without running anything on load:

1. **Data readiness**: the cached data freshness check (seed/snapshot/export needed, unreadable database, offline, differs from published). Refresh first if anything is out of date.
2. **Prerequisites**: python3 and the release script, Go, `gh auth login`, an empty `build/catalog-release`, a local database, each with its fix. **Re-check** refreshes them in the background.
3. **What will be published**: local vs pinned content hash, last release tag, whether the content differs from what Pages serves, and the prepared package.
4. **Step 1 – Prepare package** and **Step 2 – Publish release** (typed phrase `publish release`). Step 2 appears only when a prepared package matches the local content and the prerequisites pass.
5. **Manual follow-up**: commit `data/catalog.release.json` through a pull request, wait for CI and the Pages deploy, verify the site.

Edits and Spotify profile decisions never publish anything. The CLI route (`make catalog-release-prepare` / `make catalog-release-publish`, above) remains available.

## Artist Relationships

```
spotwufamily artists relationships list [--slug S] [--type T] [--established]
spotwufamily artists relationships members --group wu-tang-clan --established
spotwufamily artists relationships groups --artist rza
spotwufamily artists relationships add --type member_of --from rza --to wu-tang-clan --dry-run
spotwufamily artists relationships review --type member_of --from rza --to wu-tang-clan --status verified \
  --reviewer NAME --confidence high --evidence-type wikipedia --evidence-url URL --evidence-rationale TEXT
spotwufamily artists relationships remove --type member_of --from rza --to wu-tang-clan
spotwufamily artists rename --slug old-slug --to new-slug --dry-run
```

New edges default to `proposed`. `export` and `audit` read the verified edges from the catalog (`--catalog`, default `data/artists.yaml`) and write `relationships/index.json`. No database migration or reseed is needed, so the pinned catalog release is unaffected. Rollback: restore the previous `artists.yaml` (or `.bak`) and re-export.

## Rebuild From Snapshot

Use this to prove the logical snapshot can recreate the versioned database:

```bash
make db-rebuild
make db-verify
```

## Static Site

Build locally:

```bash
make site-build
```

Serve locally while editing templates:

```bash
make serve
```
