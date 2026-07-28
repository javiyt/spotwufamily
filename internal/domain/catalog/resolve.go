package catalog

import "strings"

type ArtistCandidate struct {
	Name       string   `json:"name"`
	SpotifyID  string   `json:"spotify_id"`
	URL        string   `json:"url"`
	ImageURL   string   `json:"image_url"`
	Popularity int      `json:"popularity"`
	Followers  int      `json:"followers"`
	Genres     []string `json:"genres"`
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
			if matches[j].Score > matches[i].Score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	return matches
}

func scoreCandidate(artist Artist, candidate ArtistCandidate) (int, string) {
	artistName := normalizeIdentity(artist.Name)
	candidateName := normalizeIdentity(candidate.Name)
	if artistName == candidateName {
		return 100, "exact normalized name match"
	}

	for _, alias := range artist.Aliases {
		if normalizeIdentity(alias) == candidateName {
			return 95, "exact normalized alias match"
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

	return score, "token overlap"
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
