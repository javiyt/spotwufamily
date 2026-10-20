# Catalog Policy

`data/artists.yaml` is the editorial source of truth for Wu-Tang Clan, Wu Family, producers and collaborators included in the project.

Rules:

- Do not use artist name as internal identity once a Spotify ID is known.
- Use `spotify_id` as the primary Spotify profile and `spotify_ids` for additional Spotify profiles that represent the same editorial artist or group.
- Keep artists disabled until their Spotify ID has been reviewed. `enabled` only says the editorial record is active; it never makes an unreviewed profile eligible for sync (see below).
- Use categories: `core`, `affiliate_group`, `affiliate_artist`, `producer`, `collaborator`.
- Keep the primary `category` present in `roles` whenever roles are listed.
- Use `public_name` only when the display name should intentionally differ from `name`.
- Use `external_url` only for HTTPS editorial references.
- Use `added_at` in `YYYY-MM-DD` format.
- Preserve aliases and notes for editorial context.

Validation detects duplicate slugs, names, Spotify IDs, aliases, roles and editorial order values, plus unknown categories and malformed Spotify IDs.

## Spotify profile identity and review status

Three identities are kept apart:

- **Editorial artist**: the stable `slug`.
- **Spotify profile**: a 22-character ID. Many profiles can belong to one artist; one profile belongs to exactly one artist, globally.
- **Release or track credit**: an artist credited on a release is *not* proof of group membership, and an identical name is *not* proof of identity. Neither counts as independent evidence.

### Canonical data

`spotify_id` / `spotify_ids` stay the canonical list of **linked** profiles, with unchanged meaning. The additive `spotify_profiles` array is the canonical **review metadata** for those IDs (and for rejected/candidate IDs that are tracked but not linked). A linked ID without an entry is implicitly `unreviewed`. There is no second list of IDs: SQLite and the exports still project the linked IDs only.

Before (legacy, still valid and loaded unchanged):

```yaml
version: 1
artists:
  - slug: rza
    name: RZA
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    enabled: true
```

After a review:

```yaml
version: 1
profile_sync_policy: verified_only   # optional; omitted means verified_only
artists:
  - slug: rza
    name: RZA
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    spotify_profiles:
      - id: 34EP7KEpOjXcM2TCat1ISk
        status: verified
        reviewed_at: 2026-01-02T15:04:05Z
        reviewer: javi
        last_checked_at: 2026-01-02T15:04:05Z
        evidence:
          - type: musicbrainz
            url: https://musicbrainz.org/artist/00000000-0000-0000-0000-000000000000
            rationale: MusicBrainz lists this Spotify URL for RZA.
      - id: 0H8YCcvC3MPLKnbDRasGiG   # tracked, not linked
        status: rejected
        reviewed_at: 2026-01-02T15:10:00Z
        reviewer: javi
        evidence:
          - type: name_match
            rationale: Different artist that happens to share the name.
    enabled: true
```

### Fields

| Field | Rules |
| --- | --- |
| `id` | 22 alphanumeric characters; unique across the whole catalog (linked or tracked) |
| `status` | `unreviewed`, `candidate`, `verified`, `rejected`, `needs_review` |
| `evidence[].type` | `name_match`, `credit` (informational), `spotify_profile`, `musicbrainz`, `wikidata`, `wikipedia`, `official_site`, `label`, `press`, `social` |
| `evidence[].url` | `https` only, public host name, no credentials, IPs or custom ports; `spotify_profile`/`credit` must be on `open.spotify.com`, `musicbrainz` on `musicbrainz.org`, `wikidata` on `wikidata.org`, `wikipedia` on `*.wikipedia.org` |
| `evidence[].release_id`, `track_id` | optional Spotify IDs |
| `evidence[].rationale` | required human explanation, up to 1000 characters |
| `reviewed_at`, `last_checked_at` | RFC 3339 UTC (`...Z`); left empty when unknown, never invented |
| `reviewer` | required for `verified` and `rejected`; forbidden on `unreviewed` |

Invariants enforced by validation: `verified` needs at least one independent (not `name_match`/`credit`) evidence item, a reviewer and `reviewed_at`, and must be linked; `rejected` needs a reviewer, `reviewed_at` and an explanation, and must **not** be linked.

### Transitions

```text
              +--------------+
  legacy ---> |  unreviewed  |
              +------+-------+
                     | (automation or manual)
                     v
 +-----------+   +-----------+   manual + independent evidence   +----------+
 | candidate |<->|needs_review|---------------------------------> | verified |
 +-----+-----+   +-----+-----+ <---------------------------------+----+-----+
       |               |            (re-check)                         |
       | manual        | manual                                        | manual
       v               v                                               v
                  +----------+  <--------------------------------------+
                  | rejected |
                  +----+-----+
                       | manual + NEW independent evidence only
                       v
              candidate / needs_review / verified
```

