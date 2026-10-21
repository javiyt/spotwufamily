# Local admin

A small web UI for browsing the editorial catalog and its Spotify links. It is an inbound adapter of the hexagonal application: it only calls use cases in `internal/application/admin` and holds no business rules. It browses **and edits** the catalog (artists, groups and their relationships) without hand-editing YAML, with validation, previews, conflict detection and atomic writes.

It is never part of the Hugo/Pages build: nothing in `site/` references it, and the static site stays read-only.

## Run

```bash
make admin                      # http://127.0.0.1:8787, CATALOG=data/artists.yaml DB=data/catalog.db
make admin ADMIN_PORT=9000
go run ./cmd/spotwufamily admin serve --host 127.0.0.1 --port 8787 --catalog data/artists.yaml --db data/catalog.db
```

The command prints a one-time-per-run launch URL, `http://127.0.0.1:8787/auth?token=...`. Open it: the token is exchanged for an `HttpOnly`, `SameSite=Strict` session cookie and the URL redirects to the dashboard. Stop with Ctrl+C (graceful shutdown, 5 s; running jobs are cancelled and waited for at most 5 s more, see *Maintenance state and restarts*).

Run it from the repository root: the maintenance workflows keep their state under `build/admin/` of the working directory, write exports to `site/…` there and run the release script from there (the same working-directory convention as every other CLI default). `--catalog` and `--db` keep their meaning; the snapshot is `catalog.snapshot.sql.gz` next to the database and the discovery ledger `wu-candidates.yaml` next to the catalog, which for the defaults are `data/catalog.snapshot.sql.gz` and `data/wu-candidates.yaml`. The release repository is `javiyt/spotwufamily` unless `SPOTWUFAMILY_RELEASE_REPO=owner/name` says otherwise; an invalid value, or `none`, registers no release action (the admin still starts).

`make admin-smoke` starts the server on a spare port (with `SPOTWUFAMILY_RELEASE_REPO=none`, so no page runs `gh` or the release script) and checks: non-loopback bind refused, unauthenticated request rejected, launch URL authenticates, foreign `Host` rejected, cross-origin `POST` rejected, `/operations`, `/jobs`, `/publish` and `/api/v1/jobs` served to the session, `POST /operations/sync/preview` without a session rejected and a cross-origin `POST /operations/freshness` rejected. It needs `go` and `curl`, no Node, no Spotify credentials and no `gh`.

## Pages and API

| Page | JSON | Content |
| --- | --- | --- |
| `/` | `/api/v1/status` | counts, verified/pending/unresolved IDs, groups/people, review queue, pending reconciliation, last sync, credential presence |
| `/artists` | `/api/v1/artists` | searchable, filterable, sortable, paginated artist list with catalog totals |
| `/artists/new`, `/artists/{slug}` | | create / edit an individual |
| `/groups` | `/api/v1/groups` | groups with verified/proposed member counts |
| `/groups/new`, `/groups/{slug}` | | create / edit a group and its members |
| `/review` | `/api/v1/review` | unverified Spotify profiles and proposed relationships |
| `/resolve`, `/resolve/{slug}` | `/api/v1/resolve`, `/api/v1/resolve/{slug}` | Spotify profile review queue and side-by-side workbench (see below) |
| `/operations`, `/operations/{action}` | `/api/v1/operations`, `/api/v1/freshness` | status, data freshness, maintenance actions with preview and confirmation, recent operations (see below) |
| `/jobs`, `/jobs/{id}` | `/api/v1/jobs`, `/api/v1/jobs/{id}` | background maintenance jobs: status, step, progress, counts, result and report downloads (see below) |
| `/publish` | | guided publication checklist: data readiness, release prerequisites, what will be published, prepare and publish steps (see below) |

