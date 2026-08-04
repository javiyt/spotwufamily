package catalog

import "strings"

type ArtistCandidate struct {
	Name                  string   `json:"name"`
	SpotifyID             string   `json:"spotify_id"`
	URL                   string   `json:"url"`
	ImageURL              string   `json:"image_url"`
	Images                []Image  `json:"images,omitempty"`
	Popularity            int      `json:"popularity"`
	Followers             int      `json:"followers"`
	Genres                []string `json:"genres"`
	RelatedArtistEvidence []string `json:"related_artist_evidence,omitempty"`
}

type CandidateMatch struct {
	Candidate  ArtistCandidate
	Score      int
	Confidence string
	Reason     string
}

func RankCandidates(artist Artist, candidates []ArtistCandidate) []CandidateMatch {
	matches := make([]CandidateMatch, 0, len(candidates))
	for _, candidate := range candidates {
		score, reason := scoreCandidate(artist, candidate)
		matches = append(matches, CandidateMatch{
			Candidate:  candidate,
			Score:      score,
			Confidence: confidenceForScore(score),
			Reason:     reason,
		})
	}

	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if candidateMatchLess(matches[i], matches[j]) {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	return matches
}

func candidateMatchLess(left, right CandidateMatch) bool {
	if left.Score != right.Score {
		return left.Score < right.Score
	}
	if len(left.Candidate.RelatedArtistEvidence) != len(right.Candidate.RelatedArtistEvidence) {
		return len(left.Candidate.RelatedArtistEvidence) < len(right.Candidate.RelatedArtistEvidence)
	}
	if left.Candidate.Popularity != right.Candidate.Popularity {
		return left.Candidate.Popularity < right.Candidate.Popularity
	}
	return left.Candidate.Followers < right.Candidate.Followers
}

func scoreCandidate(artist Artist, candidate ArtistCandidate) (int, string) {
	artistName := normalizeIdentity(artist.Name)
	candidateName := normalizeIdentity(candidate.Name)
	if artistName == candidateName {
		return applyGenreEvidence(100, "exact normalized name match", artist, candidate)
	}

	for _, alias := range artist.Aliases {
		if normalizeIdentity(alias) == candidateName {
			return applyGenreEvidence(95, "exact normalized alias match", artist, candidate)
		}
	}

	artistTokens := tokenSet(artistName)
	candidateTokens := tokenSet(candidateName)
	if len(artistTokens) == 0 || len(candidateTokens) == 0 {
		return 0, "empty comparable name"
	}

	shared := 0
	for token := range artistTokens {
		if _, ok := candidateTokens[token]; ok {
			shared++
		}
	}

	total := len(artistTokens)
	if len(candidateTokens) > total {
		total = len(candidateTokens)
	}

	score := shared * 80 / total
	if strings.Contains(candidateName, artistName) || strings.Contains(artistName, candidateName) {
		score += 10
	}
	if score > 90 {
		score = 90
	}

	return applyGenreEvidence(score, "token overlap", artist, candidate)
}

func applyGenreEvidence(score int, reason string, artist Artist, candidate ArtistCandidate) (int, string) {
	if len(candidate.Genres) == 0 {
		return applyRelatedArtistEvidence(score, reason, candidate)
	}
	if len(artist.Genres) > 0 {
		if genresCompatible(artist.Genres, candidate.Genres) {
			if score < 95 {
				score += 10
				if score > 94 {
					score = 94
				}
			}
			return applyRelatedArtistEvidence(score, reason+" + similar genre evidence", candidate)
		}
		score -= 20
		if score < 0 {
			score = 0
		}
		return applyRelatedArtistEvidence(score, reason+" - incompatible genre evidence", candidate)
	}
	if hasHipHopGenre(candidate.Genres) {
		if score < 95 {
			score += 8
			if score > 94 {
				score = 94
			}
		}
		return applyRelatedArtistEvidence(score, reason+" + hip-hop genre evidence", candidate)
	}

	score -= 10
	if score < 0 {
		score = 0
	}
	return applyRelatedArtistEvidence(score, reason+" - non hip-hop genre evidence", candidate)
}

func applyRelatedArtistEvidence(score int, reason string, candidate ArtistCandidate) (int, string) {
	if len(candidate.RelatedArtistEvidence) == 0 {
		return score, reason
	}
	score += 15
	if score > 100 {
		score = 100
	}
	return score, reason + " + track credit evidence"
}

func genresCompatible(expected, actual []string) bool {
	for _, expectedGenre := range expected {
		expectedNormalized := normalizeGenre(expectedGenre)
		if expectedNormalized == "" {
			continue
		}
		for _, actualGenre := range actual {
			actualNormalized := normalizeGenre(actualGenre)
			if actualNormalized == "" {
				continue
			}
			if expectedNormalized == actualNormalized {
				return true
			}
			if strings.Contains(expectedNormalized, actualNormalized) || strings.Contains(actualNormalized, expectedNormalized) {
				return true
			}
			if shareGenreFamily(expectedNormalized, actualNormalized) {
				return true
			}
		}
	}
	return false
}

func normalizeGenre(genre string) string {
	return normalizeIdentity(strings.ReplaceAll(genre, "-", " "))
}

func shareGenreFamily(left, right string) bool {
	families := []string{"hip hop", "rap", "boom bap", "trap", "r&b", "soul", "funk", "new age", "punk", "rock"}
	for _, family := range families {
		if strings.Contains(left, family) && strings.Contains(right, family) {
			return true
		}
	}
	return false
}

func hasHipHopGenre(genres []string) bool {
	for _, genre := range genres {
		normalized := normalizeGenre(genre)
		switch {
		case strings.Contains(normalized, "hip hop"):
			return true
		case strings.Contains(normalized, "rap"):
			return true
		case strings.Contains(normalized, "boom bap"):
			return true
		case strings.Contains(normalized, "wu tang"):
			return true
		case strings.Contains(normalized, "trap"):
			return true
		}
	}
	return false
}

func tokenSet(value string) map[string]struct{} {
	tokens := strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '.' || r == '\''
	})

	set := map[string]struct{}{}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token != "" {
			set[token] = struct{}{}
		}
	}

	return set
}

func confidenceForScore(score int) string {
	switch {
	case score >= 95:
		return "strong"
	case score >= 75:
		return "possible"
	default:
		return "manual_review"
	}
}
