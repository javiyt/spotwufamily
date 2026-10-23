# Catalog drift and one-click "Update website" workflow (issue #290)

Supersedes the original text of issue #290 where they differ (see "Changes from the original ticket").

## Intent

After the operator edits `data/artists.yaml` (the editorial source of truth) through the admin UI, or by hand/CLI, they want **one confirmation** that takes the change all the way to an open pull request: local database reconciled, derived artifacts rebuilt, database release published, and a PR containing the YAML and release manifest so GitHub can publish the site once merged.

Today the operator must know which of reconcile / sync / verify / snapshot / export / release-prepare / release-publish to run, and in what order. Freshness is inferred from file mtimes, so it can be wrong in both directions.

## Goals

1. `/operations` shows a **Pending changes / Update website** panel: what changed in the YAML versus what SQLite last applied, which stages are required / optional / not needed, and the state of each stage.
2. Freshness is **content-digest based** with per-stage provenance, replacing the mtime inference in `SeededDigest`. Detects admin-UI edits and external edits identically, including mtime-preserving ones.
3. Concrete change listing: affected artist slugs; added/removed/reordered Spotify IDs; verified/enabled/relationship/metadata changes; number of pending reconciliation operations. No absolute or private paths.
4. **Bulk reconcile**: dry-run for all affected artists, then explicitly confirmed apply with per-artist results, backup and rollback, partial-failure reporting. Keeps the cross-process lock and stale-preview-token defenses. Not a blind loop over the single-artist action.
5. **One-click guided run** ("Update website"): review the full plan, confirm once, then the stages run sequentially as typed actions on the existing job manager: reconcile -> (seed/refresh) -> Spotify sync only if required -> db-verify -> snapshot -> deterministic export -> local Hugo build/audit (a manual stage: the panel shows the make target, the run does not execute it; CI builds the site on the PR) -> release-prepare -> release-publish -> **create branch, commit, push, open PR with `gh`**.
6. The run **stops at the first failure or cancellation** and reports exactly which stages completed. It is resumable by re-running: stages whose digests already match are skipped.
7. After completion, fresh checks run again. "Up to date" is shown only when actual digests match. The panel distinguishes "local site rebuilt", "release published", "PR open" and "published site updated" (the last only once GitHub confirms the Pages deploy; otherwise "waiting for merge/deploy").
8. Discovery: a banner (after verifying an artist or changing a group/relationship) linking to Operations with the pending count; works in a new browser session and after CLI edits.
9. Each stage's call to action is disabled with an explanation when its prerequisites fail (missing DB, no Spotify credentials / offline, `gh` not authenticated, release unavailable, dirty or conflicting git state).

## Non-goals

- Merging the PR, and anything after merge. The Pages deploy is observed (status shown if cheaply available), never triggered.
- Pushing to `main` or deploying Pages directly from the UI.
- Changing the release/manifest pipeline's safety guarantees or the Hugo/Pages workflow.
- A generic shell runner; only typed actions.

## Anti-goals (would be a failure even if it "works")

- Showing "up to date" after refresh/export without an actual reconcile/sync.
- Any step implying GitHub Pages already updated before deployment is confirmed.
- Wiping releases/tracks of unrelated artists during reconcile.
- Losing unsynced editorial changes.
- A stale preview, concurrent edit, restart, or cancelled/partial job producing a false success.
- Leaking local paths, tokens, or `gh` account details in UI, logs or job reports.

## Constraints

- Hexagonal layout: UI is an inbound adapter (`internal/adapters/inbound/httpadmin`) calling use cases under `internal/application/admin/maintenance`; no business rules in the adapter.
- Reuse existing actions: `reconcile[-dry-run]`, `sync[-dry-run]`, `db-verify`, `db-snapshot`, `export`, `refresh-export`, `release-prepare`, `release-publish`, and the job manager/journal. The cross-process lock, typed confirmation phrases and stale-preview tokens remain.
- `release-publish` creates the GitHub release; the new PR step must therefore run last and be the only new network/git-write stage after it. Because the release exists before the PR is merged, an unmerged PR leaves an unpinned release; the run summary must say so.
- Export and Hugo build stay deterministic. `make ci` must pass.
- Legacy databases (no recorded applied digest) are shown as "unknown / needs check", never assumed current.