List endpoints take `q`, `page` and `page_size` (default 25, max 100; out-of-range values are clamped). `/artists` and `/api/v1/artists` also take `category`, `enabled` (`yes`/`no`), `ids` (`verified`/`pending`/`unresolved`), `rel` (a relationship type, matches both endpoints), `archived=1`, `sort` (`name`, `slug`, `category`, `ids`, `order`) and `dir=desc`; unknown values are ignored. The API response adds `facets` with whole-catalog totals. Errors are always `{"error":{"code":"...","message":"..."}}` for `/api/*` and for clients sending `Accept: application/json`, and an HTML error page otherwise. Messages never include paths, stack traces or Go error text; details are logged server-side.

Definitions: *verified* = a linked Spotify ID has a verified profile review; *pending* = IDs linked but none verified; *unresolved* = no ID linked. A *group* is an artist tagged `affiliate_group` or the target of a non-rejected `member_of` relationship. Identical names and unverified memberships are never treated as identity evidence.

*Pending reconciliation* counts active album/track links classified `orphan` by the same rule as `artists reconcile` (read-only dry run).

## Editing

One backend record serves two pages: `/artists/{slug}` for individuals and `/groups/{slug}` for groups (a slug is redirected to the page that fits it). Spotify identity is **not** part of the form.

- **Editable fields**: display name, public name, slug, primary category, roles, aliases (order kept), enabled, editorial order, editorial notes and an HTTPS reference URL.
- **Spotify profiles** are shown in their own section as reviewed associations (status, reviewer, evidence). Posted Spotify IDs are ignored; linking, verifying or rejecting a profile goes through the profile-review workflow (`/review`, `make artists-review-interactive`).
- **Preview**: *Preview changes* runs the same use case as *Save* in dry-run mode and shows a field-level before/after diff. Nothing is written until *Save*.
- **Validation**: the whole catalog is validated before any write. A change is rejected when it *introduces* catalog issues (reported next to the offending field); unrelated legacy problems do not block an edit.
- **Concurrency**: every record has a revision (a hash of everything stored for it, including reviewed profiles) sent with the form (`revision`, also the `ETag` of the page). If the record changed since you opened it, in another tab, the CLI or by hand, the save is refused with `409` and the fields the other edit changed are listed; your submission is shown back so nothing is lost. Edits to different records never conflict, and a save always applies on top of the freshly loaded file, so unrelated changes made elsewhere are kept.
- **Slug changes** need an explicit confirmation that lists the relationships that will be rewritten. Run `artists reconcile` afterwards so synced Spotify rows follow the new slug.
- **Archive instead of delete**: archiving disables the entry and hides it from lists (`archived: true` in YAML, filter *Show archived*); relationships and synced albums/tracks are untouched, and restoring is one click. **Delete** removes only the catalog entry, is blocked while any relationship references it (the references are listed), and needs the slug typed to confirm. The admin never deletes database rows.
- **Relationships** are edited inline on both pages through the same use cases as `artists relationship ...`: a verified relationship needs a reviewer, a confidence and at least one *independent* source (credits and name matches never count), a membership target must be a group, and cycles are rejected. New edges default to *proposed*; periods use `YYYY[-MM[-DD]]` plus an optional era. Existing evidence is kept; each edit can add one more source.
- **Refresh hint**: after every successful save, archive, restore, delete, relationship change or Spotify profile decision the confirmation adds "Catalog changed — the database, snapshot and exports may need refreshing. Review in Operations." with a link to `/operations`. The hint is fixed text: nothing is computed, nothing is started, and nothing is ever published from an edit. The write also marks a cached freshness check as outdated, so the Operations and Publish panels say "Catalog changed since last check" and the next **Check data freshness** recomputes even inside its 60-second cache (`GET /api/v1/freshness` reports `"stale": true`).
- **Feedback**: successful saves show a toast built from the server's confirmation (JSON when JavaScript submits the form, a one-shot flash otherwise), failures show an alert summary linking to the fields, and the page disables saving and says so when the browser is offline. Without JavaScript every form still works. Keyboard: `/` focuses search, `Ctrl/⌘+Enter` saves, `Esc` closes a details panel or toast, then cancels.