- `verified` and `rejected` can only be set by a human (`Manual`), with reviewer and review time. Resolve, discovery and name matching can only create `candidate`.
- `verified` cannot return directly to `candidate` or `unreviewed`; use `needs_review` or `rejected`.
- Leaving `rejected` requires a manual review and at least one **new** independent evidence item (evidence already attached to the rejection does not count). The rejection evidence stays in the history.
- `resolve --apply` and the interactive resolver skip any ID that is rejected anywhere in the catalog.

### Sync eligibility

`enabled: true` does not ingest anything by itself. A linked profile is synced only when:

| Status | `verified_only` (default) | `allow_unreviewed` |
| --- | --- | --- |
| `verified` | yes | yes |
| `unreviewed` (legacy) | no | yes |
| `candidate`, `needs_review`, `rejected` | no | no |

`profile_sync_policy: allow_unreviewed` is the deliberate, catalog-wide migration switch. It is set in `data/artists.yaml` so existing mappings keep syncing without fabricating provenance; remove it once the profiles are reviewed. Catalogs without the key (for example an older release backup restored as-is) sync nothing until verified or migrated.

### CLI

```sh
spotwufamily artists review-profile \
  --slug rza --spotify-id 34EP7KEpOjXcM2TCat1ISk --status verified --reviewer javi \
  --evidence-type musicbrainz --evidence-url https://musicbrainz.org/artist/<mbid> \
  --rationale "MusicBrainz lists this Spotify URL" [--link] [--dry-run] [--catalog data/artists.yaml]
```

- Always a manual decision; the review time is "now" in UTC.
- `--link` adds a not-yet-linked ID to the artist (primary if none, otherwise `spotify_ids`).
- Rejecting a linked ID unlinks it (promoting the next extra ID to primary) and disables the artist if no ID remains.
- The whole catalog is validated before saving; invalid transitions, duplicate IDs and unsafe URLs fail with exit code 1 (bad flags: 2).
- Saves are atomic (temp file + rename) and the previous file is kept as `artists.yaml.bak` (git-ignored). GitHub Release backups are never touched.

### Rollout

1. Merge; `data/artists.yaml` gains `profile_sync_policy: allow_unreviewed` and nothing else, so sync behaviour is unchanged.
2. Review profiles with `artists review-profile`, starting with groups and shared names.
3. When every linked profile is reviewed, drop `profile_sync_policy` (or set `verified_only`).
4. Rollback: the new keys are optional; deleting them returns to the legacy file shape.

## Homonym reviews

Identical or similar artist names, genres and group membership are never identity evidence. A profile is accepted only with independent evidence (for example a MusicBrainz artist credited on a release that the Spotify profile also lists), and a rejected ID stays in `spotify_profiles` with its evidence so the history is visible in the YAML, `artists audit-profiles` and the future admin. Reconcile already-synced rows with `artists reconcile` (see `docs/operations.md`).

