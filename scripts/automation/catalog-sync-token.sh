#!/usr/bin/env bash
set -euo pipefail

TOKEN_ENV="${1:-.github-token}"
SPOTIFY_CLIENT_ID_ENV="${SPOTIFY_CLIENT_ID:-}"
SPOTIFY_CLIENT_SECRET_ENV="${SPOTIFY_CLIENT_SECRET:-}"

# Check if gh CLI is installed
if ! command -v gh &> /dev/null; then
  echo "Error: gh CLI is not installed. Please install GitHub CLI first."
  echo "  macOS:  brew install github"
  echo "  Linux:  snap install github-cli --classic"
  exit 1
fi

# Check if logged in
if ! gh auth status &> /dev/null; then
  echo "Error: Not logged in to gh CLI. Please run: gh auth login"
  exit 1
fi

echo "Logged in as: $(gh auth latest-user)"
echo ""

# Validate Spotify credentials
if [[ -z "${SPOTIFY_CLIENT_ID_ENV}" || -z "${SPOTIFY_CLIENT_SECRET_ENV}" ]]; then
  echo "Error: Spotify credentials not found as environment variables."
  echo ""
  echo "Required environment variables:"
  echo "  SPOTIFY_CLIENT_ID"
  echo "  SPOTIFY_CLIENT_SECRET"
  echo ""
  echo "Set them and run again:"
  echo "  export SPOTIFY_CLIENT_ID=\"<value>\""
  echo "  export SPOTIFY_CLIENT_SECRET=\"<value>\""
  echo "  ./scripts/automation/catalog-sync-token.sh"
  exit 1
fi

echo "✅ Spotify credentials found in environment variables"
echo ""

# Generate the GitHub token
echo "Creating GitHub PAT for catalog sync..."
echo "This will be saved as: ${TOKEN_ENV}"
echo ""

gh auth token --scopes "repo,workflow,pull,contents" > "./${TOKEN_ENV}"

echo "✅ GitHub token generated and saved to ./${TOKEN_ENV}"
echo ""

# Check if running in GitHub Actions environment
if [[ -n "${GITHUB_ACTIONS:-}" && "${GITHUB_ACTIONS}" == "true" ]]; then
  echo "🚀 Running in GitHub Actions!"
  echo ""
  echo "Setting secrets via GitHub Actions API..."
  
  # Set secrets
  gh secret set CATALOG_SYNC_TOKEN -f "${TOKEN_ENV}" --private
  gh secret set SPOTIFY_CLIENT_ID -v "${SPOTIFY_CLIENT_ID_ENV}" --private
  gh secret set SPOTIFY_CLIENT_SECRET -v "${SPOTIFY_CLIENT_SECRET_ENV}" --private
  
  echo "✅ All secrets set successfully in GitHub repository!"
else
  echo "ℹ️  Running locally (not in GitHub Actions)"
  echo ""
  echo "Next steps:"
  echo "  1. Read the token: cat ./${TOKEN_ENV}"
  echo "  2. Create secrets in GitHub:"
  echo "     https://github.com/javiyt/spotwufamily/settings/secrets/actions"
  echo ""
  echo "     Add these secrets:"
  echo "     - Name: CATALOG_SYNC_TOKEN, Value: [token from ./${TOKEN_ENV}]"
  echo "     - Name: SPOTIFY_CLIENT_ID, Value: ${SPOTIFY_CLIENT_ID_ENV}"
  echo "     - Name: SPOTIFY_CLIENT_SECRET, Value: ${SPOTIFY_CLIENT_SECRET_ENV}"
  echo ""
  echo "💡 Tip: Run this script from GitHub Actions to auto-set secrets:"
  echo "     - Add a workflow that runs on push to main"
  echo "     - This script will auto-set all secrets when run in Actions"
fi

echo ""
echo "📋 Token expiration: 90 days from now"
echo "🔄 To renew: rm ${TOKEN_ENV} && ./scripts/automation/catalog-sync-token.sh"