### How writes reach the file

`Store.Update` (YAML adapter) takes an exclusive lock (`<catalog>.lock`, `flock`, so the CLI and several admin tabs are serialised), loads the current file, applies the change, writes a temp file in the same directory, `fsync`s it, copies the previous file to `<catalog>.bak` and renames the temp file over the catalog (then syncs the directory). Any failure before the rename leaves the catalog untouched and removes the temp file; the response is a generic error and the details are logged server-side. Output ordering is deterministic (struct field order, relationships sorted), and the leading header comments are re-emitted; other free-form comments are not preserved because the file is re-serialised from the domain model.

Recovery: `cp data/artists.yaml.bak data/artists.yaml` restores the last version before the previous save. The Release backups of the catalog (`make catalog-release-prepare`) are never touched.

## Spotify profile review

`/resolve` answers "which Spotify profile is this artist?" without the CLI. It is the same decision as `artists resolve --interactive --review-all`, built on the same code: candidates come from the evidence-aware searcher and are ranked with `catalog.RankCandidates`; each candidate gets the verdict of `catalog.EvaluateEligibility` (the evidence gate `artists resolve --apply` uses); evidence for both new and already attached IDs comes from `AuditProfiles.EvaluateProfile`, the engine behind `artists audit-profiles`; and the mapping rules live in `artists.ApplyMapping`, the one place that links, unlinks, reorders, verifies or rejects an ID. The page only renders.

**Queue** (`/resolve`, `/api/v1/resolve`): artists that are *unresolved*, *suspect* (cached releases contradict a linked profile), *needs_review*, *unverified* (linked but not verified, so sync ignores them), *previously rejected* or have *multiple IDs*. The badges carry whole-queue counts; `filter=` and `q=` narrow the list, and a search also finds unflagged artists so any artist can be reviewed. Order is editorial order, then slug, so it never reshuffles. Without a SQLite cache the queue says it cannot flag suspects instead of implying nothing is wrong.

**Workbench** (`/resolve/{slug}`): the editorial artist on the left; on the right one card per linked ID (primary first, so every secondary ID is reviewed on its own card), then search results, then rejected and quarantined profiles. A card shows the image (Spotify CDN only), name, exact `spotify:artist:` URI and link, genres, followers, albums, singles, representative tracks with their credits, the MusicBrainz comparison, evidence (positive, negative, missing), contradictions, score, confidence, the gate verdict and its reasons, and the stored reviewer/date/evidence. Track and release data comes from the SQLite cache first; the "assessment" is advice and never sets a status.

**Search** takes a name or alias, a `https://open.spotify.com/artist/…` link, a `spotify:artist:…` URI or a bare 22-character ID. Links are only *parsed* (`catalog.ParseSpotifyArtistRef`: https, host `open.spotify.com`, one `artist/<id>` path, optional `intl-xx/`); the server never fetches a user-supplied URL. A pasted ID is looked up by ID, never name-searched.

**Decisions** (each shows a confirmation page first: the catalog diff and the predicted downstream impact: linked IDs, what sync will ingest, artist enabled, pending reconciliation):

| Action | Effect |
| --- | --- |
| Accept as primary / Add as secondary | Links the ID as a **candidate** (sync ignores it). The former primary stays as a secondary. |
| Verify (or tick *Verify* on accept) | `verified` needs at least one **independent** evidence item you supply (MusicBrainz, Wikidata, Wikipedia, official site, label, press, social, Spotify profile). A name match, a credit or a group membership never counts. |
| Reject | Needs a note. Records `rejected` with reviewer and date and unlinks it. A rejection is lifted only with new independent evidence. |
| Mark inconclusive | `needs_review` plus your note; the link, if any, stays but is not synced. |
| Quarantine / Remove | Unlink; quarantine keeps the profile tracked as `needs_review`, remove forgets its non-rejected metadata. |
| Make primary | Reorders the linked IDs. |

