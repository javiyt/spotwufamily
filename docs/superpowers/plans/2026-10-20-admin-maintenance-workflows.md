# Admin Safe Maintenance Workflows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the local admin UI run discovery, profile audit, metadata refresh, sync, snapshot/verify, export, reconciliation and a manual catalog release as previewed, confirmed, serialised, cancellable background jobs.

**Architecture:** A new transport-free `internal/application/jobs` package owns job lifecycle, resource locks and restart recovery behind a `Store` port. A new `internal/application/admin/maintenance` package defines typed actions (`Preview` then `Run`) that call existing use cases directly. `httpadmin` only renders pages and JSON. Release publishing goes through one fixed-argument adapter around `scripts/automation/catalog-release.py`.

**Tech Stack:** Go (stdlib `html/template`, `net/http`, `encoding/csv`), SQLite adapter, existing YAML store, Python release script, `gh` CLI (publish only).

**Spec:** https://github.com/javiyt/spotwufamily/issues/271 (ticket text supplied by the user; no separate design doc). Decisions made without the user are marked **[my call]**.

## Global Constraints

- Hexagonal: `application/*` must not import `net/http`, YAML or Spotify transport packages; adapters are wired in `internal/bootstrap`.
- No subprocess string concatenation. The only subprocess is the release adapter: fixed executable (`python3`), fixed script path, fixed subcommand set (`prepare`, `publish`), no user-supplied flags. **[my call]**
- One active job per conflicting resource. Resource keys: `catalog-yaml`, `database`, `release`.
- Every write validates and backs up first: YAML via existing `.bak`; DB via `VACUUM INTO build/admin/backups/<kind>-<UTC>.db` before sync, reconcile apply and discovery import.
- Never publish a GitHub release, push, or open a PR except from the explicit `/publish` confirm step.
- Never expose secrets, raw SQL, filesystem paths or stack traces in responses, reports or job messages (log server-side only).
- Preserve: Spotify 429 `Retry-After`/`PartialSyncError` + `--resume` checkpoints, SQLite snapshot verification, release manifest pinning (`data/catalog.release.json`), existing CLI behaviour and deterministic exports.
- No network or Spotify credentials in tests; use fakes.
- Reports: CSV/JSON, rows sorted deterministically, no timestamps inside report bodies.
- `make ci` must pass at the end.

## Review Focus

- Server restart mid-sync: the job must read `interrupted`, never `succeeded`, and offer resume (test in Task 2, Task 8).
- Double-submitted confirm (double click): second request gets a conflict, not a second run (Task 1, Task 15).
- Confirm token replayed after the catalog or DB changed since preview: refused as stale (Task 6).
- Cancel during export/snapshot: no half-written artifact is left behind (Task 9).
- Missing Spotify credentials or offline: network actions are disabled with an explanation, offline audit and verify still work (Task 7, Task 15).
- `gh` unauthenticated or `data/catalog.release.json` already pinned to the same content: clear prerequisite / "no changes" message and no manifest copy (Task 12).

---

### Task 1: Job manager core

**Files:**
- Create: `internal/application/jobs/job.go`, `internal/application/jobs/manager.go`, `internal/application/jobs/manager_test.go`