## Decisions made together

- Single confirmation before the run; the confirmation screen shows the full plan, the branch name, the files to be committed and the PR target.
- The workflow stops at opening the PR.
- The release is created by the workflow (existing `release-publish`), then the PR is opened.

## Calls I made (flag if wrong)

- **PR contents:** `data/artists.yaml`, `data/artists.yaml.reviews.jsonl` (the review audit trail for the YAML change) and `data/catalog.release.json`. Nothing else is staged; other dirty files are left alone (only the three files are copied into the temporary worktree).
- **Git mechanics:** the PR is built in a temporary `git worktree` on a fresh `origin/main` (after `git fetch`): the three files are copied in, committed, pushed and the worktree removed. The operator's own working tree and branch are never switched or stashed. Repo and base branch come from the existing release config / `origin`; the confirmation screen shows only `owner/repo` and the base branch name. If the three files conflict with `origin/main` changes (e.g. someone else edited `artists.yaml` there) the run stops before publishing anything and says so.
- **Branch name and idempotency:** `catalog-update-<12 chars catalog digest>-<12 chars db content sha>`, deterministic. A re-run finds the existing branch/open PR through `gh` and reuses it (reports "PR already open"); a different digest yields a different branch. Release idempotency is already provided by `catalog-release.py` ("No catalog content changes; keeping current release"), and `release-publish` writes the final `data/catalog.release.json`, so the commit stage runs after it and a re-run never publishes a second release.
- **Plan bound to digests:** the confirmed plan records the catalog digest. Admin edits are blocked while a run is active using the existing catalog-busy mechanism, and the run aborts before the commit stage if the YAML digest changed (external edit); nothing is re-planned silently.
- **Bulk reconcile rollback:** all-or-nothing. One backup is taken under the lock; any failure restores it and the result lists per-artist outcomes (applied-then-rolled-back / failed / not attempted). A partial reconcile stops the composite run.
- **Applied-catalog record:** SQLite provenance stores a normalized snapshot of the applied catalog content (per artist: Spotify IDs, verified/enabled, relationships, metadata) plus its digest, so the change list is diffable. A legacy database has no baseline: the panel shows "unknown / needs check" and offers a baseline bulk dry-run instead of listing every artist as changed.
- **Sync-required rule:** changes to a profile's Spotify ID set, order, verified or enabled state require sync. Group member lists, relationships and metadata do not.
- **PR title/body:** generated from the change list (slugs and change categories, no paths, no account details).
- **Digest storage:** applied-catalog digest, and the DB digest used for snapshot/export/release, are stored in SQLite (small provenance table, migration adds it empty -> "unknown / needs check").
- **Spotify sync is required only when** Spotify IDs, verified/enabled state of a profile, or profile membership change; metadata-only and relationship-only edits need neither sync nor association reconcile (shown as "not needed"); they only need the database seed/refresh so SQLite matches the YAML.
- **Pending-change classification** is computed by diffing current YAML against the last applied catalog content recorded in SQLite provenance (not against git).

## Left to the builder

- Exact schema of the provenance table and digest canonicalization.
- Layout/wording of the panel and banner, job-view integration of the composite run.
- How the composite run persists progress across restarts (should reuse the job journal).
- How Pages deploy status is fetched (`gh run list`/deployments), if cheap; otherwise show "unknown, check GitHub".
- Test strategy details; acceptance criteria are those of the original ticket:
  YAML change detected regardless of mtime; multi-artist mapping change reconciled only after confirmation with backups/rollback verified; metadata-only changes do not trigger sync; missing DB / offline Spotify / unverified profiles / stale manifest / failed deployment have explicit statuses; refresh/export without sync cannot mark content fresh; restart, cancel, partial sync, stale previews and concurrent edits never yield false success.

## Changes from the original ticket

- Adds the PR-creation stage and the one-confirmation composite run (the original said "avoid combining with automatic publishing" and required confirmation per destructive/network/publish stage). Replaced by a single up-front confirmation of the full plan, stopping at the PR.
- Merge and Pages deploy remain out of scope.
