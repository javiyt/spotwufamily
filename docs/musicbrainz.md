# MusicBrainz

MusicBrainz is used as an advisory catalog-audit source.

The album audit compares:

- Spotify albums returned by every configured `spotify_id` and `spotify_ids` value for an artist.
- MusicBrainz release groups with `primarytype:album` for the same editorial artist name.

Run:

```bash
SPOTIFY_CLIENT_ID=... SPOTIFY_CLIENT_SECRET=... \
  make artists-audit-albums ARTIST=wu-tang-clan
```

The command writes `albums-audit.md` by default. It does not modify YAML, SQLite or generated site data.

The report sections are:

- `Matched`: normalized title and year match between MusicBrainz and Spotify.
- `Missing From Spotify`: MusicBrainz album release groups not found in Spotify results.
- `Suspicious Spotify Albums`: Spotify albums not matched to MusicBrainz release groups.

Use this report to decide whether an artist needs more Spotify profile IDs, a corrected primary ID or manual review of suspicious releases.