Rules the workbench enforces: an ID linked to or tracked by another artist is never taken over (a shared name is not identity); nothing is auto-verified or auto-enabled (*Also enable the artist* works only for a verified profile); if the evidence audit finds a contradiction, verifying needs an **override reason** that is stored as evidence next to your name (it never replaces the independent evidence); a stale record (changed elsewhere since the page loaded) gives a `409` and nothing is written; a lost last ID disables the artist, as in the CLI.

**Provenance and audit**: reviewer and review time are stored per profile in the catalog (`spotify_profiles`), and every decision is also appended to `<catalog>.reviews.jsonl` (time, actor, action, before/after status, linked IDs before and after, note, override reason, evidence URLs). The file is append-only; if it cannot be written the decision is still saved and the page says so.

**Reconciliation**: a decision edits the YAML only. It never touches the database. A changed mapping appears in *Pending reconciliation* on the dashboard (the same dry-run rule as `artists reconcile`), and the confirmation page prints the exact `artists reconcile --artist <slug> --dry-run` command. Rejected, quarantined and unreviewed IDs stay out of sync through the existing `ProfileSyncPolicy` gate.

**When a source fails** the page still renders and says what is missing. Spotify `429` shows the `Retry-After` it sent (the admin client gives up after 5 s instead of blocking the request, so reload later); MusicBrainz `503` or offline, a profile with no catalog albums and cache-only mode are all reported as *missing evidence*, never as a contradiction and never as support. Nothing is ever concluded from the absence of contradicting evidence.

**Credentials** (`SPOTIFY_CLIENT_ID`/`SPOTIFY_CLIENT_SECRET`) are read once by the server process to build the Spotify client and are never placed in a template, JSON response, log line or error. Without them the workbench runs cache-only and disables name search. Tests use fake Spotify and MusicBrainz sources and need no network.

## Jobs

Maintenance operations run as background jobs. `/jobs` lists them newest first; `/jobs/{id}` shows the status, current step, progress (`done` of `total`), counts, resources, timestamps and the result summary. While a job is queued, running or cancelling the page refreshes itself every 3 seconds without JavaScript (`<noscript>` meta refresh); with JavaScript it polls `/api/v1/jobs/{id}` every 2 seconds instead, updates in place and reloads once when the job finishes. If polling fails the offline banner is shown until it succeeds again.

- **Cancel** (`POST /jobs/{id}/cancel`, queued or running jobs) asks the job to stop at its next safe point. Cancelling a finished job changes nothing and says so.
- **Resume/retry…** (`POST /jobs/{id}/retry`) is offered for failed jobs and for partial, interrupted or cancelled jobs that are resumable; the maintenance service decides (`ResumeEligible`), not the page. It starts nothing: it redirects to the operation's preview, `/operations/{action}?resume_of={id}`, where the run must be confirmed.
- A refused cancel or retry (finished job, not resumable) answers `409` with the JSON error to API and in-page (fetch) clients, so it never shows as a success; plain forms get a redirect and a message.
- An **interrupted** job is one the admin was running when it restarted. It never counts as a success; the page says whether it can be resumed. A **partial** job shows what completed, what is left and any retry-after hint.
- Reports download from `/jobs/{id}/report.csv` and `/jobs/{id}/report.json` as `<action>-<first 8 characters of the id>.<ext>` attachments, byte-identical to the deterministic CSV/JSON report writers. The JSON API never inlines report rows; it reports `report_available` and links. Times are RFC 3339 UTC.

Job IDs in paths must be hexadecimal with dashes; anything else is a 404 before any lookup.

## Operations

`/operations` shows the database, credential and last-sync status (with banners when there is no database or no Spotify credentials), a data freshness panel, the maintenance actions and the last 10 entries of the operations log, newest first. When the service has no maintenance actions (`Operator` not implemented) the page keeps the status block and lists the CLI commands instead, and every route below is absent (404).

