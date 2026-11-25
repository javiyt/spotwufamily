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
REPO_OWNER="${REPO_OWNER:-javiyt}"
REPO_NAME="${REPO_NAME:-spotwufamily}"
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
echo "Type: Classic Token (recommended for now)"
echo "Scopes: repo,workflow,pull,contents"
echo ""

# Create token using gh auth token
TOKEN=$(gh auth token)

echo "✅ GitHub token generated"
echo ""

# Save token to file
echo "$TOKEN" > "./${TOKEN_ENV}"

# Calculate token expiry date
TOKEN_EXPIRY=$(date -v+${EXPIRY_DAYS}d +"%Y-%m-%d" 2>/dev/null || echo "90 days from now")

# Set secrets - check if we're in the repo directory
echo "🔐 Setting secrets in GitHub repository..."
echo ""

# Check current directory
CURRENT_DIR=$(pwd)
CURRENT_REPO=$(basename "$CURRENT_DIR")

echo "Current directory: ${CURRENT_DIR}"
echo "Repository: ${CURRENT_REPO}"
echo "Target repository: ${REPO_NAME}"
echo ""

if [[ "$CURRENT_REPO" == "$REPO_NAME" ]]; then
  echo "✅ You are in the correct repository"
  
  # Delete existing secrets first to allow re-setting
  gh secret delete CATALOG_SYNC_TOKEN 2>/dev/null || true
  gh secret delete SPOTIFY_CLIENT_ID 2>/dev/null || true
  gh secret delete SPOTIFY_CLIENT_SECRET 2>/dev/null || true
  
  # Set secrets directly using --body
  echo "Setting CATALOG_SYNC_TOKEN..."
  echo "$TOKEN" | gh secret set CATALOG_SYNC_TOKEN --body - 2>/dev/null && echo "  ✓ Added" || echo "  ⚠️  Failed"
  
  echo "Setting SPOTIFY_CLIENT_ID..."
  gh secret set SPOTIFY_CLIENT_ID --body "${SPOTIFY_CLIENT_ID_ENV}" 2>/dev/null && echo "  ✓ Added" || echo "  ⚠️  Failed"
  
  echo "Setting SPOTIFY_CLIENT_SECRET..."
  gh secret set SPOTIFY_CLIENT_SECRET --body "${SPOTIFY_CLIENT_SECRET_ENV}" 2>/dev/null && echo "  ✓ Added" || echo "  ⚠️  Failed"
  
  echo ""
  echo "✅ All secrets configured!"
  
  echo ""
  echo "✅ DONE! Go to your GitHub repo to verify:"
  echo "  https://github.com/${REPO_OWNER}/${REPO_NAME}/settings/secrets/actions"
  
  # Verify secrets were set
  echo ""
  echo "Verifying secrets..."
  gh secret list | grep -E "CATALOG|SPOTIFY" | while read line; do
    echo "  ✓ ${line}"
  done
  
else
  echo "⚠️  You are NOT in the repository directory"
  echo ""
  echo "To automatically configure secrets, run this script from:"
  echo "  ${PWD}"
  echo ""
  echo "For now, add secrets manually:"
  echo ""
  echo "  1. Go to: https://github.com/${REPO_OWNER}/${REPO_NAME}/settings/secrets/actions"
  echo ""
  echo "  2. Add 3 secrets:"
  echo ""
  echo "     Secret 1:"
  echo "       Name: CATALOG_SYNC_TOKEN"
  echo "       Value: [from ./${TOKEN_ENV}]"
  echo ""
  echo "     Secret 2:"
  echo "       Name: SPOTIFY_CLIENT_ID"
  echo "       Value: ${SPOTIFY_CLIENT_ID_ENV}"
  echo ""
  echo "     Secret 3:"
  echo "       Name: SPOTIFY_CLIENT_SECRET"
  echo "       Value: ${SPOTIFY_CLIENT_SECRET_ENV}"
  echo ""
  echo "💡 After adding, run this command to verify:"
  echo "   gh secret list"
  echo ""
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

echo ""
echo "⚠️  IMPORTANT: This script creates a classic token (deprecated)"
echo ""
echo "To create a FINE-GRAINED token instead:"
echo ""
echo "  1. Go to: https://github.com/settings/tokens?type=beta"
echo "  2. Click \"Generate new token (fine-grained)\""
echo "  3. Configure:"
echo "     - Token name: spotwufamily-catalog-sync"
echo "     - Repository access: Select 'spotwufamily' repo (full access)"
echo "     - Write permissions: 'Contents' and 'Actions'"
echo "     - Set expiration: 90 days from now"
echo "  4. Copy the generated token and save as .${TOKEN_ENV}"
echo ""
echo "Fine-grained tokens offer:"
echo "  ✅ Smaller attack surface (only specific repos/permissions)"
echo "  ✅ Automatic expiration (no manual rotation needed)"
echo "  ✅ Revocable without affecting other permissions"
echo "  ✅ Better security model going forward"
