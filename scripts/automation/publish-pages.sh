#!/usr/bin/env bash
set -euo pipefail

source_dir="${1:?source directory is required}"
target_path="${2:-}"
commit_message="${PAGES_COMMIT_MESSAGE:-deploy pages}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
token="${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
deploy_dir="$(mktemp -d)"
remote="https://x-access-token:${token}@github.com/${repo}.git"

if git ls-remote --exit-code --heads "${remote}" gh-pages >/dev/null 2>&1; then
  git clone --depth 1 --branch gh-pages "${remote}" "${deploy_dir}"
else
  git init "${deploy_dir}"
  git -C "${deploy_dir}" checkout --orphan gh-pages
fi

git -C "${deploy_dir}" config user.name "github-actions[bot]"
git -C "${deploy_dir}" config user.email "41898282+github-actions[bot]@users.noreply.github.com"

if [[ -z "${target_path}" ]]; then
  find "${deploy_dir}" -mindepth 1 -maxdepth 1 ! -name .git ! -name pr-preview -exec rm -rf {} +
  cp -R "${source_dir%/}/." "${deploy_dir}/"
else
  rm -rf "${deploy_dir}/${target_path}"
  mkdir -p "${deploy_dir}/${target_path}"
  cp -R "${source_dir%/}/." "${deploy_dir}/${target_path}/"
fi

touch "${deploy_dir}/.nojekyll"
git -C "${deploy_dir}" add -A
if git -C "${deploy_dir}" diff --cached --quiet; then
  echo "No Pages changes to publish."
  exit 0
fi

git -C "${deploy_dir}" commit -m "${commit_message}"
git -C "${deploy_dir}" push "${remote}" HEAD:gh-pages
