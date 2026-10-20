# Spotify Profile Audit

Checked on 2026-10-20 (online).

Summary: total=5 verified=2 suspicious=3 ambiguous=0 unavailable=0 insufficient_evidence=0

## Wu-Tang Clan (wu-tang-clan, enabled)

### `34EP7KEpOjXcM2TCat1ISk` (primary) - **verified** [info]

- URI: `spotify:artist:34EP7KEpOjXcM2TCat1ISk`
- Spotify name: Wu-Tang Clan
- Spotify genres: east coast hip hop | configured genres: east coast hip hop
- Review status: unreviewed | Spotify source: ok | checked: 2026-10-20
- Explanation: Independent evidence supports this profile.
- MusicBrainz: ok, release_groups=3 matched=2 unmatched=0
- Releases (2 total):
  - Enter the Wu-Tang (36 Chambers) (1993-11-09)
  - Wu-Tang Forever (1997-06-03)

**Positive evidence**

- `name_similarity`: profile name matches the configured name; a name alone is not identity evidence
- `musicbrainz_release`: 2 of 2 Spotify albums match MusicBrainz release groups

**Negative evidence**

None.

**Warnings**

None.

**Missing evidence**

None.

**Findings**

None.

### `0H8YCcvC3MPLKnbDRasGiG` (secondary) - **verified** [info]

- URI: `spotify:artist:0H8YCcvC3MPLKnbDRasGiG`
- Spotify name: Wu-Tang Clan
- Spotify genres: hip hop | configured genres: east coast hip hop
- Review status: unreviewed | Spotify source: ok | checked: 2026-10-20
- Explanation: Independent evidence supports this profile.
- MusicBrainz: ok, release_groups=3 matched=1 unmatched=0
- Releases (1 total):
  - The W (2000-11-21)

**Positive evidence**

- `name_similarity`: profile name matches the configured name; a name alone is not identity evidence
- `musicbrainz_release`: 1 of 1 Spotify albums match MusicBrainz release groups

**Negative evidence**

None.

**Warnings**

None.

**Missing evidence**

None.

**Findings**

None.

### `1aaaaaaaaaaaaaaaaaaaaa` (secondary) - **suspicious** [error]

- URI: `spotify:artist:1aaaaaaaaaaaaaaaaaaaaa`
- Spotify name: Wu Tang Clan
- Spotify genres: lullaby | configured genres: east coast hip hop
- Review status: unreviewed | Spotify source: ok | checked: 2026-10-20
- Explanation: Releases contradict the artist: none of the 2 Spotify albums match the 3 known MusicBrainz release groups.
- MusicBrainz: ok, release_groups=3 matched=0 unmatched=2
- Releases (2 total):
  - Ocean Waves (2019-02-01)
  - Rain Sounds for Sleep (2019-01-01)

**Positive evidence**

- `name_similarity`: profile name matches the configured name; a name alone is not identity evidence

**Negative evidence**

- `release_mismatch`: none of the 2 Spotify albums match the 3 known MusicBrainz release groups

**Warnings**

- `genre`: profile genres [lullaby] share nothing with configured genres [east coast hip hop]; legitimate cross-genre collaborations exist, so this is a warning only

**Missing evidence**

None.

**Findings**

- `genre_mismatch` [warning]: profile genres [lullaby] share nothing with configured genres [east coast hip hop]; legitimate cross-genre collaborations exist, so this is a warning only
- `likely_homonym` [warning]: the profile has the same name as the artist but unrelated releases; an identical name is not identity evidence
- `unrelated_albums` [error]: none of the 2 Spotify albums match the 3 known MusicBrainz release groups

## Streetlife (streetlife, enabled)

### `0FCYBhXjpfQIQWNKiVEDg5` (primary) - **suspicious** [error]

- URI: `spotify:artist:0FCYBhXjpfQIQWNKiVEDg5`
- Spotify name: Streetlife
- Spotify genres: acid jazz, nu jazz | configured genres: east coast hip hop
- Review status: unreviewed | Spotify source: ok | checked: 2026-10-20
- Explanation: Releases contradict the artist: none of the 1 Spotify albums match the 1 known MusicBrainz release groups.
- MusicBrainz: ok, release_groups=1 matched=0 unmatched=1
- Releases (1 total):
  - Moonlit Lounge (2005-05-05)

**Positive evidence**

- `name_similarity`: profile name matches the configured name; a name alone is not identity evidence

**Negative evidence**

- `release_mismatch`: none of the 1 Spotify albums match the 1 known MusicBrainz release groups

**Warnings**

- `genre`: profile genres [acid jazz, nu jazz] share nothing with configured genres [east coast hip hop]; legitimate cross-genre collaborations exist, so this is a warning only

**Missing evidence**

None.

**Findings**

- `genre_mismatch` [warning]: profile genres [acid jazz, nu jazz] share nothing with configured genres [east coast hip hop]; legitimate cross-genre collaborations exist, so this is a warning only
- `likely_homonym` [warning]: the profile has the same name as the artist but unrelated releases; an identical name is not identity evidence
- `unrelated_albums` [error]: none of the 1 Spotify albums match the 1 known MusicBrainz release groups

## K.G.B. (k-g-b, disabled)

### `2gKwx0Lm0JoOrN2mY9Z0PG` (primary) - **suspicious** [error]

- URI: `spotify:artist:2gKwx0Lm0JoOrN2mY9Z0PG`
- Spotify name: K.G.B.
- Spotify genres: chicago house | configured genres: east coast hip hop
- Review status: unreviewed | Spotify source: ok | checked: 2026-10-20
- Explanation: Releases contradict the artist: none of the 1 Spotify albums match the 1 known MusicBrainz release groups.
- MusicBrainz: ok, release_groups=1 matched=0 unmatched=1
- Releases (1 total):
  - Warehouse Nights (2011-03-03)

**Positive evidence**

- `name_similarity`: profile name matches the configured name; a name alone is not identity evidence

**Negative evidence**

- `release_mismatch`: none of the 1 Spotify albums match the 1 known MusicBrainz release groups

**Warnings**

- `genre`: profile genres [chicago house] share nothing with configured genres [east coast hip hop]; legitimate cross-genre collaborations exist, so this is a warning only

**Missing evidence**

None.

**Findings**

- `genre_mismatch` [warning]: profile genres [chicago house] share nothing with configured genres [east coast hip hop]; legitimate cross-genre collaborations exist, so this is a warning only
- `likely_homonym` [warning]: the profile has the same name as the artist but unrelated releases; an identical name is not identity evidence
- `unrelated_albums` [error]: none of the 1 Spotify albums match the 1 known MusicBrainz release groups