**Interfaces:**
- Produces:
  - `type Status string` with `StatusQueued, StatusRunning, StatusCancelling, StatusSucceeded, StatusPartial, StatusFailed, StatusCancelled, StatusInterrupted`.
  - `type Job struct { ID, Kind string; Status Status; Step string; Done, Total int; Counts map[string]int; Message string; Resources []string; ResumeOf string; Resumable bool; CreatedAt, StartedAt, FinishedAt time.Time; Result *Result }`
  - `type Result struct { Summary string; Report *Report }` where `Report` is defined in Task 3 (use a forward type in `jobs`: `type Report struct { Name string; Columns []string; Rows [][]string }`; move nothing later).
  - `type Reporter interface { Step(name string); Progress(done, total int); Count(key string, delta int) }`
  - `type Func func(ctx context.Context, r Reporter) (Outcome, error)`; `type Outcome struct { Status Status; Result Result; Resumable bool }` (Status limited to Succeeded/Partial).
  - `type Spec struct { Kind string; Resources []string; ResumeOf string; Run Func }`
  - `type Store interface { Save(ctx context.Context, j Job) error; Load(ctx context.Context) ([]Job, error) }`
  - `func NewManager(store Store, now func() time.Time, newID func() string) *Manager`
  - `(*Manager) Submit(ctx context.Context, s Spec) (Job, error)` returns `ErrConflict` (wrapping the holder's job ID via `*ConflictError{HolderID string}`) when any resource is held.
  - `(*Manager) Get(id string) (Job, bool)`, `List() []Job` (newest first), `Cancel(id string) error`, `Recover(ctx context.Context) error`, `Wait(id string)` (test helper).

- [ ] **Step 1: Write failing tests** in `manager_test.go` using a fake `Store` and a blocking fake `Func`: `TestSubmitRunsAndRecordsProgress`, `TestConflictingResourceRejected` (assert `errors.As(err, *ConflictError)`), `TestDisjointResourcesRunConcurrently`, `TestCancelMarksCancelledWithPartialCounts`, `TestFailedJobSanitisesMessage` (error text containing `/Users/x/secret` surfaces as the generic message `operation failed`), `TestPanicBecomesFailed`, `TestResourceReleasedAfterFinish`.
- [ ] **Step 2: Run** `go test ./internal/application/jobs/ -run . -v` → FAIL (package missing).
- [ ] **Step 3: Implement** the types above. Run each job in its own goroutine under a derived cancellable context; hold a `map[resource]jobID` under a mutex; persist via `Store.Save` on every status change. Errors from `Func` are mapped through an injectable `Sanitize func(error) string` defaulting to `"operation failed"` unless the error implements `interface{ Safe() string }`.
- [ ] **Step 4: Run** `go test ./internal/application/jobs/ -race` → PASS.
- [ ] **Step 5: Commit** `feat(admin): add job manager with resource locking`.

### Task 2: Job journal and restart recovery

**Files:**
- Create: `internal/adapters/outbound/filesystem/jobjournal.go`, `internal/adapters/outbound/filesystem/jobjournal_test.go`
- Modify: `internal/application/jobs/manager_test.go`

**Interfaces:**
- Consumes: `jobs.Store`, `jobs.Job`.
- Produces: `func NewJobJournal(path string) *JobJournal` implementing `jobs.Store`. Append-only JSONL; `Load` returns the last record per job ID; a corrupt trailing line is ignored.

- [ ] **Step 1: Tests** `TestJournalLoadReturnsLatestPerID`, `TestJournalIgnoresTruncatedLastLine`; in manager tests `TestRecoverMarksRunningAndQueuedInterrupted` (job saved as `running` with `Resumable=true` reloads as `interrupted`, still `Resumable`, `FinishedAt` set, resources not held) and `TestRecoverKeepsTerminalJobs`.
- [ ] **Step 2: Run** `go test ./internal/adapters/outbound/filesystem/ ./internal/application/jobs/` → FAIL.
- [ ] **Step 3: Implement** the journal (mutex, `O_APPEND`, fsync per write, file mode 0600, directory 0700) and `Manager.Recover`.
- [ ] **Step 4: Run** same → PASS. **Step 5: Commit** `feat(admin): persist job journal and recover interrupted jobs`.

### Task 3: Safe errors and deterministic reports

**Files:**
- Create: `internal/application/admin/maintenance/report.go`, `internal/application/admin/maintenance/safeerr.go`, `internal/application/admin/maintenance/report_test.go`

**Interfaces:**
- Produces: `func (r jobs.Report)` helpers kept in maintenance as free functions: `func WriteCSV(w io.Writer, r jobs.Report) error`, `func WriteJSON(w io.Writer, r jobs.Report) error` (object `{name, columns, rows:[{col:val}]}`, keys in column order, 2-space indent, trailing newline), `func SortRows(r *jobs.Report)` (lexicographic by all columns). `type SafeError struct{ Code, Message string }` implementing `Safe() string`; `func Safe(err error) error` maps known application errors (rate limit with retry-after seconds, credentials missing, conflict, stale preview, validation) to `SafeError` and everything else to code `internal`.

- [ ] **Step 1: Tests** `TestCSVDeterministic` (same rows in different input order → identical bytes), `TestCSVEscapesFormulaInjection` (cell starting `=`, `+`, `-`, `@` is prefixed with `'`), `TestSafeStripsPathsAndSQL` (error "open /Users/a/data.db: no such table x" → message has no `/`, no `SQL`), `TestSafeKeepsRetryAfter`.
- [ ] **Step 2: Run** `go test ./internal/application/admin/maintenance/` → FAIL. **Step 3: Implement. Step 4: PASS.**
- [ ] **Step 5: Commit** `feat(admin): add deterministic reports and safe error mapping`.

### Task 4: Share sync/refresh/discover wiring with the CLI

**Files:**
- Create: `internal/bootstrap/maintenance.go`, `internal/bootstrap/maintenance_test.go`
- Modify: `internal/adapters/inbound/cli/cli.go` (the `syncFetcher`, partial-snapshot-on-abort and refresh-genres assembly)

**Interfaces:**
- Produces in `bootstrap`: `type SyncDeps struct{ Run func(ctx context.Context, o catalogsync.Options) (catalogsync.Result, error) }` built by `func NewSyncRunner(catalogPath, dbPath, snapshotPath string, env EnvLookup) (SyncRunner, func(), error)`. `SyncRunner.Run` performs sync **and**, on `*catalogsync.PartialSyncError`, writes the partial snapshot exactly as the CLI does, returning the error unchanged. Same for `NewRefreshRunner` (wrapping `artists.RefreshGenres`) and `NewDiscoverRunner` (wrapping `artists.DiscoverWuFamily`).

- [ ] **Step 1: Characterisation tests**: run the existing CLI tests first (`go test ./internal/adapters/inbound/cli/`) and record they pass. Add `TestSyncRunnerWritesPartialSnapshotOnRateLimit` with a fake fetcher returning a rate-limit abort.
- [ ] **Step 2: Move** the assembly code out of `cli.go` into `bootstrap`; CLI calls the new constructors. No flag-parsing changes.
- [ ] **Step 3: Run** `go test ./internal/bootstrap/ ./internal/adapters/inbound/cli/` → PASS (CLI behaviour unchanged).
- [ ] **Step 4: Commit** `refactor: share sync, refresh and discovery wiring between CLI and admin`.

### Task 5: Database backup and cross-process lock

**Files:**
- Create: `internal/adapters/outbound/sqlite/backup.go`, `internal/adapters/outbound/sqlite/backup_test.go`, `internal/adapters/outbound/filesystem/resourcelock.go` (+ `_unix.go`/`_other.go` mirroring `yaml/lock_unix.go`/`lock_other.go`), `internal/adapters/outbound/filesystem/resourcelock_test.go`
- Modify: `internal/adapters/inbound/cli/cli.go` (sync and `artists reconcile --apply` take the lock)

**Interfaces:**
- Produces: `func (d *Database) BackupTo(ctx context.Context, dir string) (string, error)` (VACUUM INTO `<dir>/<base>-<UTC yyyymmddThhmmssZ>.db`, mode 0600, never overwrites, verifies the copy with the existing verify routine, returns the file name only). `func NewResourceLock(path string) *ResourceLock` with `TryLock() (release func(), err error)` returning `ErrLocked` when another process holds it.

- [ ] **Step 1: Tests** `TestBackupToCreatesVerifiableCopy`, `TestBackupToNeverOverwrites`, `TestResourceLockExcludesSecondHolder`, `TestResourceLockReleasable`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** CLI `sync` and `reconcile --apply` call `TryLock` on `<db>.lock` and print `another operation holds the database` on `ErrLocked`, exit non-zero.
- [ ] **Step 4: Run** `go test ./internal/adapters/...` → PASS. **Step 5: Commit** `feat: add database backup and cross-process resource lock`.

### Task 6: Maintenance action framework with preview tokens

**Files:**
- Create: `internal/application/admin/maintenance/action.go`, `internal/application/admin/maintenance/token.go`, `internal/application/admin/maintenance/service.go`, `internal/application/admin/maintenance/service_test.go`

**Interfaces:**
- Consumes: `jobs.Manager`, `jobs.Spec`.
- Produces:
  - `type Params map[string]string` (typed accessors per action; unknown keys rejected by `Action.Validate`).
  - `type Preview struct { Action string; Title string; Scope []ScopeLine; Warnings []string; Destructive, UsesNetwork bool; Fingerprint string; RequiresTyped string }` (`RequiresTyped` is the phrase the user must type, empty for non-destructive).
  - `type Action interface { Name() string; Validate(Params) error; Preview(ctx context.Context, p Params) (Preview, error); Spec(ctx context.Context, p Params) (jobs.Spec, error); Available() (bool, string) }`
  - `type Service struct`; `func NewService(m *jobs.Manager, actions []Action, secret []byte, now func() time.Time) *Service`; `(*Service) Preview(ctx, name string, p Params) (Preview, Token, error)`; `(*Service) Run(ctx, name string, p Params, tok Token, typed string) (jobs.Job, error)`; `(*Service) Actions() []ActionInfo`.
  - `Token` = base64 HMAC-SHA256 over `name|canonical(p)|Fingerprint|expiry`, TTL 10 minutes. `ErrStalePreview`, `ErrConfirmationRequired`, `ErrUnavailable`.

- [ ] **Step 1: Tests** with a fake action: `TestRunRequiresValidToken`, `TestRunRefusesExpiredToken`, `TestRunRefusesWhenFingerprintChanged` (re-preview yields different fingerprint → `ErrStalePreview`), `TestDestructiveRequiresTypedPhrase`, `TestUnavailableActionRefused`, `TestUnknownParamRejected`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** `Run` recomputes the fingerprint via `Preview` and compares before calling `jobs.Manager.Submit`.
- [ ] **Step 4: PASS** (`-race`). **Step 5: Commit** `feat(admin): add previewed and confirmed maintenance actions`.

### Task 7: Discovery, profile audit and metadata refresh actions

**Files:**
- Create: `internal/application/admin/maintenance/actions_discover.go`, `actions_audit.go`, `actions_refresh.go`, and matching `_test.go` files

**Interfaces:**
- Consumes: Task 4 runners (`DiscoverRunner`, `RefreshRunner`), `artists.NewAuditProfiles`.
- Produces actions named: `discover-preview`, `discover-import` (Params: none; import uses `Apply: true`, takes a DB backup is N/A, writes YAML ledger + catalog through the existing store so `.bak` applies; resource `catalog-yaml`; uses Wikipedia network → `UsesNetwork`), `audit-profiles` (Params: `artist` optional slug, `offline` `true|false`; read-only; produces a `jobs.Report` with one row per profile: `slug,spotify_id,status,verdict,reasons`), `refresh-metadata-preview` (dry-run, resource `database`), `refresh-metadata` (Params: `force` `true|false`; network; honours the TTL checkpoint). Actions that need Spotify return `Available() == false, "Spotify credentials are not configured"` without credentials.

- [ ] **Step 1: Tests** with fakes: `TestDiscoverPreviewDoesNotWrite` (fake store records zero writes), `TestDiscoverImportAppliesAndReportsCounts`, `TestAuditProfilesOfflineNeedsNoCredentials`, `TestAuditReportRowsSorted`, `TestRefreshUnavailableWithoutCredentials`, `TestRefreshForceRequiresConfirmation`.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): add discovery, audit and metadata refresh actions`.

### Task 8: Sync action with scope estimate, 429 partial and resume

**Files:**
- Create: `internal/application/admin/maintenance/actions_sync.go`, `actions_sync_test.go`

**Interfaces:**
- Consumes: `SyncRunner`, `artists`/`catalog` for scope, `Database.BackupTo`, `ResourceLock`.
- Produces actions `sync-dry-run` and `sync` with Params `scope` (`artist` | `selected` | `all`), `artists` (comma-separated slugs, for `selected`), `resume` (`true|false`). `Preview` computes: enabled verified artists in scope, estimated Spotify requests (artists × albums-page estimate documented as an estimate), the quota caveat text, and `Destructive: true` with `RequiresTyped: "sync <n> artists"` when `scope=all` or more than 5 artists. `selected` runs the existing single-artist `Options.ArtistSlug` sequentially per slug. On `*catalogsync.PartialSyncError` the job ends `StatusPartial`, `Resumable: true`, message `Spotify rate limit reached; retry after <n>s` and counts of completed/remaining artists. Before running: acquire `ResourceLock`, call `BackupTo`. Context cancel ends `StatusCancelled` after the current artist and keeps checkpoints.

- [ ] **Step 1: Tests**: `TestSyncAllRequiresTypedConfirmation`, `TestSyncSingleArtistNoTypedPhrase`, `TestSyncRateLimitEndsPartialResumable` (fake runner returns `PartialSyncError` with RetryAfter 90s), `TestSyncResumePassesResumeOption`, `TestSyncBacksUpDatabaseFirst` (backup call order recorded before runner), `TestSyncCancelledKeepsCheckpoints`, `TestSyncSelectedRejectsUnknownSlug`.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): add previewed sync action with resume support`.

