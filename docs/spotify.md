# Spotify

The Spotify integration will use the official Spotify Web API over `net/http`.

Expected secrets:

- `SPOTIFY_CLIENT_ID`
- `SPOTIFY_CLIENT_SECRET`

The client will use Client Credentials, inject `http.Client` for tests, follow pagination, honor `Retry-After`, and avoid real API calls in unit tests or CI.

## Artist Resolution

Phase 2 includes the domain/application side of resolution without calling Spotify:

```bash
go run ./cmd/spotwufamily artists resolve --non-interactive --candidates candidates.json --report resolve.md
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

The phase 3 HTTP adapter will implement the same search port using Spotify.
