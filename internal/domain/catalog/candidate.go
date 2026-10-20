package catalog

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CandidateStatus is the editorial decision about a possible Wu Family entry.
type CandidateStatus string

const (
	CandidatePending           CandidateStatus = "pending"
	CandidateAccepted          CandidateStatus = "accepted"
	CandidateRejected          CandidateStatus = "rejected"
	CandidateNeedsMoreEvidence CandidateStatus = "needs_more_evidence"
	CandidateAliasOfExisting   CandidateStatus = "alias_of_existing"
)

// CandidateKind tells whether a candidate is a person or a group.
type CandidateKind string

const (
	CandidateKindArtist CandidateKind = "artist"
	CandidateKindGroup  CandidateKind = "group"
)

// CandidateConfidence is the confidence the source assigns to its own claim.
// It never substitutes for evidence.
type CandidateConfidence string

const (
	ConfidenceLow    CandidateConfidence = "low"
	ConfidenceMedium CandidateConfidence = "medium"
	ConfidenceHigh   CandidateConfidence = "high"
)

// WuRelation classifies how an accepted candidate relates to Wu-Tang Clan.
type WuRelation string

const (
	RelationCreator      WuRelation = "creator"
	RelationMember       WuRelation = "member"
	RelationProducer     WuRelation = "producer"
	RelationCollaborator WuRelation = "collaborator"
	RelationAssociation  WuRelation = "association"
)

const minCandidateEvidence = 2

// CandidateSource is the provenance of one import: where a name was claimed.
type CandidateSource struct {
	Title string
	URL   string
}

// Candidate is a possible catalog entry that has not been verified. Nothing
// in a candidate is an identity claim until it is accepted with evidence.
type Candidate struct {
	Name             string
	Kind             CandidateKind
	Status           CandidateStatus
	Confidence       CandidateConfidence
	EvidenceCategory string
	// RelationClaim is what the source asserted; it is unverified text.
	RelationClaim string
	// Relation and RelationGroups are the curated classification, set on review.
	Relation       WuRelation
	RelationGroups []string
	Category       Category
	Roles          []Category
	ProposedSlug   string
	AliasOf        string
	Aliases        []string
	ExternalIDs    map[string]string
	Sources        []CandidateSource
	Evidence       []Evidence
	Reason         string
	Reviewer       string
	ReviewedAt     string
	// PossibleDuplicateOf flags a loose (punctuation-insensitive) match to an
	// existing entry that was not merged automatically.
	PossibleDuplicateOf string
	// StagedSlug and AliasApplied record what staging changed in the catalog,
	// so it can be reverted.
	StagedSlug   string
	AliasApplied bool
}

// CandidateLedger is the reviewable inventory of candidates and decisions.
type CandidateLedger struct {
	Version    int
	Candidates []Candidate
}

var candidateFolder = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a", "æ", "ae",
	"ç", "c", "ć", "c", "č", "c", "ď", "d", "đ", "d",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ė", "e", "ę", "e", "ě", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ī", "i", "ı", "i",
	"ł", "l", "ñ", "n", "ń", "n", "ň", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "ō", "o", "ő", "o", "œ", "oe",
	"ř", "r", "ś", "s", "š", "s", "ş", "s", "ß", "ss", "ť", "t",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u",
	"ý", "y", "ÿ", "y", "ź", "z", "ż", "z", "ž", "z",
	"’", "", "‘", "", "ʼ", "", "'", "", "`", "",
)

// CandidateKey is the strict identity key of a name: case, accents and
// apostrophes are ignored; other punctuation separates words.
func CandidateKey(name string) string {
	return Slugify(candidateFolder.Replace(strings.ToLower(name)))
}

// CandidateLooseKey also ignores word separators, so "N-Tyce" and "NTyce"
// share a loose key. A loose match is a warning, never a merge.
func CandidateLooseKey(name string) string {
	return strings.ReplaceAll(CandidateKey(name), "-", "")
}

// Key returns the strict key of the candidate name.
func (c Candidate) Key() string { return CandidateKey(c.Name) }

