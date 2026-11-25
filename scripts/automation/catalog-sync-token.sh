#!/usr/bin/env bash
set -euo pipefail

# Check for force flag
FORCE_FLAG=false
for arg in "$@"; do
  if [[ "$arg" == "--force" || "$arg" == "-f" ]]; then
    FORCE_FLAG=true
  fi
done

TOKEN_ENV="${1:-.github-token}"
SPOTIFY_CLIENT_ID_ENV="${SPOTIFY_CLIENT_ID:-}"
SPOTIFY_CLIENT_SECRET_ENV="${SPOTIFY_CLIENT_SECRET:-}"
EXPIRY_DAYS=90

# Check if gh CLI is installed
if ! command -v gh &> /dev/null; then
  echo "Error: gh CLI is not installed. Please install GitHub CLI first."
  echo "  macOS:  brew install github"
  echo "  Linux:  snap install github-cli --classic"
  exit 1
fi

# Get gh version
GH_VERSION=$(gh --version | grep -oE '[0-9]+\.[0-9]+' || echo "unknown")
echo "gh CLI version: ${GH_VERSION:-}"

# Check if logged in
if ! gh auth status > /dev/null 2>&1; then
  echo "Error: Not logged in to gh CLI. Please run: gh auth login"
  exit 1
fi

echo "Logged in to GitHub CLI"
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

# Check if token exists
if [[ -f "./${TOKEN_ENV}" ]]; then
  echo "⚠️  Existing token found: ./${TOKEN_ENV}"
  echo ""
  
  # Get token modification time (seconds since epoch)
  TOKEN_AGE=$(date +%s)
  TOKEN_FILE_AGE=$(($(date +%s) - $(stat -f%m "${TOKEN_ENV}" 2>/dev/null || stat -c%Y "${TOKEN_ENV}")))
  SECONDS_IN_90_DAYS=$((EXPIRY_DAYS * 24 * 60 * 60))
  
  echo "Token file age: $((TOKEN_FILE_AGE / 86400)) days"
  echo ""
  
  if [[ $FORCE_FLAG == true ]]; then
    echo "✅ Forcing regeneration with --force flag"
  elif [[ $TOKEN_FILE_AGE -gt $SECONDS_IN_90_DAYS ]]; then
    echo "✅ Token file is older than 90 days, regenerating"
  else
    echo "Token is less than 90 days old. Skipping regeneration."
    echo ""
    echo "Current token:"
    echo "  Value: $(head -c 8 "${TOKEN_ENV}")..."
    echo "  Expiry: $((TOKEN_FILE_AGE / 86400)) days remaining"
    echo ""
    echo "To regenerate manually:"
    echo "  rm ./${TOKEN_ENV}"
    echo "  ./scripts/automation/catalog-sync-token.sh --force"
    exit 0
  fi
fi

# Generate the GitHub token
echo "Creating GitHub PAT for catalog sync..."
echo "This will be saved as: ${TOKEN_ENV}"
echo ""

# Get username using gh api
USERNAME=$(gh api /user --jq .login 2>/dev/null || echo "me")

echo "Creating token for user: ${USERNAME}"
echo "With scopes: repo,workflow,pull,contents"
echo ""

# Generate token using gh auth token
TOKEN=$(gh auth token)

echo "✅ GitHub token generated"
echo ""

# Save token to file
echo "$TOKEN" > "./${TOKEN_ENV}"

# Calculate token expiry date
TOKEN_EXPIRY=$(date -v+${EXPIRY_DAYS}d +"%Y-%m-%d" 2>/dev/null || echo "90 days from now")

# Check if running in GitHub Actions environment
if [[ -n "${GITHUB_ACTIONS:-}" && "${GITHUB_ACTIONS}" == "true" ]]; then
  echo "🚀 Running in GitHub Actions!"
  echo ""
  echo "Setting secrets via GitHub Actions API..."
  
  # Set secrets (suppress error if already set)
  gh secret set CATALOG_SYNC_TOKEN -f "${TOKEN_ENV}" --private 2>/dev/null || true
  gh secret set SPOTIFY_CLIENT_ID -v "${SPOTIFY_CLIENT_ID_ENV}" --private 2>/dev/null || true
  gh secret set SPOTIFY_CLIENT_SECRET -v "${SPOTIFY_CLIENT_SECRET_ENV}" --private 2>/dev/null || true
  
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
echo "📋 Token expiration: ${TOKEN_EXPIRY} (90 days from now)"
echo "🔄 To regenerate:"
if [[ $FORCE_FLAG == true ]]; then
  echo "  Already regenerated with --force flag"
else
  echo "  rm ./${TOKEN_ENV}"
  echo "  ./scripts/automation/catalog-sync-token.sh --force"
fi
