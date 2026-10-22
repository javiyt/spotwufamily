package httpadmin_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	yamlcatalog "github.com/javiyt/spotwufamily/internal/adapters/outbound/yaml"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	evTrack   = "T1111111111111111111aa"
	evRelease = "A1111111111111111111aa"
	evMBGroup = "https://musicbrainz.org/release-group/mb1"
)

var addSuggestionPattern = regexp.MustCompile(`name="add_suggestion" value="([0-9a-f]+)"`)

// newEvidenceEnv serves a Newcomer whose audit finds one MusicBrainz release
// group and one credit next to a verified group, and links it as a candidate.
func newEvidenceEnv(t *testing.T) (*resolveEnv, string) {
	t.Helper()
	env := newResolveEnv(t, func(e *resolveEnv, _ *admin.ResolutionDeps) {
		e.spotify.tracks = map[string][]catalog.Track{evRelease: {{SpotifyID: evTrack, Name: "Opener",
			Artists: []catalog.ArtistCandidate{{SpotifyID: rzNewcomer, Name: "Newcomer"}, {SpotifyID: rzWu, Name: "Wu-Tang Clan"}}}}}
	})
	rev := env.workbenchRevision("newcomer", "?q="+rzNewcomer)
	require.Equal(t, http.StatusSeeOther, env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")).Code)
	return env, env.workbenchRevision("newcomer", "")
}

// rows builds repeated evidence_* keys, one value per key and row.
func rows(v url.Values, evidence ...[5]string) url.Values {
	for _, e := range evidence {
		v.Add("evidence_type", e[0])
		v.Add("evidence_url", e[1])
		v.Add("evidence_rationale", e[2])
		v.Add("evidence_release_id", e[3])
		v.Add("evidence_track_id", e[4])
	}
	return v
}

var (
	rowMB     = [5]string{"musicbrainz", evMBGroup, "release group First Light matches", "", ""}
	rowMBToo  = [5]string{"musicbrainz", rzMBEvidence, "artist page lists the same releases", "", ""}
	rowCredit = [5]string{"credit", "https://open.spotify.com/track/" + evTrack, "credited next to Wu-Tang Clan", evRelease, evTrack}
)

func TestSeveralEvidenceItemsAreReviewedAndPersistedTogether(t *testing.T) {
	env, rev := newEvidenceEnv(t)
	verify := rows(decision("newcomer", rzNewcomer, "verify", rev), rowMB, rowCredit, rowMBToo)

	rec := env.post("/resolve/newcomer/preview", verify)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	for _, want := range []string{evMBGroup, rzMBEvidence, "https://open.spotify.com/track/" + evTrack, "credited next to Wu-Tang Clan"} {
		require.Contains(t, body, want, "the preview shows exactly what will be recorded")
	}
	require.Less(t, strings.Index(body, evMBGroup), strings.Index(body, "https://open.spotify.com/track/"+evTrack), "rows keep the submitted order")
	require.Contains(t, body, "independent")
	require.Contains(t, body, "contextual")

	verify.Set("confirm", "1")
	require.Equal(t, http.StatusSeeOther, env.post("/resolve/newcomer/apply", verify).Code)

	got, err := yamlcatalog.NewStore().Load(context.Background(), env.path)
	require.NoError(t, err)
	p, _ := got.Artists[got.FindArtist("newcomer")].ProfileFor(rzNewcomer)
	require.Equal(t, catalog.ProfileVerified, p.Status)
	require.Len(t, p.Evidence, 3)
	require.Equal(t, catalog.EvidenceMusicBrainz, p.Evidence[0].Type)
	require.Equal(t, evMBGroup, p.Evidence[0].URL)
	require.Equal(t, catalog.EvidenceCredit, p.Evidence[1].Type)
	require.Equal(t, evTrack, p.Evidence[1].TrackID)
	require.Equal(t, evRelease, p.Evidence[1].ReleaseID)
	require.Equal(t, "credited next to Wu-Tang Clan", p.Evidence[1].Rationale)
	require.Equal(t, rzMBEvidence, p.Evidence[2].URL)
}

func TestCreditsAloneNeverSatisfyIndependentEvidence(t *testing.T) {
	env, rev := newEvidenceEnv(t)
	before := env.file()
	credits := rows(decision("newcomer", rzNewcomer, "verify", rev, "confirm", "1"), rowCredit,
		[5]string{"credit", "https://open.spotify.com/track/T2222222222222222222aa", "second credit", evRelease, "T2222222222222222222aa"})
	rec := env.post("/resolve/newcomer/apply", credits)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, before, env.file())

	withSource := rows(decision("newcomer", rzNewcomer, "verify", rev, "confirm", "1"), rowCredit, rowMB)
	require.Equal(t, http.StatusSeeOther, env.post("/resolve/newcomer/apply", withSource).Code)
}