### Task 9: Snapshot, verify, export and repository audit actions

**Files:**
- Create: `internal/application/admin/maintenance/actions_publishprep.go`, `actions_publishprep_test.go`
- Create: `internal/application/catalogaudit/audit.go`, `audit_test.go` (extract the snapshot-freshness and export-artifact checks from `cli.executeAudit`; **[my call]** the admin audit omits the git-diff and Hugo build checks, which stay CLI/CI-only)
- Modify: `internal/adapters/inbound/cli/cli.go` (`executeAudit` calls the extracted checks)

**Interfaces:**
- Produces: `catalogaudit.Run(ctx, catalogaudit.Options) (catalogaudit.Report, error)`; actions `db-verify`, `db-snapshot`, `export`, `audit` and a composite `refresh-export` that runs verify → snapshot → export → audit as steps. Snapshot and export write to a temp file/dir and rename; cancel between steps leaves previous artifacts intact.

- [ ] **Step 1: Tests**: `TestExportCancelledLeavesPreviousArtifacts` (fake writer, cancel mid-step, previous file bytes unchanged, no `.tmp` left), `TestRefreshExportStepsInOrder`, `TestVerifyFailureStopsComposite`, `TestAuditDetectsStaleSnapshot`; CLI audit tests must still pass.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS** (`go test ./internal/application/... ./internal/adapters/inbound/cli/`). **Step 5: Commit** `feat(admin): add snapshot, verify, export and audit actions`.

