package artists

import (
	"context"
	"fmt"
	"sync"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type AlbumEvidenceCandidateSearcher struct {
	searcher    CandidateSearcher
	spotify     SpotifyAlbumFetcher
	musicBrainz MusicBrainzReleaseGroupSearcher

	trackCreditMu     sync.Mutex
	trackCreditCache  map[string][]configuredArtistTrackCredit
	trackCreditErrors map[string]error
}

type ConfiguredSpotifyIDWarning struct {
	SpotifyID             string
	Reason                string
	SpotifyAlbumCount     int
	MusicBrainzAlbumCount int
	MatchedAlbumCount     int
	SpotifyAlbums         []AuditedAlbum
	MusicBrainzAlbums     []AuditedAlbum
}

type configuredArtistTrackCredit struct {
	ArtistID   string
	TrackName  string
	AlbumName  string
	SourceName string
}

type spotifyAlbumTrackFetcher interface {
	GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error)
	GetAlbumTracks(context.Context, string) ([]catalog.Track, error)
}

func NewAlbumEvidenceCandidateSearcher(searcher CandidateSearcher, spotify SpotifyAlbumFetcher, musicBrainz MusicBrainzReleaseGroupSearcher) *AlbumEvidenceCandidateSearcher {
	return &AlbumEvidenceCandidateSearcher{
		searcher:          searcher,
		spotify:           spotify,
		musicBrainz:       musicBrainz,
		trackCreditCache:  map[string][]configuredArtistTrackCredit{},
		trackCreditErrors: map[string]error{},
	}
}

func (s *AlbumEvidenceCandidateSearcher) SearchArtistCandidates(ctx context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	return s.searchArtistCandidates(ctx, artist, nil)
}

func (s *AlbumEvidenceCandidateSearcher) SearchArtistCandidatesWithCatalog(ctx context.Context, artist catalog.Artist, editorialCatalog catalog.EditorialCatalog) ([]catalog.ArtistCandidate, error) {
	return s.searchArtistCandidates(ctx, artist, &editorialCatalog)
}

func (s *AlbumEvidenceCandidateSearcher) searchArtistCandidates(ctx context.Context, artist catalog.Artist, editorialCatalog *catalog.EditorialCatalog) ([]catalog.ArtistCandidate, error) {
	candidates, err := s.searcher.SearchArtistCandidates(ctx, artist)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return candidates, nil
	}

	trackEvidence := map[string][]string{}
	if editorialCatalog != nil {
		trackEvidence = s.configuredArtistTrackEvidence(ctx, artist, candidates, *editorialCatalog)
		candidates = applyTrackEvidence(candidates, trackEvidence)
	}
	candidates = filterResolvableCandidates(artist, candidates)
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
			if len(trackEvidence[candidate.SpotifyID]) > 0 {
				filtered = append(filtered, candidate)
			}
			continue
		}
		matched, _, _ := compareAlbums(spotifyAlbums, musicBrainzAlbums)
		if len(matched) == 0 && len(trackEvidence[candidate.SpotifyID]) == 0 {
			continue
		}
		filtered = append(filtered, candidate)
	}

	return filtered, nil
}

func (s *AlbumEvidenceCandidateSearcher) configuredArtistTrackEvidence(ctx context.Context, target catalog.Artist, candidates []catalog.ArtistCandidate, editorialCatalog catalog.EditorialCatalog) map[string][]string {
	trackFetcher, ok := s.spotify.(spotifyAlbumTrackFetcher)
	if !ok {
		return nil
	}

	candidateIDs := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate.SpotifyID != "" {
			candidateIDs[candidate.SpotifyID] = struct{}{}
		}
	}
	if len(candidateIDs) == 0 {
		return nil
	}

	evidence := map[string][]string{}
	for _, source := range editorialCatalog.Artists {
		if source.Slug == target.Slug || !isConfiguredGroup(source) || len(source.AllSpotifyIDs()) == 0 {
			continue
		}
		for _, spotifyID := range source.AllSpotifyIDs() {
			credits, err := s.configuredArtistTrackCredits(ctx, trackFetcher, source, spotifyID)
			if err != nil {
				continue
			}
			for _, credit := range credits {
				if _, ok := candidateIDs[credit.ArtistID]; !ok {
					continue
				}
				if len(evidence[credit.ArtistID]) >= 3 {
					continue
				}
				evidence[credit.ArtistID] = append(evidence[credit.ArtistID], formatTrackCreditEvidence(credit))
			}
		}
	}

	return evidence
}

func (s *AlbumEvidenceCandidateSearcher) configuredArtistTrackCredits(ctx context.Context, trackFetcher spotifyAlbumTrackFetcher, source catalog.Artist, spotifyID string) ([]configuredArtistTrackCredit, error) {
	s.trackCreditMu.Lock()
	if s.trackCreditCache == nil {
		s.trackCreditCache = map[string][]configuredArtistTrackCredit{}
	}
	if s.trackCreditErrors == nil {
		s.trackCreditErrors = map[string]error{}
	}
	if credits, ok := s.trackCreditCache[spotifyID]; ok {
		s.trackCreditMu.Unlock()
		return credits, nil
	}
	if err, ok := s.trackCreditErrors[spotifyID]; ok {
		s.trackCreditMu.Unlock()
		return nil, err
	}
	s.trackCreditMu.Unlock()

	releases, err := trackFetcher.GetArtistAlbums(ctx, spotifyID, []string{"album"})
	if err != nil {
		s.rememberTrackCreditError(spotifyID, err)
		return nil, err
	}

	var credits []configuredArtistTrackCredit
	seenAlbums := map[string]string{}
	for _, release := range releases {
		if release.SpotifyID == "" {
			continue
		}
		if _, ok := seenAlbums[release.SpotifyID]; ok {
			continue
		}
		seenAlbums[release.SpotifyID] = release.Name
		tracks, err := trackFetcher.GetAlbumTracks(ctx, release.SpotifyID)
		if err != nil {
			s.rememberTrackCreditError(spotifyID, err)
			return nil, err
		}
		for _, track := range tracks {
			for _, artist := range track.Artists {
				if artist.SpotifyID == "" || source.HasSpotifyID(artist.SpotifyID) {
					continue
				}
				credits = append(credits, configuredArtistTrackCredit{
					ArtistID:   artist.SpotifyID,
					TrackName:  track.Name,
					AlbumName:  release.Name,
					SourceName: source.Name,
				})
			}
		}
	}

	s.trackCreditMu.Lock()
	s.trackCreditCache[spotifyID] = credits
	s.trackCreditMu.Unlock()
	return credits, nil
}

