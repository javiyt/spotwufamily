# Catalog Policy

`data/artists.yaml` is the editorial source of truth for Wu-Tang Clan, Wu Family, producers and collaborators included in the project.

Rules:

- Do not use artist name as internal identity once a Spotify ID is known.
- Keep artists disabled until their Spotify ID has been reviewed.
- Use categories: `core`, `affiliate_group`, `affiliate_artist`, `producer`, `collaborator`.
- Preserve aliases and notes for editorial context.
- Keep `data/groups.txt` until the YAML migration has been verified.
