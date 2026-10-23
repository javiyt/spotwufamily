# Catalog drift and one-click "Update website" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show content-based catalog drift on `/operations` and let the operator confirm once to run reconcile -> sync (if needed) -> verify -> snapshot -> export -> build -> release -> PR.

**Architecture:** A SQLite provenance table records the normalized catalog last applied and per-stage checkpoints (replacing the mtime heuristic in `SeededDigest`). A pure diff classifies changes into required/optional/not-needed stages. Bulk reconcile and a PR-opening action are new typed `maintenance.Action`s; an `update-website` composite action orchestrates existing actions through the existing job manager. The httpadmin adapter only renders.

**Tech Stack:** Go, SQLite (`migrations/`), `html/template`, `gh` CLI, `git worktree`, existing `jobs.Manager`.

**Spec:** `docs/superpowers/specs/2026-10-22-catalog-drift-and-update-website-design.md`

## Global Constraints

- UI is an inbound adapter; business rules live in `internal/application/admin/maintenance` and `internal/application/artists`.
- Reuse actions `reconcile[-dry-run]`, `sync[-dry-run]`, `db-verify`, `db-snapshot`, `export`, `refresh-export`, `release-prepare`, `release-publish`; keep the cross-process lock, typed confirmation phrases and stale-preview tokens.
- No absolute paths, tokens or `gh` account details in UI, logs, job reports, PR text.
- Bulk reconcile is all-or-nothing: one backup, any failure restores it.
- Spotify sync required only when a profile's Spotify ID set/order or verified/enabled state changes; group members, relationships, metadata do not.
- Run stops at opening the PR; never merges, never pushes to `main`.
- PR contains only `data/artists.yaml`, `data/artists.yaml.reviews.jsonl`, `data/catalog.release.json`, built in a temporary `git worktree` on fresh `origin/main`; operator's working tree is never switched.
- Branch name `catalog-update-<12 chars catalog digest>-<12 chars db content sha>`; re-run reuses existing branch/open PR.
- Admin edits blocked while a run is active; run aborts before commit if the YAML digest differs from the confirmed plan.
- Legacy DB without provenance = "unknown / needs check".
- Export and Hugo build stay deterministic; `make ci` passes.

## Review Focus

- YAML edited by hand with mtime preserved (`touch -r`) -> still detected as drift.
- Legacy DB with no provenance -> "unknown / needs check", not "up to date", not every artist "changed".
- `refresh-export` or `export` run without sync/reconcile -> catalog still shown stale.
- Job cancelled or process restarted between reconcile and publish -> no stage shown done that did not finish; re-run skips only stages whose digests match.
- Re-run after release published but PR failed -> no second release, existing branch/PR reused.
- YAML edited during a confirmed run -> run aborts before commit.
- `gh` unauthenticated, offline Spotify, missing DB, unverified profile -> each stage disabled with a distinct reason.

---

### Task 1: Provenance storage

**Files:**
- Create: `migrations/0007_catalog_provenance.sql`, `internal/adapters/outbound/sqlite/provenance.go`, `internal/adapters/outbound/sqlite/provenance_test.go`
- Modify: `internal/adapters/outbound/sqlite/migrations.go` only if migrations are not auto-discovered

**Interfaces:**
- Produces (package `artists`, file `internal/application/artists/provenance.go`):
  - `type AppliedCatalog struct { Digest string; Artists []ArtistFingerprint; AppliedAt string }`
  - `type StageCheckpoint struct { Stage, CatalogDigest, DBDigest, At string }`
  - `type ProvenanceStore interface { AppliedCatalog(ctx) (AppliedCatalog, bool, error); RecordApplied(ctx, AppliedCatalog) error; Checkpoint(ctx, stage string) (StageCheckpoint, bool, error); RecordCheckpoint(ctx, StageCheckpoint) error }`
  - Stage constants: `StageReconcile, StageSync, StageSnapshot, StageExport, StageRelease`.
  - `ArtistFingerprint{Slug string; SpotifyIDs []string; Verified, Enabled bool; Relationships []string; Metadata string}` (Metadata = hash of non-Spotify editorial fields).

- [ ] **Step 1:** Write `TestProvenanceRoundTrip` and `TestProvenanceEmptyOnLegacyDB` (migration on a DB at version 6 yields `ok=false`, no error).
- [ ] **Step 2:** Run `go test ./internal/adapters/outbound/sqlite -run Provenance` — expect FAIL (undefined).
- [ ] **Step 3:** Add migration with tables `catalog_applied(id=1 singleton, digest, artists_json, applied_at)` and `stage_checkpoints(stage PK, catalog_digest, db_digest, at)`; implement `ProvenanceStore` on the sqlite `Database`.
- [ ] **Step 4:** Rerun — PASS. Also run `go test ./internal/adapters/outbound/sqlite` (migration checksum tests).
- [ ] **Step 5:** Commit `feat(db): add catalog provenance tables`.

