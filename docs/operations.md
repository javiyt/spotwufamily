# Operations

## Local Audit

Run the full local audit before merging catalog or automation changes:

```bash
go run ./cmd/spotwufamily audit
```

The audit validates `data/artists.yaml`, verifies `data/catalog.db`, checks that `data/catalog.snapshot.sql` is fresh, regenerates JSON exports, verifies generated artifacts, checks that generated files have no uncommitted diff and builds the Hugo site into `/tmp/spotwufamily-site`.

For unit-test style runs without Hugo or Git checks:

```bash
go run ./cmd/spotwufamily audit --skip-site --skip-git-diff
```

## Catalog Sync

Manual sync for one reviewed artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily sync --artist wu-tang-clan
```

After sync, run:

```bash
go run ./cmd/spotwufamily db verify
go run ./cmd/spotwufamily export
go run ./cmd/spotwufamily audit
```

## Rebuild From Snapshot

Use this to prove the logical snapshot can recreate the versioned database:

```bash
go run ./cmd/spotwufamily db rebuild
go run ./cmd/spotwufamily db verify
```

## Static Site

Build locally:

```bash
go run ./cmd/spotwufamily site build
```

Serve locally while editing templates:

```bash
make serve
```