### Task 10: Reconciliation dry-run and apply actions

**Files:**
- Create: `internal/application/admin/maintenance/actions_reconcile.go`, `actions_reconcile_test.go`

**Interfaces:**
- Consumes: `artists.NewReconcileArtist` (`reconcile.go`, `reconcile_report.go`).
- Produces actions `reconcile-dry-run` (Params `artist` required slug) and `reconcile-apply` (same param). `reconcile-apply` previews the plan, uses the plan's `plan_checksum` as the preview `Fingerprint` and requires typed phrase `reconcile <slug>`; runs with `--backup-dir build/admin/backups` equivalent, so the existing `VACUUM INTO` backup + manifest happen. Report columns: `kind,artist,item,from,to`.

- [ ] **Step 1: Tests**: `TestReconcileDryRunWritesNothing`, `TestReconcileApplyRefusedIfPlanChanged` (checksum differs → `ErrStalePreview`), `TestReconcileApplyTakesBackupFirst`, `TestReconcileApplyRequiresTypedPhrase`.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): add reconciliation dry-run and apply actions`.

### Task 11: Freshness and published-data drift

**Files:**
- Create: `internal/application/admin/maintenance/freshness.go`, `freshness_test.go`

**Interfaces:**
- Produces: `type Freshness struct { SeedNeeded, SnapshotNeeded, ExportNeeded, RebuildNeeded bool; DiffersFromPublished bool; Reasons []string }`; `func (s *Service) Freshness(ctx) (Freshness, error)` using a `FreshnessSource` port (`CatalogDigest() string`, `SeededDigest() (string, bool)`, `SnapshotDigest()`, `ExportDigest()`, `PinnedContentSHA() (string, bool)`, `LocalContentSHA() (string, bool)`). Digests come from the existing manifest/`content_sha256` logic; adapter in `bootstrap`.

- [ ] **Step 1: Tests**: `TestYamlNewerThanSeedNeedsSeed`, `TestSnapshotOlderThanDBNeedsSnapshot`, `TestLocalHashDiffersFromPinnedMeansDiffersFromPublished`, `TestNoDatabaseReportsOfflineNotStale`.
- [ ] **Step 2: FAIL. Step 3: Implement + `bootstrap` adapter. Step 4: PASS. Step 5: Commit** `feat(admin): report data freshness and drift from published catalog`.

### Task 12: Manual release prepare/publish adapter

**Files:**
- Create: `internal/adapters/outbound/release/script.go`, `script_test.go`, `internal/application/admin/maintenance/actions_release.go`, `actions_release_test.go`
- Modify: `scripts/automation/catalog-release.py` only if it lacks a machine-readable preflight; add `preflight` subcommand printing JSON `{gh_authenticated, go_available, package_dir_clean, pinned_matches_local}` and no secrets.

**Interfaces:**
- Produces: `release.Script` implementing port `maintenance.Releaser{ Preflight(ctx) (Prereq, error); Prepare(ctx) (PackageInfo, error); Publish(ctx) (PublishInfo, error) }`. Command is built from constants only (`exec.CommandContext(ctx, "python3", scriptPath, sub, "--repo", repo)`); `repo` is validated against `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`; env is the minimum (`PATH`, `HOME`, `GH_TOKEN` passthrough only if already set). Output is parsed, never echoed. Actions `release-prepare` and `release-publish` (resource `release` + `database`; `release-publish` requires typed phrase `publish release` and a successful `release-prepare` in the same journal for the same local content hash).

- [ ] **Step 1: Tests** (fake `Releaser`; script adapter tested against a stub `python3` on a temp `PATH`): `TestPublishRequiresPriorPrepare`, `TestPublishNoChangesDoesNotCopyManifest`, `TestPreflightReportsMissingGhAuth`, `TestRepoArgValidated`, `TestScriptOutputNeverLeaksToken`, `TestNoAutomaticPublishAfterEdit` (running every other action never invokes `Releaser.Publish`).
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS** (also `python3 scripts/automation/test_catalog_release.py`). **Step 5: Commit** `feat(admin): add manual release prepare and publish actions`.

### Task 13: Operations decision log

**Files:**
- Create: `internal/adapters/outbound/filesystem/operationlog.go`, `operationlog_test.go`; `internal/application/admin/maintenance/oplog.go`, `oplog_test.go`

**Interfaces:**
- Produces: `type OperationEntry struct{ At time.Time; Action, JobID, Outcome, Summary string }`; port `OperationLog{ Append(ctx, OperationEntry) error; Recent(ctx, n int) ([]OperationEntry, error) }`; JSONL at `<catalog>.operations.jsonl`. `Service` appends one entry when a job is confirmed and one when it ends (hook on `jobs.Manager` via an `OnFinish` option added in Task 1's `NewManager` — extend it with `func(Job)` callback).

- [ ] **Step 1: Tests** `TestEntryAppendedOnConfirmAndFinish`, `TestRecentNewestFirst`, `TestEntrySummaryIsSanitised`. **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): keep a local operations log`.

