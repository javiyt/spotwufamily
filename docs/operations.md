# Operations

## Local Audit

Run the full local audit before merging catalog or automation changes:

```bash
make audit
```

The audit validates `data/artists.yaml`, verifies `data/catalog.db`, checks that `data/catalog.snapshot.sql` is fresh, regenerates JSON exports, verifies generated artifacts, checks that generated files have no uncommitted diff and builds the Hugo site into `/tmp/spotwufamily-site`.

For unit-test style runs without Hugo or Git checks:

```bash
make audit-fast
```

## Bootstrap From YAML

Initialize the local project state from `data/artists.yaml`:

```bash
make init-from-yaml
```

This validates the YAML, migrates SQLite, writes a fresh logical snapshot, verifies the database, exports JSON, builds Hugo and runs the audit gate.

## Catalog Sync

Resolve reviewed artist IDs with as much automation as possible:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-apply
make artists-validate
```

Review `resolve.md` for skipped or ambiguous entries before manually editing YAML.

If an artist is skipped because candidates are ambiguous, use interactive mode:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-resolve-interactive
```

Select the candidate number to write its Spotify ID to YAML, `s` to skip, or `q` to save and stop.

To review the full YAML, including artists that already have a Spotify ID:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... make artists-review-interactive
```

For artists with an existing ID, the prompt shows current Spotify profiles and lets you keep them, replace the primary ID with a candidate, add one or more candidates as additional IDs with `aN` or `aN,aM` such as `a2,a1`, clear all IDs, skip, or save and quit.

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
