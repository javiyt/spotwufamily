#!/usr/bin/env bash
set -euo pipefail

target_path="${1:?target path is required}"
commit_message="${PAGES_COMMIT_MESSAGE:-remove pages preview}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
token="${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
deploy_dir="$(mktemp -d)"
remote="https://x-access-token:${token}@github.com/${repo}.git"

if ! git ls-remote --exit-code --heads "${remote}" gh-pages >/dev/null 2>&1; then
  echo "No gh-pages branch found."
  exit 0
fi

git clone --depth 1 --branch gh-pages "${remote}" "${deploy_dir}"
if [[ ! -e "${deploy_dir}/${target_path}" ]]; then
  echo "No preview found at ${target_path}."
  exit 0
fi

git -C "${deploy_dir}" config user.name "github-actions[bot]"
git -C "${deploy_dir}" config user.email "41898282+github-actions[bot]@users.noreply.github.com"
rm -rf "${deploy_dir}/${target_path}"
touch "${deploy_dir}/.nojekyll"

git -C "${deploy_dir}" add -A
if git -C "${deploy_dir}" diff --cached --quiet; then
  echo "No Pages preview changes to publish."
  exit 0
fi

git -C "${deploy_dir}" commit -m "${commit_message}"
git -C "${deploy_dir}" push "${remote}" HEAD:gh-pages