func TestUnsafeEvidenceRowsAreRefusedWithoutWriting(t *testing.T) {
	env, rev := newEvidenceEnv(t)
	before := env.file()
	many := make([][5]string, 0, 21)
	for i := 0; i < 21; i++ {
		many = append(many, [5]string{"musicbrainz", "https://musicbrainz.org/artist/e70e310e-8e92-4da7-9aba-071bc6032a" + string(rune('a'+i)), "row", "", ""})
	}
	cases := map[string]url.Values{
		"duplicate":     rows(decision("newcomer", rzNewcomer, "verify", rev), rowMB, rowCredit, rowMB),
		"too many":      rows(decision("newcomer", rzNewcomer, "verify", rev), many...),
		"malformed url": rows(decision("newcomer", rzNewcomer, "verify", rev), [5]string{"musicbrainz", "http://musicbrainz.org/x", "plain http", "", ""}),
		"overlong text": rows(decision("newcomer", rzNewcomer, "verify", rev), [5]string{"musicbrainz", evMBGroup, strings.Repeat("x", 1001), "", ""}),
		"forged ids":    rows(decision("newcomer", rzNewcomer, "verify", rev), [5]string{"credit", "https://open.spotify.com/track/x", "forged", "not an id", "../../etc"}),
		"unknown type":  rows(decision("newcomer", rzNewcomer, "verify", rev), [5]string{"trust_me", evMBGroup, "forged", "", ""}),
	}
	for name, form := range cases {
		for _, path := range []string{"/resolve/newcomer/preview", "/resolve/newcomer/apply"} {
			f := url.Values{}
			for k, v := range form {
				f[k] = v
			}
			f.Set("confirm", "1")
			rec := env.post(path, f)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s %s: %s", name, path, rec.Body.String())
		}
	}
	require.Equal(t, before, env.file())
}

func TestLegacySingleRowFieldsStillWork(t *testing.T) {
	env, rev := newEvidenceEnv(t)
	legacy := decision("newcomer", rzNewcomer, "verify", rev, "confirm", "1",
		"evidence_type", "musicbrainz", "evidence_url", evMBGroup, "evidence_rationale", "one row")
	require.Equal(t, http.StatusSeeOther, env.post("/resolve/newcomer/apply", legacy).Code)
}

func TestSuggestionsAreAddedServerSideWithoutJavaScript(t *testing.T) {
	env, rev := newEvidenceEnv(t)
	page := env.get("/resolve/newcomer?q=" + rzNewcomer).Body.String()
	keys := addSuggestionPattern.FindAllStringSubmatch(page, -1)
	require.Len(t, keys, 2, "one MusicBrainz source and one credit are offered")
	require.NotContains(t, page, "data-fill-url", "selecting never overwrites a single group of fields")

	add := func(form url.Values, key string) string {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set("add_suggestion", key)
		f.Set("q", rzNewcomer)
		rec := env.post("/resolve/newcomer/edit", f)
		return rec.Body.String() + "\n" + http.StatusText(rec.Code)
	}

	base := decision("newcomer", rzNewcomer, "verify", rev, "note", "keep me")
	first := add(base, keys[0][1])
	require.Contains(t, first, "keep me", "typed values survive")
	require.Contains(t, first, `<details class="decide" open`)

	// Both suggestions accumulate; the first one is carried by the form.
	carried := rows(decision("newcomer", rzNewcomer, "verify", rev, "note", "keep me"), rowMB)
	second := add(carried, keys[1][1])
	require.Contains(t, second, evMBGroup)
	require.Contains(t, second, "https://open.spotify.com/track/"+evTrack)
	require.Contains(t, second, "Added", "an added suggestion is visibly marked")

	// Adding the same suggestion twice is refused, visibly.
	again := add(rows(decision("newcomer", rzNewcomer, "verify", rev), rowMB), keys[0][1])
	require.Contains(t, again, "already added")
	require.Equal(t, 1, strings.Count(again, `value="`+evMBGroup+`"`))

	// A forged key adds nothing.
	forged := add(decision("newcomer", rzNewcomer, "verify", rev), "deadbeefdeadbeef")
	require.Contains(t, forged, "no longer offered")
	require.NotContains(t, forged, "deadbeef\"")

	// Rows can be removed and blank rows added without JavaScript.
	f := rows(decision("newcomer", rzNewcomer, "verify", rev), rowMB, rowCredit)
	f.Set("remove_row", "0")
	rec := env.post("/resolve/newcomer/edit", f)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `value="`+evMBGroup+`"`)
	require.Contains(t, rec.Body.String(), "https://open.spotify.com/track/"+evTrack)
}

func TestEditRequiresCSRF(t *testing.T) {
	env, rev := newEvidenceEnv(t)
	f := decision("newcomer", rzNewcomer, "verify", rev)
	f.Set("add_row", "1")
	f.Set("csrf_token", "wrong")
	require.Equal(t, http.StatusForbidden, env.post("/resolve/newcomer/edit", f).Code)
}

func TestSelectionsSurviveThePreviewAndErrorSteps(t *testing.T) {
	env, rev := newEvidenceEnv(t)

	// A refused preview offers a way back that carries every row.
	bad := rows(decision("newcomer", rzNewcomer, "verify", rev, "note", "n1"), rowMB, [5]string{"credit", "http://bad", "x", "", evTrack})
	rec := env.post("/resolve/newcomer/preview", bad)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), `/resolve/newcomer/edit`)
	require.Contains(t, rec.Body.String(), evMBGroup)

	// The confirmation page echoes all rows so apply records what was shown.
	ok := rows(decision("newcomer", rzNewcomer, "verify", rev), rowMB, rowCredit)
	rec = env.post("/resolve/newcomer/preview", ok)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 2, strings.Count(rec.Body.String(), `name="evidence_type"`))
	require.Contains(t, rec.Body.String(), `/resolve/newcomer/edit`)
}

func TestTypedSuggestionsAreExposedByTheAPIWithoutSecrets(t *testing.T) {
	env, _ := newEvidenceEnv(t)
	rec := env.get("/api/v1/resolve/newcomer?q=" + rzNewcomer)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"suggestions"`)
	require.Contains(t, body, `"credit_suggestions"`)
	require.Contains(t, body, `"independent":true`)
	require.Contains(t, body, `"independent":false`)
	require.Contains(t, body, evMBGroup)
	require.NotContains(t, body, "csrf")
}
