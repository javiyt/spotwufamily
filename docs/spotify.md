# Spotify

The Spotify integration uses the official Spotify Web API over `net/http`.

Expected secrets:

- `SPOTIFY_CLIENT_ID`
- `SPOTIFY_CLIENT_SECRET`

The client uses Client Credentials, injects `http.Client` for tests, follows pagination, honors `Retry-After`, retries 429 and temporary 5xx responses, and avoids real API calls in unit tests or CI.

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

For human-in-the-loop resolution, omit `--non-interactive`. The CLI prompts per unresolved artist:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  go run ./cmd/spotwufamily artists resolve
```

Use the candidate number to write that Spotify ID, `s` to skip, or `q` to save selected changes and quit.

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