### Task 14: Jobs pages and API

**Files:**
- Create: `internal/adapters/inbound/httpadmin/jobs.go`, `jobs_test.go`, `templates/jobs.gohtml`, `templates/job.gohtml`
- Modify: `internal/adapters/inbound/httpadmin/server.go` (register routes when `svc` implements new `Maintenance` interface, like `Editor`/`Resolver`), `static/admin.js` (poll `/api/v1/jobs/{id}` every 2 s while status is `queued|running|cancelling`), `static/admin.css`

**Interfaces:**
- Produces routes: `GET /jobs`, `GET /jobs/{id}`, `POST /jobs/{id}/cancel`, `POST /jobs/{id}/retry`, `GET /jobs/{id}/report.csv`, `GET /jobs/{id}/report.json`, `GET /api/v1/jobs`, `GET /api/v1/jobs/{id}`. Page includes `<meta http-equiv="refresh" content="3">` for active jobs when JS is absent. `Maintenance` interface in the adapter lists the service methods it needs (jobs list/get/cancel/retry, report render).

- [ ] **Step 1: Tests** (httptest with a fake `Maintenance`): `TestJobPageShowsStatusStepCounts`, `TestInterruptedJobShowsResumeButtonNotSuccess`, `TestCancelPostRequiresSameOrigin`, `TestReportDownloadDeterministicHeaders` (`Content-Disposition` filename has no path), `TestJobPageNeverShowsPathOrStack`, `TestJobsRoutesAbsentWithoutMaintenance`.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): add jobs pages and API`.

### Task 15: Operations dashboard with preview and confirmation

**Files:**
- Create: `internal/adapters/inbound/httpadmin/operations.go`, `operations_test.go`, `templates/operation_preview.gohtml`
- Modify: `templates/operations.gohtml` (replace static `make` list with action cards, freshness panel, last operations from the log, offline/credential banners)

**Interfaces:**
- Produces routes: `GET /operations`, `GET /operations/{action}` (parameter form), `POST /operations/{action}/preview`, `POST /operations/{action}/run` (fields: `token`, `typed`, params). `/api/v1/operations`, `/api/v1/freshness`. Run redirects `303` to `/jobs/{id}`; `409` page naming the holder job when `ErrConflict`; stale token shows "preview expired, review again". The generic "Refresh" never starts a full sync: the sync card links to the form only.

- [ ] **Step 1: Tests**: `TestRunWithoutPreviewTokenRefused`, `TestDoubleSubmitSecondGets409` (fake manager blocks), `TestSyncAllShowsScopeAndQuotaCaveatBeforeRun`, `TestUnavailableActionShowsReasonAndNoRunButton`, `TestDashboardOfflineBannerWithoutDB`, `TestOperationsPageDoesNotRenderSecretsOrPaths`.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): preview and confirm maintenance actions in the UI`.