// Keys returns the strict keys of the name and every alias, without repeats.
func (c Candidate) Keys() []string {
	keys := make([]string, 0, 1+len(c.Aliases))
	seen := map[string]struct{}{}
	for _, value := range append([]string{c.Name}, c.Aliases...) {
		key := CandidateKey(value)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func IsKnownCandidateStatus(status CandidateStatus) bool {
	switch status {
	case CandidatePending, CandidateAccepted, CandidateRejected, CandidateNeedsMoreEvidence, CandidateAliasOfExisting:
		return true
	default:
		return false
	}
}

func IsKnownCandidateKind(kind CandidateKind) bool {
	return kind == CandidateKindArtist || kind == CandidateKindGroup
}

func IsKnownWuRelation(relation WuRelation) bool {
	switch relation {
	case RelationCreator, RelationMember, RelationProducer, RelationCollaborator, RelationAssociation:
		return true
	default:
		return false
	}
}

// ParseCandidateConfidence maps source wording (English or Spanish) to a
// confidence; anything unrecognised is low.
func ParseCandidateConfidence(value string) CandidateConfidence {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high", "alta", "alto":
		return ConfidenceHigh
	case "medium", "media", "medio":
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// IndependentEvidenceTypes counts distinct source types among the independent
// evidence items. Two items of one type are one source of corroboration.
func IndependentEvidenceTypes(evidence []Evidence) int {
	types := map[EvidenceType]struct{}{}
	for _, item := range evidence {
		if item.Independent() && item.URL != "" {
			types[item.Type] = struct{}{}
		}
	}
	return len(types)
}

// ValidateCandidateLedger checks the ledger against the editorial catalog.
// Accepting a candidate is the only way new identity enters the catalog, so
// it needs reviewed, corroborated, permanent evidence.
func ValidateCandidateLedger(ledger CandidateLedger, c EditorialCatalog) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	if ledger.Version != 1 {
		issues = append(issues, ValidationIssue{Field: "version", Message: "must be 1"})
	}
	slugs := map[string]struct{}{}
	for _, artist := range c.Artists {
		slugs[artist.Slug] = struct{}{}
	}

	keys := map[string]int{}
	for i, candidate := range ledger.Candidates {
		prefix := fmt.Sprintf("candidates[%d]", i)
		add := func(field, message string) {
			issues = append(issues, ValidationIssue{Field: prefix + "." + field, Message: message})
		}

		key := candidate.Key()
		switch {
		case key == "":
			add("name", "is required")
		default:
			if previous, ok := keys[key]; ok {
				add("name", fmt.Sprintf("duplicate candidate; first seen at candidates[%d]", previous))
			} else {
				keys[key] = i
			}
		}
		if !IsKnownCandidateKind(candidate.Kind) {
			add("kind", "must be artist or group")
		}
		if !IsKnownCandidateStatus(candidate.Status) {
			add("status", "is unknown")
			continue
		}
		if candidate.Category != "" && !IsKnownCategory(candidate.Category) {
			add("category", "is unknown")
		}
		for roleIndex, role := range candidate.Roles {
			if !IsKnownCategory(role) {
				add(fmt.Sprintf("roles[%d]", roleIndex), "is unknown")
			}
		}
		if candidate.ProposedSlug != "" && !slugPattern.MatchString(candidate.ProposedSlug) {
			add("proposed_slug", "must use lowercase letters, numbers and single hyphens")
		}
		if candidate.ReviewedAt != "" && !isUTCTimestamp(candidate.ReviewedAt) {
			add("reviewed_at", "must be an RFC 3339 UTC timestamp")
		}
		for evidenceIndex, item := range candidate.Evidence {
			for _, issue := range validateEvidence(fmt.Sprintf("%s.evidence[%d]", prefix, evidenceIndex), item) {
				issues = append(issues, issue)
			}
		}

		switch candidate.Status {
		case CandidateAccepted:
			if candidate.Reviewer == "" || candidate.ReviewedAt == "" {
				add("reviewer", "accepted candidates need a reviewer and reviewed_at")
			}
			if !IsKnownCategory(candidate.Category) {
				add("category", "accepted candidates need a category")
			}
			if len(candidate.Roles) > 0 && !containsCategory(candidate.Roles, candidate.Category) {
				add("roles", "must include primary category")
			}
			if !IsKnownWuRelation(candidate.Relation) {
				add("relation", "accepted candidates need a Wu relation: creator, member, producer, collaborator or association")
			}
			if strings.TrimSpace(candidate.RelationClaim) == "" {
				add("relation_claim", "accepted candidates need a relation claim")
			}
			if IndependentEvidenceTypes(candidate.Evidence) < minCandidateEvidence {
				add("evidence", fmt.Sprintf("accepted candidates need independent evidence with a URL from at least %d different source types", minCandidateEvidence))
			}
			for _, group := range candidate.RelationGroups {
				if _, ok := slugs[group]; !ok {
					add("relation_groups", fmt.Sprintf("unknown catalog slug %q", group))
				}
			}
			if candidate.StagedSlug != "" {
				if _, ok := slugs[candidate.StagedSlug]; !ok {
					add("staged_slug", fmt.Sprintf("staged entry %q is not in the catalog", candidate.StagedSlug))
				}
			}
		case CandidateRejected:
			if candidate.Reviewer == "" || candidate.ReviewedAt == "" {
				add("reviewer", "rejected candidates need a reviewer and reviewed_at")
			}
			if strings.TrimSpace(candidate.Reason) == "" {
				add("reason", "rejected candidates need a reason")
			}
		case CandidateNeedsMoreEvidence:
			if strings.TrimSpace(candidate.Reason) == "" {
				add("reason", "needs_more_evidence candidates must say what is missing")
			}
		case CandidateAliasOfExisting:
			if candidate.AliasOf == "" {
				add("alias_of", "is required")
			} else if _, ok := slugs[candidate.AliasOf]; !ok {
				add("alias_of", fmt.Sprintf("unknown catalog slug %q", candidate.AliasOf))
			}
		}
		if candidate.Status != CandidateAccepted && candidate.StagedSlug != "" {
			add("staged_slug", "only accepted candidates can be staged")
		}
	}
	return issues
}

func containsCategory(categories []Category, want Category) bool {
	for _, category := range categories {
		if category == want {
			return true
		}
	}
	return false
}

// CandidateReview is one manual decision about a candidate.
type CandidateReview struct {
	Status         CandidateStatus
	Reviewer       string
	Reason         string
	Category       Category
	Roles          []Category
	Relation       WuRelation
	RelationGroups []string
	RelationClaim  string
	AliasOf        string
	Aliases        []string
	ProposedSlug   string
	ExternalIDs    map[string]string
	Evidence       []Evidence
	At             time.Time
}

var (
	ErrCandidateStaged        = errors.New("candidate is staged in the catalog; revert staging first")
	ErrCandidateNeedsNewProof = errors.New("leaving rejected requires at least one new independent evidence item")
)

// ReviewCandidate applies a manual decision. Imports and discovery can only
// create pending candidates; every other status is set here, by a person.
func ReviewCandidate(current Candidate, review CandidateReview) (Candidate, error) {
	if review.Status == CandidatePending || !IsKnownCandidateStatus(review.Status) {
		return current, fmt.Errorf("status must be accepted, rejected, needs_more_evidence or alias_of_existing")
	}
	if strings.TrimSpace(review.Reviewer) == "" {
		return current, errors.New("reviewer is required")
	}
	if current.StagedSlug != "" || current.AliasApplied {
		return current, ErrCandidateStaged
	}

	next := current
	next.Evidence = append([]Evidence(nil), current.Evidence...)
	known := map[string]struct{}{}
	for _, item := range current.Evidence {
		known[item.key()] = struct{}{}
	}
	newIndependent := 0
	for _, item := range review.Evidence {
		if _, ok := known[item.key()]; ok {
			continue
		}
		known[item.key()] = struct{}{}
		next.Evidence = append(next.Evidence, item)
		if item.Independent() {
			newIndependent++
		}
	}
	if current.Status == CandidateRejected && review.Status != CandidateRejected && newIndependent == 0 {
		return current, ErrCandidateNeedsNewProof
	}

	next.Status = review.Status
	next.Reviewer = strings.TrimSpace(review.Reviewer)
	next.ReviewedAt = review.At.UTC().Format(time.RFC3339)
	next.Reason = strings.TrimSpace(review.Reason)
	if review.Category != "" {
		next.Category = review.Category
	}
	if len(review.Roles) > 0 {
		next.Roles = append([]Category(nil), review.Roles...)
	}
	if review.Relation != "" {
		next.Relation = review.Relation
	}
	if len(review.RelationGroups) > 0 {
		next.RelationGroups = append([]string(nil), review.RelationGroups...)
	}
	if strings.TrimSpace(review.RelationClaim) != "" {
		next.RelationClaim = strings.TrimSpace(review.RelationClaim)
	}
	if review.AliasOf != "" {
		next.AliasOf = review.AliasOf
	}
	if review.ProposedSlug != "" {
		next.ProposedSlug = review.ProposedSlug
	}
	next.Aliases = mergeNames(next.Aliases, review.Aliases, next.Name)
	if len(review.ExternalIDs) > 0 {
		merged := map[string]string{}
		for k, v := range current.ExternalIDs {
			merged[k] = v
		}
		for k, v := range review.ExternalIDs {
			merged[k] = v
		}
		next.ExternalIDs = merged
	}
	if next.Status == CandidateAccepted && next.Category != "" && len(next.Roles) == 0 {
		next.Roles = []Category{next.Category}
	}
	next.PossibleDuplicateOf = ""
	return next, nil
}

// MergeCandidateNames returns base plus any extra names whose key is new,
// excluding the primary name, in a stable order.
func MergeCandidateNames(base, extra []string, primary string) []string {
	return mergeNames(base, extra, primary)
}

func mergeNames(base, extra []string, primary string) []string {
	seen := map[string]struct{}{CandidateKey(primary): {}}
	out := make([]string, 0, len(base)+len(extra))
	for _, name := range append(append([]string(nil), base...), extra...) {
		name = strings.TrimSpace(name)
		key := CandidateKey(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

// SortCandidates orders a ledger deterministically by strict key.
func SortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].Key(), candidates[j].Key()
		if a != b {
			return a < b
		}
		return candidates[i].Name < candidates[j].Name
	})
}
