# Spotify

The Spotify integration uses the official Spotify Web API over `net/http`.

Expected secrets:

- `SPOTIFY_CLIENT_ID`
- `SPOTIFY_CLIENT_SECRET`

The client uses Client Credentials, injects `http.Client` for tests, follows pagination, honors `Retry-After`, retries 429 and temporary 5xx responses, and avoids real API calls in unit tests or CI.

Artist images and album artwork are stored as Spotify image URLs and rendered directly from Spotify's CDN. The project does not download, transform, crop, blur, overlay, rehost or cache Spotify visual content. When Spotify images or metadata are shown, the UI includes links back to the applicable Spotify artist or album.

Implemented endpoints:

- `POST https://accounts.spotify.com/api/token`
- `GET /v1/search?type=artist`
- `GET /v1/artists/{id}`
- `GET /v1/artists/{id}/albums`
- `GET /v1/albums/{id}`
- `GET /v1/albums/{id}/tracks`
- `GET /v1/tracks?ids=...`

`sync` currently uses:

- `GET /v1/artists/{id}`
- `GET /v1/artists/{id}/albums?include_groups=album,single,compilation,appears_on`
- `GET /v1/albums/{id}`
- `GET /v1/albums/{id}/tracks`

`GET /v1/artists/{id}` does not include the artist's albums, so `sync` has to call the artist albums endpoint separately. To reduce quota usage, regular syncs skip artists completed in the same market during the previous 14 days when their configured Spotify IDs have not changed. Use `--full` to force a refresh. The sync path also reads cached album and track data from SQLite before falling back to Spotify for album and track detail calls.

References:

- Client Credentials Flow: https://developer.spotify.com/documentation/web-api/tutorials/client-credentials-flow
- Search endpoint: https://developer.spotify.com/documentation/web-api/reference/search
- Rate limits and `Retry-After`: https://developer.spotify.com/documentation/web-api/concepts/rate-limits

## Artist Resolution

Artist resolution can use Spotify directly:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily artists resolve --non-interactive --report resolve.md
```

To reduce manual work, apply strong unambiguous matches directly to `data/artists.yaml`:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily artists resolve --non-interactive --apply --report resolve.md
```

`--apply` defaults to `--min-score 95 --min-score-gap 10`, skips ambiguous candidates, validates the resulting YAML before saving, and keeps resolved artists disabled unless `--enable-applied` is provided.

### Evidence gate

A similarity score (name, alias, genre, popularity) only *ranks* candidates; it never approves one. `--apply` and `--enable-applied` write an ID only when `catalog.EvaluateEligibility` returns `approved`, the same evaluator the report and the interactive CLI use. Approval requires, on top of `--min-score`/`--min-score-gap`, at least one independent strong positive signal:

- a verified credit on a track of a configured group with a known Spotify ID (`track_credit`);
- at least two distinct Spotify albums matching MusicBrainz release groups (`musicbrainz_release`). A single shared album title can be a homonym, so it is recorded as weak evidence;
- fixtures may also supply `official_discography` or `external_authority` evidence with `"strong": true`.

Name, alias, Spotify genre and popularity are warnings/ranking hints only. MusicBrainz is advisory: outages, no release groups or a failed Spotify album fetch add a `musicbrainz_unavailable` warning and leave the candidate as `needs_review` (it is no longer discarded). Statuses are `approved`, `needs_review` (queued for a human; no catalog mutation) and `rejected` (below the score threshold or a previously rejected profile). Approved IDs are still recorded as `candidate` profiles and stay disabled unless `--enable-applied` is given and the gate passed.

Reports list positive evidence, negative evidence and warnings with source URLs, release names/years and the evaluated Spotify ID. Use `--report resolve.json` for the machine-readable form. Offline fixtures can carry `identity_evidence` (fields: `kind`, `polarity`, `strong`, `source`, `url`, `subject_id`, `release_name`, `release_year`, `detail`).

