# Spotify

The Spotify integration will use the official Spotify Web API over `net/http`.

Expected secrets:

- `SPOTIFY_CLIENT_ID`
- `SPOTIFY_CLIENT_SECRET`

The client will use Client Credentials, inject `http.Client` for tests, follow pagination, honor `Retry-After`, and avoid real API calls in unit tests or CI.
