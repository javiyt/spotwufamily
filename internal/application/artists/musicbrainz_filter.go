package artists

import (
	"context"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type AlbumEvidenceCandidateSearcher struct {
	searcher    CandidateSearcher
	spotify     SpotifyAlbumFetcher
	musicBrainz MusicBrainzReleaseGroupSearcher
}

func NewAlbumEvidenceCandidateSearcher(searcher CandidateSearcher, spotify SpotifyAlbumFetcher, musicBrainz MusicBrainzReleaseGroupSearcher) AlbumEvidenceCandidateSearcher {
	return AlbumEvidenceCandidateSearcher{searcher: searcher, spotify: spotify, musicBrainz: musicBrainz}
}

func (s AlbumEvidenceCandidateSearcher) SearchArtistCandidates(ctx context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	candidates, err := s.searcher.SearchArtistCandidates(ctx, artist)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return candidates, nil
	}

	releaseGroups, err := s.musicBrainz.SearchArtistAlbumReleaseGroups(ctx, artist)
	if err != nil {
		return candidates, nil
	}
	musicBrainzAlbums := auditedMusicBrainzAlbums(releaseGroups)
	if len(musicBrainzAlbums) == 0 {
		return candidates, nil
	}

	filtered := make([]catalog.ArtistCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.SpotifyID == "" {
			continue
		}
		spotifyAlbums, err := s.candidateAlbums(ctx, candidate.SpotifyID)
		if err != nil {
			continue
		}
		matched, _, _ := compareAlbums(spotifyAlbums, musicBrainzAlbums)
		if len(matched) == 0 {
			continue
		}
		filtered = append(filtered, candidate)
	}

	return filtered, nil
}

func (s AlbumEvidenceCandidateSearcher) candidateAlbums(ctx context.Context, spotifyID string) ([]AuditedAlbum, error) {
	releases, err := s.spotify.GetArtistAlbums(ctx, spotifyID, []string{"album"})
	if err != nil {
		return nil, err
	}
	albums := make([]AuditedAlbum, 0, len(releases))
	seen := map[string]struct{}{}
	for _, release := range releases {
		if release.SpotifyID == "" {
			continue
		}
		if _, ok := seen[release.SpotifyID]; ok {
			continue
		}
		seen[release.SpotifyID] = struct{}{}
		albums = append(albums, AuditedAlbum{
			ID:       release.SpotifyID,
			Title:    release.Name,
			Date:     release.ReleaseDate,
			Year:     yearFromDate(release.ReleaseDate),
			URL:      release.URL,
			SourceID: spotifyID,
		})
	}
	sortAlbums(albums)
	return albums, nil
}

func auditedMusicBrainzAlbums(releaseGroups []MusicBrainzReleaseGroup) []AuditedAlbum {
	albums := make([]AuditedAlbum, 0, len(releaseGroups))
	for _, releaseGroup := range releaseGroups {
		albums = append(albums, AuditedAlbum{
			ID:    releaseGroup.ID,
			Title: releaseGroup.Title,
			Date:  releaseGroup.FirstReleaseDate,
			Year:  yearFromDate(releaseGroup.FirstReleaseDate),
			URL:   releaseGroup.URL,
		})
	}
	sortAlbums(albums)
	return albums
}
