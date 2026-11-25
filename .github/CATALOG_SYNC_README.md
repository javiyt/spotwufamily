# GitHub Token for Catalog Sync

This secret is used by the `catalog-sync` workflow to push changes to GitHub.

## Token Generation

Use the provided script to generate a new token:

```bash
# Generate token and save to .github-token
./scripts/automation/catalog-sync-token.sh

# Generate token and save to custom name
./scripts/automation/catalog-sync-token.sh my-super-secret-token
```

## Requirements

- GitHub CLI installed: `brew install github` (macOS) or `snap install github-cli --classic` (Linux)
- Logged in to GitHub: `gh auth login`

## Scopes

The token requires the following scopes:
- `repo` - Full control of private repositories
- `workflow` - Read and write access to workflows
- `pull` - Access to public repositories via oauth apps
- `contents` - Read and write repository contents

## Adding the Secret

1. Open GitHub repository settings: https://github.com/javiyt/spotwufamily/settings/secrets/actions
2. Click "New repository secret"
3. Name: `CATALOG_SYNC_TOKEN`
4. Value: Copy the token from the generated file

## Token Rotation

GitHub PATs expire after 90 days. To renew:

```bash
# Remove old token
rm .github-token

# Generate new token
./scripts/automation/catalog-sync-token.sh

# Add the new secret value in GitHub settings
```

## Troubleshooting

### Token rejected

- Make sure the token hasn't expired (90 days from creation)
- Check that all scopes are enabled
- Delete the old secret in GitHub settings and recreate it with the new token

### Permission denied

- Verify the token has been added to GitHub secrets
- Check repository visibility (token won't work for organization repositories without proper settings)
- Ensure the user has access to the repository

## Security

- **Never share the token** in code, logs, or chat
- **Rotate the token** regularly (every 60-90 days)
- Use environment variables when referencing: `CATALOG_SYNC_TOKEN`
- Store the token file in `.gitignore`: `.github-token`