- **Actions** are grouped as Discover, Audit, Sync, Refresh/Export, Reconcile and Release. Each card shows the title, a one-line description, `NETWORK`/`DESTRUCTIVE` badges and either *Available* with a **Review…** link to `/operations/{action}` or the action's own unavailability reason. Release cards link to `/publish`. Nothing runs from the dashboard; in particular a sync is only ever started from its form and preview.
- **`GET /operations/{action}`** is the parameter form: sync and sync-dry-run take `scope` (artist/selected/all), `artists` (comma-separated slugs) and `resume`; audit-profiles takes `artist` and `offline`; refresh-metadata(-preview) takes `force`; reconcile-* take `artist`; the rest take none. Unknown actions are 404; an unavailable action shows its reason and no form. `?resume_of={jobID}` shows "Resuming job …" (and ticks `resume` for sync) only when the service confirms that job exists, ran this action and is resumable or failed; otherwise it says so and is ignored.
- **`POST /operations/{action}/preview`** shows the estimated impact (scope lines), warnings, badges and, for network or destructive actions, a quota/impact caveat. Form fields are passed to the service untouched and validated there; a validation error re-renders the form with the message. The confirmation form carries an opaque preview token and the validated parameters as hidden fields, and a typed-confirmation field only when the action requires one (e.g. `sync 42 artists` for a large sync).
- **`POST /operations/{action}/run`** (`token`, `typed`, the parameters) re-checks the preview and redirects `303` to `/jobs/{id}`. A missing, expired or mismatched token shows "The preview is out of date; review it again" with a button that re-previews the same parameters; a wrong phrase re-renders the preview with a message; a resource conflict answers `409` naming and linking the job that holds it. The confirm button disables itself on submit (JavaScript); the server's conflict check is what prevents double runs.
- **Data freshness** is never computed on page load or by `GET /api/v1/freshness` (which returns the last result or `{"checked":false}`). **Check data freshness** (`POST /operations/freshness`) runs the comparison once — it reads the whole database and can take seconds — and caches the result for 60 seconds; concurrent clicks share one computation. The page shows the result with its age.
- **Availability never blocks a page.** The dashboard and `GET /api/v1/operations` list actions through `Service.Actions()`, which never runs a costly check on the request: the release actions report their last preflight result (even if older than 30 s) and refresh it in one background check (bounded at 2 minutes), showing "Checking release prerequisites…" until the first check ends. The per-action pages (form, preview, run) look up only their own action (`Service.Action(name)`), so a slow `gh` or release script never affects other actions. The release actions' preview, job spec and in-lock re-check still run a fresh synchronous preflight.
- `GET /api/v1/operations` lists every action with `name`, `title`, `group`, `description`, `available`, `unavailable_reason`, `uses_network`, `destructive` and a `page` link.

All operation POSTs need the session, the CSRF token and a same-origin request. Preview tokens are never logged, and pages never show paths or Go error text.

## Publish

`/publish` is the guided publication checklist and the **only** page in the admin from which a release can be prepared or published. It works without JavaScript and never blocks: it never computes data freshness on load and never runs the release preflight on the request.

