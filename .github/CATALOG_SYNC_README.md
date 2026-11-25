# GitHub Token for Catalog Sync

This secret is used by the `catalog-sync` workflow to push changes to GitHub and sync with Spotify.

## Quick Setup (GitHub Actions)

Run this script directly from GitHub Actions to automatically set all secrets:

```bash
# Set environment variables
export SPOTIFY_CLIENT_ID="${SPOTIFY_CLIENT_ID}"
export SPOTIFY_CLIENT_SECRET="${SPOTIFY_CLIENT_SECRET}"

# Run the script (auto-sets all secrets)
./scripts/automation/catalog-sync-token.sh
```

**Requirements:**
- Environment variables must be set in GitHub Actions:
  - `SPOTIFY_CLIENT_ID`
  - `SPOTIFY_CLIENT_SECRET`

The script will automatically:
1. Generate GitHub PAT token
2. Save as secret: `CATALOG_SYNC_TOKEN`
3. Save as secret: `SPOTIFY_CLIENT_ID`
4. Save as secret: `SPOTIFY_CLIENT_SECRET`

## Quick Setup (Local)

If you prefer to set secrets manually:

```bash
# Generate token and save to file
./scripts/automation/catalog-sync-token.sh

# The script will output instructions for manual setup
cat .github-token
```

## Smart Token Management

The script intelligently handles existing tokens:

1. **No overwrite** - Existing tokens (< 90 days old) are preserved
2. **Auto-regenerate** - Old tokens (> 90 days) are regenerated automatically
3. **Force flag** - Use `--force` to regenerate regardless of age

### Examples

```bash
# Generate new token
./scripts/automation/catalog-sync-token.sh

# Check if token needs renewal
./scripts/automation/catalog-sync-token.sh
# Output: "Token is less than 90 days old. Skipping regeneration."

# Force regenerate even if token is recent
./scripts/automation/catalog-sync-token.sh --force
```

## Manual Setup

### 1. Generate GitHub Token

```bash
./scripts/automation/catalog-sync-token.sh
```

This will:
- Check if `gh CLI` is installed
- Verify you're logged in
- Create a token with scopes: `repo,workflow,pull,contents`
- Save it to `.github-token`

### 2. Add Secrets to GitHub

Go to: https://github.com/javiyt/spotwufamily/settings/secrets/actions

Add these secrets:

| Name | Value | Description |
|------|-------|-------------|
| `CATALOG_SYNC_TOKEN` | [from `.github-token`] | GitHub PAT for workflow |
| `SPOTIFY_CLIENT_ID` | [from env] | Spotify Client ID |
| `SPOTIFY_CLIENT_SECRET` | [from env] | Spotify Client Secret |

## Required Scopes

The GitHub PAT requires:
- `repo` - Full control of private repositories
- `workflow` - Read and write access to workflows
- `pull` - Access to public repositories via oauth apps
- `contents` - Read and write repository contents

## Environment Variables

### For Local Script Execution

Set these before running the script:

```bash
export SPOTIFY_CLIENT_ID="your-spotify-client-id"
export SPOTIFY_CLIENT_SECRET="your-spotify-client-secret"
./scripts/automation/catalog-sync-token.sh
```

### For GitHub Actions

Set these in repository secrets (they can be read by workflows):

```yaml
# In .github/workflows/ci.yml
env:
  SPOTIFY_CLIENT_ID: ${{ secrets.SPOTIFY_CLIENT_ID }}
  SPOTIFY_CLIENT_SECRET: ${{ secrets.SPOTIFY_CLIENT_SECRET }}
```

## Token Rotation

GitHub PATs expire after 90 days. The script automatically checks and regenerates:

```bash
# Script will auto-renew if token is older than 90 days
./scripts/automation/catalog-sync-token.sh

# Force regenerate even if token is recent
./scripts/automation/catalog-sync-token.sh --force

# Manual renewal
rm .github-token
./scripts/automation/catalog-sync-token.sh
```

## Using in Workflows

```yaml
jobs:
  sync:
    runs-on: ubuntu-latest
    env:
      CATALOG_SYNC_TOKEN: ${{ secrets.CATALOG_SYNC_TOKEN }}
      SPOTIFY_CLIENT_ID: ${{ secrets.SPOTIFY_CLIENT_ID }}
      SPOTIFY_CLIENT_SECRET: ${{ secrets.SPOTIFY_CLIENT_SECRET }}
```

## Troubleshooting

### Token rejected

- ✅ Make sure token hasn't expired (90 days)
- ✅ Check all scopes are enabled
- ✅ Delete old secret in GitHub and recreate with new token

### Spotify API errors

- ✅ Verify Spotify app is active: https://developer.spotify.com/dashboard
- ✅ Check Client ID matches the app
- ✅ Verify Client Secret is correct (check app settings)

### gh CLI errors

```bash
# Install gh CLI
brew install github

# Login to GitHub
gh auth login

# Verify login
gh auth status
```

## Security

- ✅ **Never share the token** in code, logs, or chat
- ✅ **Rotate the token** every 60-90 days
- ✅ **Token file is ignored** by git (in `.gitignore`)
- ✅ **Use environment variables** in code to access values

## Files

- `scripts/automation/catalog-sync-token.sh` - Token generation script
- `.github/CATALOG_SYNC_README.md` - This documentation
- `.github-token` - Generated token file (auto-created)
