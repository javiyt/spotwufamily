# End-to-end Verification

Use this checklist for release or phase-completion verification.

## Offline

```bash
go run ./cmd/spotwufamily artists validate
go run ./cmd/spotwufamily db verify
go run ./cmd/spotwufamily export
go run ./cmd/spotwufamily site build
go test ./...
go vet ./...
go build -o /tmp/spotwufamily ./cmd/spotwufamily
go run ./cmd/spotwufamily audit
```

Expected result:

- Artist YAML is valid.
- SQLite integrity, foreign keys, migrations and snapshot freshness pass.
- Exported JSON is deterministic and leaves no generated diff.
- Hugo builds under the GitHub Pages subpath.
- Tests and vet run without Spotify credentials.

## Online

Run only with reviewed Spotify IDs and credentials:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily sync --artist wu-tang-clan
go run ./cmd/spotwufamily audit
```

Expected result:

- Spotify pagination completes.
- Releases include albums, singles, compilations and appearances.
- Album tracks and credited artists are stored.
- A second sync with unchanged Spotify data leaves generated artifacts unchanged.

## GitHub

Expected workflow behavior:

- CI does not require Spotify credentials.
- Scheduled sync uses Spotify secrets.
- No-change sync exits without creating a PR.
- Changed sync creates or updates one `automation/catalog-sync-*` PR.
- Generated-data PRs get a readable summary.
- Automatic approval never approves code, workflow, migration, script, template, dependency or `data/artists.yaml` changes.
- After merge to `main`, Pages rebuilds and deploys the Hugo site.