Decisions recorded on 2026-10-20 (issue #263):

| Entry | Previous ID | Disposition | Evidence |
| --- | --- | --- | --- |
| `streetlife` | `0FCYBhXjpfQIQWNKiVEDg5` | **Rejected**, replaced by `7CrVv4zLga71gdlMOMfjXL` (verified) | The rejected profile is named "Streetlife Originals" (nu jazz, 1.2K followers): Sidewalk Stories (Paper Recordings, 1999) and compilation appearances, no Wu credits. The replacement lists "Street Education (Method Man Presents Street Life)" (2006), Wu-Tang Forever, Tical 2000, Iron Flag and The W; MusicBrainz `e70e310e-8e92-4da7-9aba-071bc6032a34` is credited with Method Man on release `ff16ca9f-137a-4638-9120-fdc1ac160ee1` "Street Education". |
| `k-g-b` | `2gKwx0Lm0JoOrN2mY9Z0PG` | **Quarantined** (rejected, no replacement, entry disabled) | The profile mixes French releases (Retour d'exil, Seule la lutte paye), Neat Face/Trax Records releases and compilations. MusicBrainz K.G.B. `2d9e108a-8f89-4ac3-8e6c-98f0ab323a62` (US, alias Klik Ga Bow) is not shown to own it. Unverified candidate `7Jab8mHFYj7jSkFwKtPpxB` (KGB: Freek Tha Flo 1994, Bless Ya Life) matches MusicBrainz recording titles but has no Spotify URL relation; it is noted, not adopted. |

To lift the K.G.B. quarantine, confirm a candidate against a primary Wu discography source, then `review-profile --status verified --link` it and enable the entry.

## Core Wu-Tang Clan solo records

Every core member has exactly one editorial record: `rza`, `gza`, `method-man`, `ghostface-killah`, `raekwon`, `inspectah-deck`, `u-god`, `masta-killa`, `cappadonna` and `ol-dirty-bastard`. Their primary Spotify profile is `verified` through the MusicBrainz URL relation of the matching artist, never through the name or through Wu-Tang Clan credits, and no member reuses a group profile.

- Other Spotify profiles with the same name that exist in the local cache (several members have a second, same-named profile) are **not** linked: nothing independent ties them to the artist. Link them with `artists review-profile` only after evidence is found.
- `category: core` is an editorial label. Group membership comes only from `relationships` records (see Artist relationships below).
- Cappadonna began as an affiliate and is credited as a full member on later group albums. He is kept as `core`, with the nuance recorded in `notes`; the membership period lives in his `member_of` relationship (`start: 2007`, medium confidence, with commentary).
- New records get appended `editorial_order` values (196-204). Existing orders are never shifted: `seed-db` upserts row by row against a unique `editorial_order` index, so renumbering breaks seeding of existing databases.

## Candidate inventory and curation

External inventories (including AI-generated or fan-directory references) are **candidates**, never identities. `data/wu-candidates.yaml` is the reviewable ledger of every candidate, its provenance and the human decision about it. The generated summary and per-candidate evidence links live in [`docs/candidate-curation.md`](candidate-curation.md).

### Statuses

| Status | Meaning | Reaches `artists.yaml`? |
| --- | --- | --- |
| `pending` | Imported, not reviewed. The only status imports can create (besides the automatic duplicate below). | No |
| `accepted` | Wu relation corroborated by at least two independent evidence types with permanent URLs. | Yes, staged disabled with no Spotify ID |
| `needs_more_evidence` | Plausible but not corroborated; `reason` says what is missing. | No |
| `rejected` | Dismissed with a reason. Re-imports and `discover-wu` never propose it again; leaving this status needs at least one new independent evidence item. | No |
| `alias_of_existing` | A variant of an entry already in the catalog (`alias_of` slug). Applied as an alias, never as a new group. | Alias only |

An imported name that matches a catalog entry by slug, name, public name, alias or a known external identifier is recorded as `alias_of_existing` automatically ("mechanical duplicate"). A name that only matches after ignoring punctuation (`N-Tyce` vs `NTyce`) is kept `pending` with `possible_duplicate_of`: never merged on a guess. Matching ignores case, accents and apostrophes.

Accepted entries record a classification: `relation` (`creator`, `member`, `producer`, `collaborator`, `association`), `relation_groups` (catalog slugs) and `category`/`roles`. Group membership of a Wu affiliate group is not Wu-Tang Clan membership: Prince Paul is a `collaborator` (and producer) whose only Wu relation is Gravediggaz.

Evidence reuses the profile evidence types. `name_match` and `credit` never count; two items of one type are one source. Same-name MusicBrainz records can be different people (`N-Tyce` is also a UK girl group; MusicBrainz "Too Poetic" is a different act), so record the exact MusicBrainz ID in `external_ids`.

### Commands

```sh
# Dry run: shows counts and the plan, writes nothing. JSON or CSV (BOMs and the Spanish reference headers are accepted).
spotwufamily artists import-candidates --input inventory.json --report candidates-report.md
# Stage pending candidates and provenance in the ledger.
spotwufamily artists import-candidates --input inventory.json --apply
# Record one manual decision (--kind only when the name is not in the ledger yet).
spotwufamily artists review-candidate --name "Frukwan" --status accepted --reviewer javi \
  --category affiliate_artist --relation member --group gravediggaz --relation-claim "Member of Gravediggaz" \
  --evidence "musicbrainz|https://musicbrainz.org/artist/<mbid>|why" \
  --evidence "wikipedia|https://en.wikipedia.org/wiki/Gravediggaz|why" --external-id musicbrainz=<mbid>
# Write accepted candidates and alias decisions into data/artists.yaml (disabled, no Spotify ID).
spotwufamily artists import-candidates --apply
# Undo staging (entries that were enabled or got a Spotify profile since are kept).
spotwufamily artists import-candidates --revert --apply
```

`make artists-import-candidates`, `artists-import-candidates-apply`, `artists-stage-candidates` and `artists-revert-candidates` wrap these.

Guarantees: an import never changes the status, classification or evidence of a known candidate (it only adds provenance and aliases); output is sorted and byte-stable, so repeated runs produce no diff; `artists.yaml.bak` and `wu-candidates.yaml.bak` are kept (git-ignored) and GitHub Release backups are never touched. `discover-wu --ledger` only **reads** the ledger: ledger names are reported as "Suppressed By Candidate Ledger" and never added again. Mapping a Spotify profile for a staged entry is a separate step with `review-profile`; nothing here sets a Spotify ID or enables an entry.

### Decisions recorded on 2026-10-20 (issue #265)

Source: the 2026-10-19 comparative inventory (266 artists and 4 groups absent by name, plus 8 variant claims). 278 rows imported: 11 accepted, 13 `alias_of_existing`, 6 `needs_more_evidence`, 0 rejected, 248 left `pending`. Corroboration used MusicBrainz group relations plus the matching Wikipedia article; both are community-edited, so they are independent databases, not authorities. A maintainer should still sign off the `reviewer: claude-code` decisions.

| Candidate | Decision | Classification and note |
| --- | --- | --- |
| Frukwan | accepted | Gravediggaz member, `affiliate_artist`; MusicBrainz also lists Wu-Tang Killa Bees |
| Too Poetic | accepted | Gravediggaz member (d. 2001); the MusicBrainz record is "Poetic", the record named "Too Poetic" is a different act |
| Prince Paul | accepted | `collaborator` + `producer`; Gravediggaz member, no Wu-Tang Clan membership |
| 60 Second Assassin | accepted | Sunz of Man member |
| Joe Mafia, Myalansky | accepted | Wu-Syndicate members |
| N-Tyce, Champ MC | accepted | Deadly Venoms members; N-Tyce's UK girl-group homonym excluded |
| Trife Diesel | accepted | Theodore Unit / T.M.F. member; Wikipedia article lacks citations, MusicBrainz relation is the anchor |
| Kevlaar 7 | accepted | Wisemen co-founder, `affiliate_artist` + `producer` (d. 2014) |
| Illah Dayz | accepted | Wisemen member |
| Lord Jessiah | needs_more_evidence | Only in a Wikipedia infobox; no MusicBrainz group relation |
| Black Brigade, Morbid Society, The Art of War, The Purge | needs_more_evidence | No independent documentation found; not rejected because absence of a source is not disproof |
| Ancient Coins | alias_of_existing → `ancients-coins` | Wikipedia and MusicBrainz use "Ancient Coins" |
| The Wisemen | alias_of_existing → `wisemen` | Same group; an empty MusicBrainz "The Wisemen" record is not a separate act |
| United Kingdom (Population Clik) | needs_more_evidence | No source for the qualifier; not merged and not aliased |
| Ghostface Killah and the other core members | alias_of_existing | Already catalogued as core records (#264); the inventory predates them |

No Spotify IDs were guessed, and no staged entry is enabled.

## Artist relationships

`relationships:` in `data/artists.yaml` is an optional top-level list of typed edges between existing artist slugs (YAML `version` stays 1; files without it load and save unchanged). A group is an editorial artist with the `affiliate_group` category or role (`wu-tang-clan` carries it as a role next to `core`); an individual is never the same record as the group.

| Type | Direction | Rule |
| --- | --- | --- |
| `member_of` | `from` is a member of `to` | `to` must be a group. An `end` date (or era) makes it a former membership. |
| `producer_for` | `from` produced for `to` | `from` must have the `producer` category. |
| `collective_or_imprint` | `from` belongs to the collective, label or imprint `to` | `to` must be a group. |
| `collaborated_with` | undirected | Stored once, lower slug in `from`. |
| `associated_with` | undirected | Documented affiliation that is neither membership nor collaboration. |

- **Cardinality**: many-to-many, at most one edge per `(type, from, to)`; undirected pairs are one edge in either order. Several stints in a group are described in `era`/`commentary`, not by repeated edges. A pair that has `member_of` cannot also carry `collaborated_with` or `associated_with`. `member_of`/`collective_or_imprint` cycles are rejected.
- **Fields**: `start`/`end` are partial dates (`YYYY`, `YYYY-MM`, `YYYY-MM-DD`) and stay empty when unknown; `era`, `commentary`, `confidence` (`low|medium|high`), `reviewer`, `reviewed_at` and `evidence` (same types as Spotify profile evidence).
- **Review**: `status` is `proposed`, `verified` or `rejected`. `verified` needs a reviewer, a confidence and at least one *independent* evidence item; `credit` and `name_match` evidence never count, so a track credit or a shared name can never create a membership. Only `verified` edges are projected to SQLite and exported; proposed/rejected claims stay in the YAML.
- **Seed**: the ten Wu-Tang Clan members, RZA in Gravediggaz, the accepted `member` entries of the candidate ledger, and `associated_with` for affiliate groups listed on Wikipedia's *List of Wu-Tang Clan affiliates*. Affiliate groups are associated with Wu-Tang Clan, and their members are *not* Wu-Tang Clan members.
- **Safety**: all edits go through `artists relationships ...` / `artists rename`, which validate the whole catalog, only fail on issues the change introduces, support `--dry-run`, and save atomically (the previous file is kept as `artists.yaml.bak`). Renaming a slug rewrites every reference; renaming onto an existing slug is blocked. An artist that is still referenced cannot be removed.
