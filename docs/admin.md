# Local admin

A small web UI for browsing the editorial catalog and its Spotify links. It is an inbound adapter of the hexagonal application: it only calls use cases in `internal/application/admin` and holds no business rules. This first version is **read-only**; edits still go through the CLI or `data/artists.yaml`.

It is never part of the Hugo/Pages build: nothing in `site/` references it, and the static site stays read-only.

## Run

```bash
make admin                      # http://127.0.0.1:8787, CATALOG=data/artists.yaml DB=data/catalog.db
make admin ADMIN_PORT=9000
go run ./cmd/spotwufamily admin serve --host 127.0.0.1 --port 8787 --catalog data/artists.yaml --db data/catalog.db
```

The command prints a one-time-per-run launch URL, `http://127.0.0.1:8787/auth?token=...`. Open it: the token is exchanged for an `HttpOnly`, `SameSite=Strict` session cookie and the URL redirects to the dashboard. Stop with Ctrl+C (graceful shutdown, 5 s).

`make admin-smoke` starts the server on a spare port and checks: non-loopback bind refused, unauthenticated request rejected, launch URL authenticates, foreign `Host` rejected, cross-origin `POST` rejected. It needs `go` and `curl`, no Node and no Spotify credentials.

## Pages and API

| Page | JSON | Content |
| --- | --- | --- |
| `/` | `/api/v1/status` | counts, verified/pending/unresolved IDs, groups/people, review queue, pending reconciliation, last sync, credential presence |
| `/artists` | `/api/v1/artists` | searchable, paginated artist list |
| `/groups` | `/api/v1/groups` | groups with verified/proposed member counts |
| `/review` | `/api/v1/review` | unverified Spotify profiles and proposed relationships |
| `/operations` | | database/credential status and the CLI commands to run |

List endpoints take `q`, `page` and `page_size` (default 25, max 100; out-of-range values are clamped). Errors are always `{"error":{"code":"...","message":"..."}}` for `/api/*` and for clients sending `Accept: application/json`, and an HTML error page otherwise. Messages never include paths, stack traces or Go error text; details are logged server-side.

Definitions: *verified* = a linked Spotify ID has a verified profile review; *pending* = IDs linked but none verified; *unresolved* = no ID linked. A *group* is an artist tagged `affiliate_group` or the target of a non-rejected `member_of` relationship. Identical names and unverified memberships are never treated as identity evidence.

*Pending reconciliation* counts active album/track links classified `orphan` by the same rule as `artists reconcile` (read-only dry run).

## Offline mode

The YAML catalog is read on every request, so edits show up on reload. If the database file is missing or unreadable the UI says so and keeps serving the catalog; the file is never created. The admin never calls Spotify.

## Security model

- Binds `127.0.0.1` by default. A non-loopback `--host` (including `0.0.0.0` or an empty host) is refused unless `--allow-remote` is passed **and** `SPOTWUFAMILY_ADMIN_TOKEN` holds a token of at least 32 characters. There is no TLS: put remote access behind an SSH tunnel or a TLS proxy.
- `Host` must match the listen address (loopback aliases `localhost`/`127.0.0.1`/`[::1]` on the same port), which defeats DNS rebinding.
- Every request except static assets needs a session. Sessions live in memory only, expire after 8 h and are lost on restart; nothing is written to the repo. Token comparison is constant-time and failed attempts are rate limited (10 per minute, then 429).
- Every state-changing request needs a same-origin `Origin` (or no cross-site `Sec-Fetch-Site`) **and** a session-bound HMAC CSRF token (`csrf_token` form field). Today the only mutation is `POST /logout`; future edits must reuse the same path.
- Headers on every response: strict CSP (no inline script/style, `frame-ancestors 'none'`), `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy: no-referrer`, COOP/CORP, `Cache-Control: no-store`.
- Request bodies are capped at 64 KiB, headers at 16 KiB, with read/write/idle timeouts.
- Templates are `html/template` with contextual escaping, so artist names and notes cannot inject script.
- `SPOTIFY_CLIENT_ID`/`SPOTIFY_CLIENT_SECRET` are only checked for presence in the server process; values are not read into any response, template or log. The launch token is not logged (only request paths, never queries). The server proxies no URLs.

## Layout

- `internal/application/admin`: read-only use cases, ports (`OperationsReader`) and pagination.
- `internal/adapters/inbound/httpadmin`: server, security middleware, handlers, embedded templates and CSS/JS (no Node build).
- `internal/bootstrap`: shared composition root wiring YAML + SQLite into the admin service.
- `internal/adapters/inbound/cli/admin.go`: the `admin serve` command.