### Task 16: Publish page and post-edit freshness banner

**Files:**
- Create: `internal/adapters/inbound/httpadmin/publish.go`, `publish_test.go`, `templates/publish.gohtml`
- Modify: `templates/layout.gohtml` and `editor.go` / `resolve.go` success flashes (append "Database/export need refresh" link when `Freshness` says so)

**Interfaces:**
- Produces: `GET /publish` shows the checklist (data fresh? differs from Pages? preflight prerequisites with pass/fail, last release manifest tag) and two separate forms: prepare and publish (typed phrase). Nothing on any edit/resolve page posts to `/publish`.

- [ ] **Step 1: Tests**: `TestPublishPageListsUnmetPrerequisites`, `TestPublishButtonDisabledWhenStale`, `TestSavingArtistNeverTriggersPublish`, `TestFlashShowsRefreshNeededAfterYamlEdit`.
- [ ] **Step 2: FAIL. Step 3: Implement. Step 4: PASS. Step 5: Commit** `feat(admin): add guided publish page and refresh-needed hints`.

### Task 17: Wiring, docs, end-to-end and CI

**Files:**
- Modify: `internal/bootstrap/admin.go` (build `Maintenance` service: job manager with journal at `build/admin/jobs.jsonl`, `Recover` on start, actions, release adapter, operation log), `internal/adapters/inbound/cli/admin.go`, `Makefile` (`admin-smoke` also hits `/operations` and `/jobs`), `docs/admin.md`, `docs/operations.md` (new "Admin maintenance workflows" and "Publication checklist" sections), `docs/security.md`
- Create: `internal/bootstrap/admin_maintenance_test.go` (E2E with fakes: preview → confirm → job → report download; 429 partial → restart → resume; reconcile dry-run → apply; offline start)

- [ ] **Step 1: E2E tests** `TestEndToEndSyncPartialThenResume`, `TestRestartMarksRunningJobInterrupted`, `TestOfflineStartNoCredentialsNoDatabase`, `TestStaticSiteBuildDoesNotReferenceAdmin`.
- [ ] **Step 2: FAIL. Step 3: Wire and document.** The doc must state: no deployment of admin to Pages, how to publish (checklist), backup locations, recovery from `interrupted`.
- [ ] **Step 4: Run** `make ci` → PASS; `make admin-smoke` → PASS.
- [ ] **Step 5: Commit** `feat(admin): wire maintenance workflows and document operations`; push branch `271-admin-add-safe-reviewable-sync-discovery-export-and-audit-workflows`; the PR description (written by hand) lists implementation notes, tests and the manual rollout procedure from `docs/operations.md`.
