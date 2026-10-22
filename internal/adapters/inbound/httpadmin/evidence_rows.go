package httpadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	// maxEvidenceRows mirrors the catalog's per-profile cap; the form refuses
	// more rows instead of dropping them silently.
	maxEvidenceRows = 20
	// maxEvidenceScan bounds how many submitted values are looked at at all.
	maxEvidenceScan = 4 * maxEvidenceRows
)

// evidenceFields are the parallel, repeated form keys that carry one evidence
// row each. A single group of keys is the legacy one-row form.
var evidenceFields = [5]string{"evidence_type", "evidence_url", "evidence_rationale", "evidence_release_id", "evidence_track_id"}

// evidenceDraft is one evidence row as typed or picked, before validation.
type evidenceDraft struct {
	Type, URL, Rationale, ReleaseID, TrackID string
}

func (d evidenceDraft) empty() bool {
	return d == evidenceDraft{}
}

func (d evidenceDraft) evidence() catalog.Evidence {
	return catalog.Evidence{Type: catalog.EvidenceType(d.Type), URL: d.URL, Rationale: d.Rationale, ReleaseID: d.ReleaseID, TrackID: d.TrackID}
}

func draftOf(e catalog.Evidence) evidenceDraft {
	return evidenceDraft{string(e.Type), e.URL, e.Rationale, e.ReleaseID, e.TrackID}
}

// evidenceKey identifies an evidence item by what the catalog deduplicates on.
// It is also the stable ID of a suggestion: the server re-derives suggestions
// and looks the key up, so a posted key can never carry content.
func evidenceKey(e catalog.Evidence) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{string(e.Type), e.URL, e.ReleaseID, e.TrackID}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// evidenceIndependent says whether a row can support identity on its own.
func evidenceIndependent(d evidenceDraft) bool {
	return d.evidence().Independent()
}

// readEvidenceRows reads the repeated keys positionally, keeping blank rows so
// an edit form shows what the reviewer sees. Values are trimmed and capped; at
// most maxEvidenceScan rows are read.
func readEvidenceRows(v url.Values) []evidenceDraft {
	n := 0
	for _, name := range evidenceFields {
		n = max(n, len(v[name]))
	}
	n = min(n, maxEvidenceScan)
	value := func(name string, i int) string {
		values := v[name]
		if i >= len(values) {
			return ""
		}
		s := strings.TrimSpace(values[i])
		if len(s) > maxFormText {
			s = s[:maxFormText]
		}
		return s
	}
	out := make([]evidenceDraft, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, evidenceDraft{value(evidenceFields[0], i), value(evidenceFields[1], i), value(evidenceFields[2], i), value(evidenceFields[3], i), value(evidenceFields[4], i)})
	}
	return out
}

// filledEvidence drops blank rows and refuses more than maxEvidenceRows and any
// repeat of the same item, keeping the submitted order.
func filledEvidence(rows []evidenceDraft) ([]evidenceDraft, error) {
	var out []evidenceDraft
	seen := map[string]bool{}
	for _, row := range rows {
		if row.empty() {
			continue
		}
		key := evidenceKey(row.evidence())
		if seen[key] {
			return nil, fmt.Errorf("%w: the same evidence is listed twice (%s %s); remove the repeated row", artists.ErrInvalidMapping, row.Type, row.URL)
		}
		seen[key] = true
		out = append(out, row)
	}
	if len(out) > maxEvidenceRows {
		return nil, fmt.Errorf("%w: at most %d evidence items can be recorded in one decision", artists.ErrInvalidMapping, maxEvidenceRows)
	}
	return out, nil
}

// echoEvidence writes rows back as complete groups of keys so they stay aligned.
func echoEvidence(rows []evidenceDraft) []hiddenField {
	var out []hiddenField
	for _, row := range rows {
		if row.empty() {
			continue
		}
		for i, value := range [5]string{row.Type, row.URL, row.Rationale, row.ReleaseID, row.TrackID} {
			out = append(out, hiddenField{evidenceFields[i], value})
		}
	}
	return out
}

