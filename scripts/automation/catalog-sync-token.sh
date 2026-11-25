#!/usr/bin/env bash
set -euo pipefail

TOKEN_ENV="${1:-}"

if [[ -z "${TOKEN_ENV}" ]]; then
  echo "Usage: catalog-sync-token.sh [token-file-name]"
  echo ""
  echo "Generate a GitHub PAT for catalog sync workflow."
  echo ""
  echo "Arguments:"
  echo "  token-file-name  Name of the file to save the token (default: .github-token)"
  echo ""
  echo "Scopes required:"
  echo "  repo      - full control of private repositories"
  echo "  workflow  - read and write access to workflows"
  echo "  pull      - access to public repositories via oauth apps"
  echo "  contents  - read and write contents"
  echo ""
  echo "Steps:"
  echo "  1. Run this script (it will open a browser or prompt for token creation)"
  echo "  2. Copy the generated token"
  echo "  3. Save it as a secret: CATALOG_SYNC_TOKEN in GitHub repository settings"
  exit 1
fi

echo "Generating GitHub PAT for catalog sync..."
echo ""

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

echo "You are logged in as: $(gh auth latest-user)"
echo ""

# Generate the token
echo "Creating Personal Access Token..."
echo "Please open: $(gh api user)"
echo "The token will be generated and saved to ./${TOKEN_ENV}"
echo ""

gh auth token --scopes "repo,workflow,pull,contents" > "./${TOKEN_ENV}"

echo ""
echo "✅ Token generated and saved to ./${TOKEN_ENV}"
echo ""
echo "Next steps:"
echo "  1. Read the token: cat ./${TOKEN_ENV}"
echo "  2. Create a secret in GitHub:"
echo "     - Go to: https://github.com/javiyt/spotwufamily/settings/secrets/actions"
echo "     - Click 'New repository secret'"
echo "     - Name: CATALOG_SYNC_TOKEN"
echo "     - Value: [paste the token]"
echo ""
echo "To renew the token when it expires (90 days):"
echo "  rm ./${TOKEN_ENV}"
echo "  ./scripts/automation/catalog-sync-token.sh"
