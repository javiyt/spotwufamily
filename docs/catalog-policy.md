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