### Task 2: Catalog fingerprint and change classification

**Files:**
- Create: `internal/application/artists/catalog_diff.go`, `internal/application/artists/catalog_diff_test.go`

**Interfaces:**
- Consumes: `ArtistFingerprint`, `AppliedCatalog` (Task 1); existing `catalog` types in `internal/application/artists/catalog.go`.
- Produces:
  - `func FingerprintCatalog(c Catalog) AppliedCatalog` (canonical, order-stable per artist, digest = sha256 of canonical JSON)
  - `type ChangeKind string` (`added, removed, ids_added, ids_removed, ids_reordered, verified, enabled, relationships, metadata`)
  - `type ArtistChange struct { Slug string; Kinds []ChangeKind; NeedsReconcile, NeedsSync bool }`
  - `func DiffCatalog(applied, current AppliedCatalog) []ArtistChange` sorted by slug.

- [ ] **Step 1:** Table test `TestDiffCatalog` with cases: IDs added -> NeedsReconcile+NeedsSync; reordered IDs -> both; verified toggled -> sync; relationships only -> neither sync (reconcile false too); metadata only -> neither; artist added/removed; identical -> empty.
- [ ] **Step 2:** Run — FAIL. **Step 3:** Implement. **Step 4:** PASS. **Step 5:** Commit.

### Task 3: Content-based freshness with unknown state

**Files:**
- Modify: `internal/application/admin/maintenance/freshness.go`, `internal/bootstrap/maintenance_freshness.go`, tests `freshness_test.go`, `maintenance_freshness_test.go`

**Interfaces:**
- Consumes: `ProvenanceStore`.
- Produces: `Freshness.SeedUnknown bool` (legacy DB), `Freshness.Pending []artists.ArtistChange`; `FreshnessSource` gains `AppliedChanges() ([]artists.ArtistChange, applied bool)`; `SeededDigest` now returns the digest recorded in `catalog_applied` (ok=false when none). Remove the mtime logic.

- [ ] **Step 1:** Tests: `TestSeededDigestIgnoresMtime` (edit YAML preserving mtime -> `SeedNeeded`), `TestLegacyDBIsUnknown` (no provenance -> `SeedUnknown`, `SeedNeeded` false, reason "Catalog state of the database is unknown; run a check"), `TestExportWithoutReconcileStaysStale`.
- [ ] **Step 2–4:** Fail, implement, pass. **Step 5:** Commit.

### Task 4: Record provenance from existing actions

**Files:**
- Modify: sync, reconcile-apply, snapshot, export/refresh-export, release-prepare/publish action wiring in `internal/bootstrap/maintenance_actions.go`, `maintenance_reconcile.go`, `maintenance_release.go` and the corresponding `actions_*.go`.

**Interfaces:** each successful action calls `ProvenanceStore.RecordCheckpoint` with the digests it actually used; successful sync and reconcile-apply (only when the catalog digest still equals the one planned) call `RecordApplied`. A partial/failed/cancelled run records nothing.

- [ ] **Step 1:** Tests per action: success records; failure/cancel/partial does not; `export` alone leaves `SeedNeeded` true (Review Focus 3).
- [ ] **Step 2–4:** Fail, implement, pass; run `go test ./internal/...`. **Step 5:** Commit.

### Task 5: Bulk reconcile (dry-run and apply)

**Files:**
- Create: `internal/application/admin/maintenance/actions_reconcile_all.go` (+ `_test.go`)
- Modify: `internal/bootstrap/maintenance_reconcile.go`, `internal/adapters/inbound/httpadmin/operations.go` (action metadata)

**Interfaces:**
- Consumes: `ReconcilePort`, `DiffCatalog`.
- Produces: `NewReconcileAllDryRun(ReconcileDeps) Action` (`reconcile-all-dry-run`, no params) and `NewReconcileAllApply(ReconcileDeps) Action` (`reconcile-all-apply`, typed phrase `reconcile all`). Preview fingerprint = hash of per-artist plan checksums + catalog digest. Apply: takes the lock, migrates, one backup, recomputes every plan, refuses a changed fingerprint, applies all in one transaction (extend `ReconcilePort` with `ApplyAll(ctx, checksum string) ([]artists.ReconcileReport, error)`); on error restore the backup and report per-artist outcomes `rolled_back / failed / not_attempted`.

- [ ] **Step 1:** Tests with a fake port: stale fingerprint refused; failure in artist 2 of 3 restores and reports three outcomes; cancel mid-run restores; clean plan takes no backup; unrelated artists' releases/tracks untouched (use real sqlite test in `internal/bootstrap`).
- [ ] **Step 2–4:** Fail, implement, pass. **Step 5:** Commit.

### Task 6: Release PR action

