# Catalog Policy

`data/artists.yaml` is the editorial source of truth for Wu-Tang Clan, Wu Family, producers and collaborators included in the project.

Rules:

- Do not use artist name as internal identity once a Spotify ID is known.
- Use `spotify_id` as the primary Spotify profile and `spotify_ids` for additional Spotify profiles that represent the same editorial artist or group.
- Keep artists disabled until their Spotify ID has been reviewed.
- Use categories: `core`, `affiliate_group`, `affiliate_artist`, `producer`, `collaborator`.
- Keep the primary `category` present in `roles` whenever roles are listed.
- Use `public_name` only when the display name should intentionally differ from `name`.
- Use `external_url` only for HTTPS editorial references.
- Use `added_at` in `YYYY-MM-DD` format.
- Preserve aliases and notes for editorial context.
- Keep `data/groups.txt` until the YAML migration has been verified.

Validation detects duplicate slugs, names, Spotify IDs, aliases, roles and editorial order values, plus unknown categories and malformed Spotify IDs.