- **Data readiness** shows the cached freshness check (same 60-second cache and **Check data freshness** button as Operations, which returns here): seed, snapshot or export needed, unreadable database, offline, differs from the published catalog, and the fixed reasons. If anything needs refreshing, refresh first and prepare again. Next to step 2 the page warns (without blocking) when freshness is unchecked, outdated by a catalog write, or says the database, snapshot or export needs work.
- **Prerequisites** shows pass/fail rows for python3 and the release script, Go, the GitHub CLI login and an empty package directory (plus the local database) with the fix for each failure, e.g. "Run `gh auth login` in a terminal" or "Empty or remove `build/catalog-release`". The rows come from the release actions' cached preflight (`Service.ReleaseStatus()`); while no check has finished the page says "Checking…", lists that as the reason for both steps, and reloads itself every 3 seconds. **Re-check** (`POST /publish/recheck`) marks every release action's cached result as outdated and starts one background check for the publish action only (the prepare action re-checks on its next listing); it does not wait. Until that check ends the page keeps showing the previous result, marked "Re-checking…", and reloads itself. A release job that finishes also marks the cached result outdated, so the page picks up a new package without waiting for the 30-second cache; if step 2 does not appear right after preparing, press **Re-check**. A check that fails or panics is recorded (and a panic logged) as "python3 or the release script is unavailable" until the next check.
- **What will be published** shows the local and pinned content hashes (first 12 characters), the last release tag, whether the local content differs from the pinned release Pages serves, and the prepared package (tag, content hash, and whether it matches the local content).
- **Step 1 – Prepare package** and **Step 2 – Publish release** are plain forms leading to the usual preview and confirmation pages for `release-prepare` and `release-publish` (the latter needs the typed phrase `publish release`). A step is shown as a form only when the cached check says it can run; otherwise the page lists why. Step 2 needs a prepared package that matches the local content, an authenticated GitHub CLI and a working release script. The preview, the run and the job re-check everything with a fresh preflight regardless of what the page showed.
- The generic routes of the release actions are reserved for this page: `GET /operations/release-prepare|release-publish` redirects (`303`) to `/publish`, and their `POST …/preview` and `POST …/run` redirect there too unless the form carries the hidden field `from=publish` that only this page's forms (and the preview pages they lead to) emit. That field is not a security boundary (anyone can type it); session, CSRF, same-origin, the preview token, the typed phrase and the service's re-checks are the real gates. It keeps the checklist the one way into publishing.
- The page lists the last release entries of the operations log with links to their jobs, and the manual rollout steps below.

**Manual rollout procedure**

1. In Operations, run `refresh-export` (verify, snapshot, export, audit) and check data freshness until it reports nothing to refresh other than "Local data differs from the published catalog" (expected: that is what you are about to publish).
2. On `/publish`, **Prepare package…** → review → confirm. The job writes the package to `build/catalog-release`.
3. On `/publish`, **Review publish…** → type `publish release` → confirm. The job uploads the GitHub release and updates `data/catalog.release.json` locally.
4. Commit the updated `data/catalog.release.json` pin **together with the catalog changes it was built from** (`data/artists.yaml` and, if changed, `data/artists.yaml.reviews.jsonl`) on one branch and open one pull request; merge it once CI passes. Pages builds from `data/artists.yaml`, so committing the pin alone would ship the new release without the edits.
5. Wait for CI and the GitHub Pages deploy, then verify the site (artist count, a recently changed artist, the new release loaded).

**Prerequisites**: `python3` (runs `scripts/automation/catalog-release.py`), `go` on `PATH` (prepare), and the GitHub CLI logged in with `gh auth login` (publish; an already exported `GH_TOKEN`/`GITHUB_TOKEN` also works). The repository is the one the admin was configured with.

**Where things live**: the package (complete database backup, snapshot and manifest) is in `build/catalog-release` until published or removed; jobs and their results are in the admin's job journal (`/jobs`), and every confirmed and finished operation is in the operations log (`/operations#recent`, `/publish`). The YAML editor keeps `<catalog>.bak`, and every sync takes its own database backup first.

**Recovery from `interrupted`**: a publish job that was running when the admin stopped, or that hit its timeout, never counts as a success. Check the repository's GitHub Releases page before retrying, as the job message says: if a partial release with the prepared tag exists, remove it there by hand, then return to `/publish`, **Re-check**, and publish again (the package is still in `build/catalog-release`). Do not edit the pin by hand. A prepare that was interrupted can simply be repeated after emptying `build/catalog-release`.

The admin itself is never deployed to Pages: nothing in `site/` references it, only the static site and the pinned release reach Pages.

## Maintenance state and restarts