**Files:**
- Create: `internal/application/admin/maintenance/actions_releasepr.go` (+ test), `internal/bootstrap/maintenance_releasepr.go` (git/gh adapter + test with a temp bare remote and fake `gh`)

**Interfaces:**
- Produces: `type PRPort interface { Check(ctx) (PRPrereq, error); Open(ctx, PRRequest) (PRResult, error) }`; `PRRequest{Branch, Title, Body string; Files []string}`; `PRResult{URL string; Reused bool}`; `NewReleasePR(PRDeps) Action` named `release-pr`, typed phrase `open pull request`, `UsesNetwork`. Available only when `gh` authenticated, `origin` resolvable, manifest pinned to the local content hash. `Open`: fetch, `git worktree add` on `origin/main`, copy the three files, abort on conflict with changed `origin/main` versions, commit, push, `gh pr create` unless an open PR for the branch exists, always remove the worktree. Title/body come from the change list.

- [ ] **Step 1:** Tests: creates branch+commit with exactly three files; second call returns `Reused=true`; conflict aborts with nothing pushed; worktree removed on failure; operator working tree unchanged; no paths in result text.
- [ ] **Step 2–4:** Fail, implement, pass. **Step 5:** Commit.

### Task 7: Pending changes service and composite run

**Files:**
- Create: `internal/application/admin/maintenance/pending.go`, `update_website.go` (+ tests)
- Modify: `service.go` (register), `internal/bootstrap/admin_maintenance.go`

**Interfaces:**
- Produces: `func (s *Service) Pending(ctx) (PendingView, error)` where `PendingView{State (unknown|clean|pending); Changes []artists.ArtistChange; Stages []StageView}` and `StageView{Name, Need (required|optional|not_needed), Status (done|todo|blocked|unknown), BlockedReason string}` for the ordered stages in the spec; action `update-website` (typed phrase `update website`) whose preview lists the plan, branch and files; its job runs the stages through the existing actions, records the confirmed catalog digest, aborts before `release-pr` if the digest changed, skips stages whose checkpoint digests match, stops on first failure/cancel, and reports completed stages.

- [ ] **Step 1:** Tests: metadata-only change -> sync `not_needed`; ID change -> sync `required`; missing DB / no `gh` / no Spotify credentials -> `blocked` with distinct reasons; YAML changed mid-run -> abort before PR; cancel after reconcile -> result lists reconcile done, rest not run; restart (fresh service over same DB) -> done stages skipped only when digests match.
- [ ] **Step 2–4:** Fail, implement, pass. **Step 5:** Commit.

### Task 8: Operations panel, banner and API

**Files:**
- Modify: `internal/adapters/inbound/httpadmin/operations.go`, `templates/operations.gohtml`, `templates/layout.gohtml`, `templates/artist.gohtml`/`review.gohtml` (post-verify link), `static/` css; tests `operations_test.go`
- API: `GET /api/v1/pending`

**Interfaces:** Consumes `Service.Pending` via the `Operator` interface (optional `PendingProvider`, like `Publisher`). Panel shows state, change list, ordered stage checklist with required/optional/not-needed, disabled buttons with reasons, "Review plan" -> existing preview page for `update-website`, and the honest labels "local site rebuilt / release published / PR open / waiting for merge and Pages deploy". Banner on all admin pages when pending > 0, text "N artists modified since last DB reconciliation", linking to `/operations#pending`; computed per request from a cheap cached digest compare so it works in a new session.

- [ ] **Step 1:** Handler tests: panel renders pending artists and no paths; banner appears for CLI-edited YAML in a new session; disabled-with-reason rendering; "up to date" only when `Pending.State == clean`.
- [ ] **Step 2–4:** Fail, implement, pass. **Step 5:** Commit.

### Task 9: Docs and end-to-end

**Files:** Modify `docs/admin.md`, `docs/operations.md`; add an e2e-style test in `internal/bootstrap` running the whole composite against a temp repo with fake `gh`.

- [ ] **Step 1:** Write the e2e test (edit YAML externally -> pending -> `update-website` -> branch, release manifest and PR URL; second run reports nothing to do).
- [ ] **Step 2:** Document the workflow, the unknown state, what each stage does, that merge/Pages deploy happen on GitHub, and the unpinned-release caveat if the PR is never merged.
- [ ] **Step 3:** Run `make ci` — expect exit 0. **Step 4:** Commit and push branch `290-admin-show-catalog-drift-and-guide-reconcile-sync-export-and-web-publication-after-editorial-changes`.

---

## Self-review

Spec coverage: detection (T1–3), concrete changes (T2, T8), bulk reconcile with rollback (T5), prerequisites/disabled reasons (T7–8), composite single-confirm run and PR (T6–7), honest status and banner (T8), docs/CI (T9). Names `ArtistChange`, `AppliedCatalog`, `ProvenanceStore`, `PRPort` are used consistently.