func (s *AlbumEvidenceCandidateSearcher) rememberTrackCreditError(spotifyID string, err error) {
	s.trackCreditMu.Lock()
	defer s.trackCreditMu.Unlock()
	if s.trackCreditErrors == nil {
		s.trackCreditErrors = map[string]error{}
	}
	s.trackCreditErrors[spotifyID] = err
}

func applyTrackEvidence(candidates []catalog.ArtistCandidate, evidence map[string][]string) []catalog.ArtistCandidate {
	if len(evidence) == 0 {
		return candidates
	}
	enriched := make([]catalog.ArtistCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if items := evidence[candidate.SpotifyID]; len(items) > 0 {
			candidate.RelatedArtistEvidence = append(candidate.RelatedArtistEvidence, items...)
		}
		enriched = append(enriched, candidate)
	}
	return enriched
}

func filterResolvableCandidates(artist catalog.Artist, candidates []catalog.ArtistCandidate) []catalog.ArtistCandidate {
	matches := catalog.RankCandidates(artist, candidates)
	filtered := make([]catalog.ArtistCandidate, 0, len(candidates))
	for _, match := range matches {
		if match.Score >= 50 || len(match.Candidate.RelatedArtistEvidence) > 0 {
			filtered = append(filtered, match.Candidate)
		}
	}
	return filtered
}

func isConfiguredGroup(artist catalog.Artist) bool {
	if artist.Category == catalog.CategoryCore || artist.Category == catalog.CategoryAffiliateGroup {
		return true
	}
	for _, role := range artist.Roles {
		if role == catalog.CategoryCore || role == catalog.CategoryAffiliateGroup {
			return true
		}
	}
	return false
}

func formatTrackCreditEvidence(credit configuredArtistTrackCredit) string {
	switch {
	case credit.SourceName != "" && credit.TrackName != "" && credit.AlbumName != "":
		return fmt.Sprintf("credited on %s track %q from %q", credit.SourceName, credit.TrackName, credit.AlbumName)
	case credit.SourceName != "" && credit.TrackName != "":
		return fmt.Sprintf("credited on %s track %q", credit.SourceName, credit.TrackName)
	case credit.SourceName != "":
		return fmt.Sprintf("credited on %s album track", credit.SourceName)
	default:
		return "credited on configured artist album track"
	}
}

func (s *AlbumEvidenceCandidateSearcher) ReviewConfiguredSpotifyIDs(ctx context.Context, artist catalog.Artist) ([]ConfiguredSpotifyIDWarning, error) {
	spotifyIDs := artist.AllSpotifyIDs()
	if len(spotifyIDs) == 0 {
		return nil, nil
	}

	releaseGroups, err := s.musicBrainz.SearchArtistAlbumReleaseGroups(ctx, artist)
	if err != nil {
		return nil, err
	}
	musicBrainzAlbums := auditedMusicBrainzAlbums(releaseGroups)
	if len(musicBrainzAlbums) == 0 {
		return nil, nil
	}

	var warnings []ConfiguredSpotifyIDWarning
	for _, spotifyID := range spotifyIDs {
		spotifyAlbums, err := s.candidateAlbums(ctx, spotifyID)
		if err != nil {
			warnings = append(warnings, ConfiguredSpotifyIDWarning{
				SpotifyID:             spotifyID,
				Reason:                "could not fetch Spotify albums for configured ID",
				MusicBrainzAlbumCount: len(musicBrainzAlbums),
				MusicBrainzAlbums:     firstAuditedAlbums(musicBrainzAlbums, 3),
			})
			continue
		}
		matched, _, _ := compareAlbums(spotifyAlbums, musicBrainzAlbums)
		if len(matched) > 0 {
			continue
		}
		warnings = append(warnings, ConfiguredSpotifyIDWarning{
			SpotifyID:             spotifyID,
			Reason:                "no Spotify albums matched MusicBrainz release groups",
			SpotifyAlbumCount:     len(spotifyAlbums),
			MusicBrainzAlbumCount: len(musicBrainzAlbums),
			MatchedAlbumCount:     len(matched),
			SpotifyAlbums:         firstAuditedAlbums(spotifyAlbums, 3),
			MusicBrainzAlbums:     firstAuditedAlbums(musicBrainzAlbums, 3),
		})
	}

	return warnings, nil
}

func (s *AlbumEvidenceCandidateSearcher) candidateAlbums(ctx context.Context, spotifyID string) ([]AuditedAlbum, error) {
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

func firstAuditedAlbums(albums []AuditedAlbum, limit int) []AuditedAlbum {
	if limit <= 0 || len(albums) == 0 {
		return nil
	}
	if len(albums) < limit {
		limit = len(albums)
	}
	return append([]AuditedAlbum(nil), albums[:limit]...)
}
