#!/usr/bin/env bash
set -euo pipefail

summary_file="${1:-/tmp/catalog-pr-body.md}"

{
  echo "## Catalog Sync"
  echo
  echo "Automated Spotify catalog sync output."
  echo
  echo "### Changed files"
  echo
  if git diff --name-only HEAD~1..HEAD | sed 's/^/- `/' | sed 's/$/`/'; then
    true
  else
    echo "- Unable to list changed files."
  fi
  echo
  echo "### Diff summary"
  echo
  echo '```text'
  git diff --stat HEAD~1..HEAD || true
  echo '```'
  echo
  echo "### Verification"
  echo
  echo "- Catalog validation passed in workflow."
  echo "- SQLite verification passed in workflow."
  echo "- Export regeneration passed in workflow."
  echo "- Hugo build passed in workflow."
} >"${summary_file}"

echo "${summary_file}"
