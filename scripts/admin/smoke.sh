#!/usr/bin/env bash
# Smoke test for `spotwufamily admin serve`: loopback bind, auth, host check.
# Needs only go and curl; no Spotify credentials and no network beyond loopback.
set -euo pipefail

CATALOG="${CATALOG:-data/artists.yaml}"
DB="${DB:-data/catalog.db}"
PORT="${ADMIN_PORT:-18787}"
BIN="${BIN:-build/spotwufamily}"

work="$(mktemp -d)"
pid=""
cleanup() {
  [ -n "$pid" ] && kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

go build -o "$BIN" ./cmd/spotwufamily

# Dangerous binds must fail safe.
if "$BIN" admin serve --host 0.0.0.0 --port "$PORT" >/dev/null 2>&1; then
  echo "FAIL: non-loopback bind was accepted" >&2; exit 1
fi

"$BIN" admin serve --port "$PORT" --catalog "$CATALOG" --db "$DB" >"$work/out" 2>"$work/err" &
pid=$!

url=""
for _ in $(seq 1 50); do
  url="$(grep -o "http://127.0.0.1:${PORT}/auth?token=[A-Za-z0-9_-]*" "$work/out" || true)"
  [ -n "$url" ] && break
  sleep 0.1
done
[ -n "$url" ] || { echo "FAIL: server did not start"; cat "$work/err"; exit 1; }

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

[ "$(code "http://127.0.0.1:${PORT}/")" = 401 ] || { echo "FAIL: unauthenticated access allowed" >&2; exit 1; }
[ "$(code -c "$work/jar" "$url")" = 303 ] || { echo "FAIL: launch URL did not authenticate" >&2; exit 1; }
[ "$(code -b "$work/jar" "http://127.0.0.1:${PORT}/")" = 200 ] || { echo "FAIL: dashboard unavailable" >&2; exit 1; }
[ "$(code -b "$work/jar" -H 'Host: evil.example' "http://127.0.0.1:${PORT}/")" = 403 ] || { echo "FAIL: foreign Host accepted" >&2; exit 1; }
[ "$(code -b "$work/jar" -X POST -H 'Origin: https://evil.example' "http://127.0.0.1:${PORT}/logout")" = 403 ] || { echo "FAIL: cross-origin POST accepted" >&2; exit 1; }

echo "admin smoke test passed"
