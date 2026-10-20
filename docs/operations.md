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

When a reviewed decision rejects or replaces a Spotify ID (`artists review-profile`), rows already synced for that configured artist stay in SQLite: sync only adds or reactivates `artist_albums` / `artist_tracks` rows, it never retires them. `artists reconcile` closes that gap for one configured artist at a time.

```bash
# 1. Re-seed the corrected IDs, then (optional) sync the replacement profile
spotwufamily artists seed-db
spotwufamily sync --artist streetlife

# 2. Dry run: classify every active association (default, changes nothing)
spotwufamily artists reconcile --slug streetlife [--format json]

# 3. Apply after review
spotwufamily artists reconcile --slug streetlife --apply

# 4. Verify, snapshot, export
make db-verify db-snapshot export audit-fast
```

Each active association is `supported` (an album/track credit points at a currently linked profile), `orphan` (credits exist but none points at a linked profile, or the artist has no linked profile) or `unverifiable` (no stored credits; reported and kept). `--apply` only soft-retires orphans of the named artist: `active = 0`, `missing_since = <retired_at>`. Nothing is deleted. Spotify artists, albums and tracks are shared rows, so the report lists which orphans other configured artists still reference (`shared_with`) and which become unreferenced; those unreferenced rows are left in place for a separate, reviewed pruning.

Rollback: `--apply` prints a `retired_at` token. `spotwufamily artists reconcile --slug SLUG --restore <retired_at>` reactivates exactly those rows. No schema change is involved.

Release backups: the local database is not tracked by Git. A cleanup changes `data/catalog.db` / `data/catalog.snapshot.sql.gz` only, and `data/catalog.release.json` keeps pinning the previous GitHub Release (`catalog.db.gz` + snapshot, which still contain the rejected mapping) until `make catalog-release-prepare` / `catalog-release-publish` creates a **new** release and updates the manifest. Existing release tags are never overwritten, so the old backup remains the rollback target. `make catalog-fetch` on a fresh clone keeps returning the old contents until the new manifest is merged.

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

New edges default to `proposed`. `seed-db` and `sync` project verified edges into `artist_relationships` (migration `0006`, additive) and `export` writes them to `relationships/index.json`. Rollout: merge, run `spotwufamily db migrate`, then `artists seed-db` and `export`. Rollback: the migration only adds tables; restoring the previous `artists.yaml` (or `.bak`) and re-seeding removes the projection.

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
