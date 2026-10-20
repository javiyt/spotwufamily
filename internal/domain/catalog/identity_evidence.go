package catalog

import (
	"fmt"
	"strings"
)

// EvidenceKind names the source of a piece of identity evidence.
type EvidenceKind string

const (
	// Kinds that can independently prove identity when marked Strong.
	EvidenceTrackCredit         EvidenceKind = "track_credit"
	EvidenceMusicBrainzRelease  EvidenceKind = "musicbrainz_release"
	EvidenceOfficialDiscography EvidenceKind = "official_discography"
	EvidenceExternalAuthority   EvidenceKind = "external_authority"

	// Kinds that are only ranking hints or warnings and never prove identity.
	EvidenceNameSimilarity         EvidenceKind = "name_similarity"
	EvidenceGenre                  EvidenceKind = "genre"
	EvidencePopularity             EvidenceKind = "popularity"
	EvidenceReleaseMismatch        EvidenceKind = "release_mismatch"
	EvidenceMusicBrainzUnavailable EvidenceKind = "musicbrainz_unavailable"
)

type EvidencePolarity string

const (
	EvidencePositive EvidencePolarity = "positive"
	EvidenceNegative EvidencePolarity = "negative"
	EvidenceWarning  EvidencePolarity = "warning"
)

// IdentityEvidence is a machine-readable piece of evidence about whether a
// Spotify profile belongs to the configured artist.
type IdentityEvidence struct {
	Kind        EvidenceKind     `json:"kind"`
	Polarity    EvidencePolarity `json:"polarity"`
	Strong      bool             `json:"strong,omitempty"`
	Source      string           `json:"source,omitempty"`
	URL         string           `json:"url,omitempty"`
	SubjectID   string           `json:"subject_id,omitempty"`
	ReleaseName string           `json:"release_name,omitempty"`
	ReleaseYear string           `json:"release_year,omitempty"`
	Detail      string           `json:"detail,omitempty"`
}

// IsIndependentProof reports whether the evidence can, on its own, establish
// identity. Only specific kinds qualify and only when flagged strong, so a
// name, genre or popularity can never be promoted into proof.
func (e IdentityEvidence) IsIndependentProof() bool {
	if e.Polarity != EvidencePositive || !e.Strong {
		return false
	}
	switch e.Kind {
	case EvidenceTrackCredit, EvidenceMusicBrainzRelease, EvidenceOfficialDiscography, EvidenceExternalAuthority:
		return true
	default:
		return false
	}
}

// AllIdentityEvidence returns the structured evidence plus legacy verified
// track credits (RelatedArtistEvidence), which are credits on tracks of
// configured groups with known Spotify IDs.
func (c ArtistCandidate) AllIdentityEvidence() []IdentityEvidence {
	all := make([]IdentityEvidence, 0, len(c.IdentityEvidence)+len(c.RelatedArtistEvidence))
	all = append(all, c.IdentityEvidence...)
	for _, credit := range c.RelatedArtistEvidence {
		all = append(all, IdentityEvidence{
			Kind:     EvidenceTrackCredit,
			Polarity: EvidencePositive,
			Strong:   true,
			Source:   "spotify",
			URL:      spotifyURLForID(c.SpotifyID),
			Detail:   credit,
		})
	}
	return all
}

func spotifyURLForID(id string) string {
	if id == "" {
		return ""
	}
	return "https://open.spotify.com/artist/" + id
}

type EligibilityStatus string

const (
	EligibilityApproved    EligibilityStatus = "approved"
	EligibilityNeedsReview EligibilityStatus = "needs_review"
	EligibilityRejected    EligibilityStatus = "rejected"
)

const (
	DefaultMinScore    = 95
	DefaultMinScoreGap = 10
)

// EligibilityPolicy configures the gate. Zero values use the defaults.
type EligibilityPolicy struct {
	MinScore    int
	MinScoreGap int
	// IsRejected reports Spotify IDs a human already rejected.
	IsRejected func(spotifyID string) bool
}

// EligibilityDecision is the single outcome shared by the CLI, --apply,
// --enable-applied and any future UI.
type EligibilityDecision struct {
	Status    EligibilityStatus  `json:"status"`
	SpotifyID string             `json:"spotify_id,omitempty"`
	Evaluated string             `json:"evaluated_spotify_id,omitempty"`
	Score     int                `json:"score"`
	Reasons   []string           `json:"reasons"`
	Positive  []IdentityEvidence `json:"positive_evidence,omitempty"`
	Negative  []IdentityEvidence `json:"negative_evidence,omitempty"`
	Warnings  []IdentityEvidence `json:"warnings,omitempty"`
}

// EvaluateEligibility decides whether the best ranked candidate may be applied
// automatically. Ranking (score) only measures similarity; approval requires at
// least one independent strong positive signal on top of the score thresholds.
func EvaluateEligibility(artist Artist, matches []CandidateMatch, policy EligibilityPolicy) EligibilityDecision {
	if policy.MinScore == 0 {
		policy.MinScore = DefaultMinScore
	}
	if policy.MinScoreGap == 0 {
		policy.MinScoreGap = DefaultMinScoreGap
	}

	if len(matches) == 0 {
		return EligibilityDecision{Status: EligibilityNeedsReview, Reasons: []string{"no candidates found; a human must search manually"}}
	}

	best := matches[0]
	decision := EligibilityDecision{Evaluated: best.Candidate.SpotifyID, Score: best.Score}
	for _, evidence := range best.Candidate.AllIdentityEvidence() {
		switch evidence.Polarity {
		case EvidencePositive:
			decision.Positive = append(decision.Positive, evidence)
		case EvidenceNegative:
			decision.Negative = append(decision.Negative, evidence)
		default:
			decision.Warnings = append(decision.Warnings, evidence)
		}
	}

	reject := func(format string, args ...any) EligibilityDecision {
		decision.Status = EligibilityRejected
		decision.Reasons = append(decision.Reasons, fmt.Sprintf(format, args...))
		return decision
	}
	review := func(format string, args ...any) EligibilityDecision {
		decision.Status = EligibilityNeedsReview
		decision.Reasons = append(decision.Reasons, fmt.Sprintf(format, args...))
		return decision
	}

	if best.Candidate.SpotifyID == "" {
		return review("best candidate has no Spotify ID")
	}
	if policy.IsRejected != nil && policy.IsRejected(best.Candidate.SpotifyID) {
		return reject("Spotify profile was rejected in review; a name match is not new evidence")
	}
	if best.Score < policy.MinScore {
		return reject("best score %d is below minimum %d", best.Score, policy.MinScore)
	}
	if len(matches) > 1 && best.Score-matches[1].Score < policy.MinScoreGap {
		return review("ambiguous top candidates: score gap %d is below minimum %d", best.Score-matches[1].Score, policy.MinScoreGap)
	}

	proofs := 0
	for _, evidence := range decision.Positive {
		if evidence.IsIndependentProof() {
			proofs++
		}
	}
	if proofs == 0 {
		return review("no independent strong evidence (score %d from %s is similarity only; name, alias, genre and popularity are not proof)", best.Score, best.Reason)
	}
	if len(decision.Negative) > 0 {
		return review("%d strong signal(s) conflict with %d negative signal(s)", proofs, len(decision.Negative))
	}

	decision.Status = EligibilityApproved
	decision.SpotifyID = best.Candidate.SpotifyID
	decision.Reasons = append(decision.Reasons, fmt.Sprintf("approved: %d independent strong signal(s); %s", proofs, strings.TrimSpace(best.Reason)))
	return decision
}