The same review is available in the local admin UI (`/resolve`, see [admin](admin.md#spotify-profile-review)): it shows these candidates side by side with the evidence of every attached ID, and records the same reviewer/date/evidence. For human-in-the-loop resolution on the command line, omit `--non-interactive`. The CLI prompts per unresolved artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily artists resolve
```

Use the candidate number to write that Spotify ID, `s` to skip, or `q` to save selected changes and quit.

Live resolution uses `data/catalog.db` as a cache for synced albums, tracks and credits before making extra Spotify album/track calls. Pass `--db` when using a non-default catalog database.

For deterministic local testing, it can still use a local JSON candidate file:

```bash
go run ./cmd/spotwufamily artists resolve --non-interactive --candidates data/artist-candidates.example.json --report resolve.md
```

Candidate JSON shape:

```json
{
  "gravediggaz": [
    {
      "name": "Gravediggaz",
      "spotify_id": "0CH4f9m2L3TRaA5oErU2p0",
      "url": "https://open.spotify.com/artist/0CH4f9m2L3TRaA5oErU2p0",
      "image_url": "",
      "popularity": 45,
      "followers": 1000,
      "genres": ["hip hop"]
    }
  ]
}
```

`data/artist-candidates.example.json` is a small offline fixture for smoke-testing this path. The JSON adapter is only for offline reports and tests.

## Auditing every configured Spotify profile

`artists audit-albums` merges the albums of all IDs of an artist before comparing them with MusicBrainz, so one wrong secondary ID can hide among authentic releases. `artists audit-profiles` evaluates every `spotify_id` and every `spotify_ids` entry on its own, including IDs of disabled artists:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... go run ./cmd/spotwufamily artists audit-profiles --report profiles-audit.md
go run ./cmd/spotwufamily artists audit-profiles --offline --artist wu-tang-clan --format json --report -
make artists-audit-profiles ARTIST_FILTER=streetlife OFFLINE=1
```

Options: `--artist`, `--offline` (cache only, no secrets, no network), `--report` (path or `-`), `--format markdown|json` (inferred from a `.json` report), `--catalog`, `--db`, `--market`, `--concurrency` (default 4) and `--quiet`.

Each profile gets a classification, a severity, an explanation and the date it was checked:

| Classification | Meaning |
| --- | --- |
| `verified` | Independent evidence supports the profile (MusicBrainz release match, credit next to another verified profile, or a manual review with independent evidence). |
| `suspicious` | Strong negative evidence (none of its albums match the known release groups) and no independent proof. |
| `ambiguous` | Duplicate ID across artists, or positive and negative evidence at the same time. |
| `unavailable` | Spotify answered 404 (removed), 429 or an outage. The profile is never called wrong because a source failed. |
| `insufficient_evidence` | Nothing contradicts or proves the profile, e.g. MusicBrainz was down, nothing is cached offline, or the profile has no albums. |

Reports list positive evidence, negative evidence, warnings and missing evidence separately. Genre mismatch, a renamed profile, release metadata anomalies, conflicting credits and unrelated albums are findings; a genre mismatch is always only a warning because cross-genre collaborations are legitimate, and an identical name or unverified group membership is never identity evidence. Output is deterministic (artist editorial order, primary before secondary IDs).

Spotify albums and tracks are read from `data/catalog.db` first and fetched at most once per ID; MusicBrainz is queried once per artist. After a Spotify 429 the remaining profiles are marked `unavailable` instead of hammering the API. Spotify profile metadata (name, genres) is not cached, so offline runs have no profile name or genre findings, and MusicBrainz is skipped offline, so offline results are mostly `insufficient_evidence` plus the structural findings.

Audit mode never writes `data/artists.yaml`. The only write path is explicit and manual:

```bash
go run ./cmd/spotwufamily artists audit-profiles --artist streetlife --spotify-id 0FCYBhXjpfQIQWNKiVEDg5 --apply
```

It re-audits that artist and only moves a `suspicious` profile to `needs_review` (exit code 3 for any other result, in particular `ambiguous`). Rejecting or verifying stays a human decision through `artists review-profile`.

The typed report (`artists.ProfileAuditReport`, produced by `artists.NewAuditProfiles(...).Run`) is the single definition used by the CLI renderers and is the use case for the later local web UI.