// suggestionView is one offered source, independent or contextual.
type suggestionView struct {
	Key         string
	Evidence    catalog.Evidence
	Independent bool
	Title       string
	Detail      string
	ReleaseURL  string
	Limitation  string
	Added       bool
}

const creditLimitation = "A shared credit corroborates identity but does not prove membership of the group, so it can never verify a profile on its own."

// suggestionsOf lists every typed suggestion of a card: independent audit
// findings first, then contextual group credits, in the card's own order.
func suggestionsOf(c admin.Card) []suggestionView {
	out := make([]suggestionView, 0, len(c.Suggestions)+len(c.Credits.Suggestions))
	for _, s := range c.Suggestions {
		e := catalog.Evidence{Type: s.Type, URL: s.URL, Rationale: s.Rationale}
		out = append(out, suggestionView{Key: evidenceKey(e), Evidence: e, Independent: e.Independent(), Title: "Audit finding"})
	}
	for _, s := range c.Credits.Suggestions {
		e := s.Evidence()
		title := s.TrackName
		if s.GroupName != "" {
			title = s.GroupName + " · " + s.TrackName
		}
		out = append(out, suggestionView{Key: evidenceKey(e), Evidence: e, Independent: e.Independent(), Title: title, Detail: s.ReleaseName,
			ReleaseURL: s.ReleaseURL, Limitation: creditLimitation})
	}
	return out
}

// decisionDraft is a decision being edited: what the reviewer typed and picked,
// carried through the form, the confirmation and any error in between.
type decisionDraft struct {
	SpotifyID string
	Revision  string
	Note      string
	Override  string
	Verify    bool
	Enable    bool
	Rows      []evidenceDraft
	keys      map[string]bool
}

func newDraft(v url.Values) *decisionDraft {
	field := func(name string) string {
		s := strings.TrimSpace(v.Get(name))
		if len(s) > maxFormText {
			s = s[:maxFormText]
		}
		return s
	}
	d := &decisionDraft{
		SpotifyID: field("spotify_id"), Revision: field("revision"), Note: field("note"), Override: field("override_reason"),
		Verify: v.Get("verify") != "", Enable: v.Get("enable") != "", Rows: readEvidenceRows(v),
	}
	d.index()
	return d
}

func (d *decisionDraft) index() {
	d.keys = map[string]bool{}
	for _, row := range d.Rows {
		if !row.empty() {
			d.keys[evidenceKey(row.evidence())] = true
		}
	}
}

// Has reports whether the draft already carries the suggestion.
func (d *decisionDraft) Has(key string) bool { return d != nil && d.keys[key] }

// add appends a suggestion and says why it could not.
func (d *decisionDraft) add(s suggestionView) string {
	switch {
	case d.Has(s.Key):
		return "That evidence was already added; it is listed once."
	case len(d.Rows) >= maxEvidenceRows:
		return fmt.Sprintf("At most %d evidence items can be recorded in one decision.", maxEvidenceRows)
	}
	d.Rows = append(d.Rows, draftOf(s.Evidence))
	d.index()
	return ""
}

func (d *decisionDraft) remove(i int) {
	if i >= 0 && i < len(d.Rows) {
		d.Rows = append(d.Rows[:i:i], d.Rows[i+1:]...)
		d.index()
	}
}

func (d *decisionDraft) addBlank() string {
	if len(d.Rows) >= maxEvidenceRows {
		return fmt.Sprintf("At most %d evidence items can be recorded in one decision.", maxEvidenceRows)
	}
	d.Rows = append(d.Rows, evidenceDraft{})
	return ""
}

// evidenceRowView is what one editable evidence row renders from.
type evidenceRowView struct {
	Row         evidenceDraft
	Index       int
	Types       []option
	Edit        string
	Independent bool
	Known       bool
}