| What | Where |
| --- | --- |
| Job journal (every job's status, counts and result, append-only JSONL, `0600` in a `0700` directory) | `build/admin/jobs.jsonl` |
| Database backups taken before a sync, metadata refresh, discovery import or reconcile apply (`VACUUM INTO`, never overwritten; same-second backups get a `-2`, `-3`… suffix) | `build/admin/backups/` |
| Operations log (confirmed and finished entries: time, action, job, outcome, sanitized summary; no params or paths) | `<catalog>.operations.jsonl`, e.g. `data/artists.yaml.operations.jsonl` |
| YAML backup of the previous catalog on every write | `<catalog>.bak` |
| Release package | `build/catalog-release` |

`build/` is git-ignored. Delete old backups by hand when they are no longer needed; the journal and the operations log are never pruned.

- **Preview tokens** are HMAC-signed with a 32-byte secret drawn from `crypto/rand` at every start (the admin refuses to start if it cannot get one). A restart therefore invalidates every open preview: confirming one answers "The preview is out of date"; review it again.
- **Restart semantics**: on start the admin reloads the journal. A job that was queued, running or cancelling when the previous process stopped is recorded as **interrupted** ("interrupted by server restart") and never as succeeded. Its resources (`catalog-yaml`, `database`, `release`) are free again, so new jobs can start; the database file lock held by the old process died with it.
- **Recovering from `interrupted`**: open the job. If it says it can be resumed (a sync that had completed artists), **Resume/retry…** leads to the sync preview with `resume` ticked; the resumed run skips the artists the interrupted run completed (their checkpoints were committed per artist). Other interrupted jobs are simply previewed and run again: every action re-reads the current state and backs the database up again first. An interrupted `release-publish` is the exception: check the GitHub Releases page first (see *Publish*).
- **Shutdown**: Ctrl+C cancels every running job and waits at most 5 seconds for them to stop at a safe point (a cancelled sync keeps its completed artists and is resumable). A started release publish is not cancellable; shutdown stops waiting after the bound instead of hanging, and the next start marks it interrupted.
- **Job IDs** are random UUIDs.

## Offline mode

The YAML catalog is read on every request, so edits show up on reload. If the database file is missing or unreadable the UI says so and keeps serving the catalog; the file is never created.

The admin starts and serves every page with no database, no Spotify credentials, no `python3`, no `go` and no `gh`. Nothing needs them at start: credentials are read when an action runs, and the release prerequisites are checked in the background. Each action that needs something missing is listed as unavailable with its reason (e.g. "No database found; run the init workflow first", missing Spotify credentials, or "python3 or the release script is unavailable"); offline actions such as `sync-dry-run` and `audit-profiles` with `offline` stay available (discovery needs no credentials but reaches Wikipedia when it runs). The profile review workbench is the only part that can call Spotify or MusicBrainz, and only to answer a search or fill a card the cache cannot.

## Catalog releases

Publishing a catalog release is always manual and takes two maintenance actions; no other action, edit or save ever publishes.

- `release-prepare` runs `catalog-release.py prepare` under the database lock and writes the package (backup, snapshot, manifest) to `build/catalog-release`. It uploads nothing and needs Go, a local database and an empty package directory; it does not need the GitHub CLI. Remove or publish an earlier package before preparing another.
- `release-publish` runs `catalog-release.py publish`. It is listed only when `gh auth status` succeeds, needs the typed phrase `publish release`, and refuses unless the latest successful `release-prepare` in the admin job journal, and the verified package on disk, hold the current local content hash. It re-checks everything under the database lock. If the pinned `data/catalog.release.json` already has that content, nothing is uploaded and the manifest is not touched. Once the upload has started it cannot be cancelled, because stopping `gh` mid-upload could leave a partial release. A cancel request is noted in the result and the job reports what actually happened. If the upload hits its 30-minute timeout, the job fails with "Publishing was interrupted; a partial release <tag> may exist on GitHub — check the Releases page before retrying."

Both run the script as `python3 scripts/automation/catalog-release.py <preflight|prepare|publish> --repo <owner/name>`, from the repository root, with only `PATH`, `HOME`, `LANG` and an already set `GH_TOKEN`/`GITHUB_TOKEN` in the environment. Script output is parsed, never shown; failures show a fixed message and log at most a short, redacted excerpt of stderr. The freshness view reads the pinned and local content hashes from the script's read-only `preflight`.

## Security model

- Binds `127.0.0.1` by default. A non-loopback `--host` (including `0.0.0.0` or an empty host) is refused unless `--allow-remote` is passed **and** `SPOTWUFAMILY_ADMIN_TOKEN` holds a token of at least 32 characters. There is no TLS: put remote access behind an SSH tunnel or a TLS proxy.
- `Host` must match the listen address (loopback aliases `localhost`/`127.0.0.1`/`[::1]` on the same port), which defeats DNS rebinding.
- Every request except static assets needs a session. Sessions live in memory only, expire after 8 h and are lost on restart; nothing is written to the repo. Token comparison is constant-time and failed attempts are rate limited (10 per minute, then 429).
- Every state-changing request needs a same-origin `Origin` (or no cross-site `Sec-Fetch-Site`) **and** a session-bound HMAC CSRF token (`csrf_token` form field). Mutations are `POST /logout` and the editor's `POST` routes; all go through the same checks.
- Headers on every response: strict CSP (no inline script/style, `frame-ancestors 'none'`), `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy: no-referrer`, COOP/CORP, `Cache-Control: no-store`.
- Request bodies are capped at 64 KiB, headers at 16 KiB, with read/write/idle timeouts.
- Templates are `html/template` with contextual escaping, so artist names and notes cannot inject script.
- Maintenance actions run only through preview → confirm: the confirm form carries an HMAC preview token bound to the action, its parameters, a fingerprint of the state it previewed (catalog, database, plan checksum) and a 10-minute expiry, signed with the per-process secret. Destructive or large actions also need a typed phrase (`sync 42 artists`, `reconcile <slug>`, `publish release`). A changed state makes the token stale; a held resource answers `409` naming the holder.
- The only subprocess is the release script, run with a fixed executable, script path and subcommand, a validated `--repo`, and a minimal environment. No action, edit or save ever publishes; only the `release-publish` step of `/publish` does, after its typed phrase.
- Job messages, reports, summaries and API errors never contain paths, SQL, stack traces or secrets: unknown errors become "operation failed" / "An internal error occurred." and are logged server-side only.
- `SPOTIFY_CLIENT_ID`/`SPOTIFY_CLIENT_SECRET` are used only inside the server process to talk to Spotify; values are not read into any response, template or log. The launch token is not logged (only request paths, never queries). The server proxies no URLs. The CSP allows images only from `'self'`, `data:` and the Spotify image CDNs (`*.scdn.co`, `*.spotifycdn.com`); a profile image from any other host is dropped before rendering.

## Layout

- `internal/application/admin`: queries, the editing use cases (`ApplyChange`, `ApplyRelationship`), the profile review use cases (`ResolveQueue`, `ResolveWorkbench`, `PreviewResolution`, `ApplyResolution`), ports (`OperationsReader`, `CatalogUpdater`) and pagination.
- `internal/adapters/inbound/httpadmin`: server, security middleware, handlers, embedded templates and CSS/JS (no Node build).
- `internal/application/jobs`: background job manager (resource locks, cancellation, restart recovery).
- `internal/application/admin/maintenance`: maintenance actions, preview tokens, freshness, operations log.
- `internal/bootstrap`: shared composition root. `NewAdmin` (`admin_maintenance.go`) wires YAML + SQLite into the admin service and adds the job manager, every maintenance action, the release adapter, freshness and the operations log.
- `internal/adapters/inbound/cli/admin.go`: the `admin serve` command.
