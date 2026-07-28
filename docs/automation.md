# Automation

Planned workflows:

- `ci.yml`: validate catalog, test, vet and build.
- `catalog-sync.yml`: scheduled Spotify sync that opens or updates one catalog PR.
- `catalog-pr-review.yml`: restricted approval from a separate identity for generated data-only PRs.
- `pages.yml`: Hugo build and GitHub Pages deployment after merge to `main`.

Current local build command:

```bash
go run ./cmd/spotwufamily site build
```

The site is configured for `https://javiyt.github.io/spotwufamily/` and must keep links working under that subpath.

The sync identity and review identity must be separate GitHub Apps or bot credentials when branch protection requires review approval.
