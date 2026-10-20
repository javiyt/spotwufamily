package artists_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

const (
	wuPrimary    = "34EP7KEpOjXcM2TCat1ISk"
	wuSecondary  = "0H8YCcvC3MPLKnbDRasGiG"
	wuWrong      = "1aaaaaaaaaaaaaaaaaaaaa"
	streetlifeID = "0FCYBhXjpfQIQWNKiVEDg5"
	kgbID        = "2gKwx0Lm0JoOrN2mY9Z0PG"
	rawkusID     = "3bbbbbbbbbbbbbbbbbbbbb"
)

type profileFixture struct {
	artist catalog.ArtistCandidate
	albums []catalog.Release
	err    error
}

type profileSource struct {
	mu       sync.Mutex
	profiles map[string]profileFixture
	calls    map[string]int
}

func (s *profileSource) record(kind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[kind+":"+id]++
}

func (s *profileSource) GetArtist(_ context.Context, id string) (catalog.ArtistCandidate, error) {
	s.record("artist", id)
	f, ok := s.profiles[id]
	if !ok {
		return catalog.ArtistCandidate{}, artists.ErrSourceNotFound
	}
	if f.err != nil {
		return catalog.ArtistCandidate{}, f.err
	}
	return f.artist, nil
}

func (s *profileSource) GetArtistAlbums(_ context.Context, id string, _ []string) ([]catalog.Release, error) {
	s.record("albums", id)
	f, ok := s.profiles[id]
	if !ok {
		return nil, artists.ErrSourceNotFound
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.albums, nil
}

type mbSource struct {
	mu     sync.Mutex
	groups map[string][]artists.MusicBrainzReleaseGroup
	err    error
	calls  map[string]int
}

func (m *mbSource) SearchArtistAlbumReleaseGroups(_ context.Context, a catalog.Artist) ([]artists.MusicBrainzReleaseGroup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls == nil {
		m.calls = map[string]int{}
	}
	m.calls[a.Slug]++
	if m.err != nil {
		return nil, m.err
	}
	return m.groups[a.Slug], nil
}

func album(id, name, date string) catalog.Release {
	return catalog.Release{SpotifyID: id, Name: name, ReleaseDate: date, URL: "https://open.spotify.com/album/" + id}
}

func mbGroup(id, title, date string) artists.MusicBrainzReleaseGroup {
	return artists.MusicBrainzReleaseGroup{ID: id, Title: title, FirstReleaseDate: date, URL: "https://musicbrainz.org/release-group/" + id}
}

var auditNow = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }

func auditCatalog() catalog.EditorialCatalog {
	return catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "wu-tang-clan", Name: "Wu-Tang Clan", SpotifyID: wuPrimary, SpotifyIDs: []string{wuSecondary, wuWrong},
			Genres: []string{"east coast hip hop"}, Category: catalog.CategoryCore, Roles: []catalog.Category{catalog.CategoryCore}, Enabled: true, EditorialOrder: 1},
		{Slug: "streetlife", Name: "Streetlife", SpotifyID: streetlifeID, Genres: []string{"east coast hip hop"},
			Category: catalog.CategoryAffiliateArtist, Roles: []catalog.Category{catalog.CategoryAffiliateArtist}, Enabled: true, EditorialOrder: 2},
		{Slug: "k-g-b", Name: "K.G.B.", SpotifyID: kgbID, Genres: []string{"east coast hip hop"},
			Category: catalog.CategoryAffiliateGroup, Roles: []catalog.Category{catalog.CategoryAffiliateGroup}, Enabled: false, EditorialOrder: 3},
	}}
}

func auditSpotify() *profileSource {
	return &profileSource{profiles: map[string]profileFixture{
		wuPrimary: {
			artist: catalog.ArtistCandidate{Name: "Wu-Tang Clan", SpotifyID: wuPrimary, Genres: []string{"east coast hip hop"}},
			albums: []catalog.Release{album("a1", "Enter the Wu-Tang (36 Chambers)", "1993-11-09"), album("a2", "Wu-Tang Forever", "1997-06-03")},
		},
		wuSecondary: {
			artist: catalog.ArtistCandidate{Name: "Wu-Tang Clan", SpotifyID: wuSecondary, Genres: []string{"hip hop"}},
			albums: []catalog.Release{album("a3", "The W", "2000-11-21")},
		},
		wuWrong: {
			artist: catalog.ArtistCandidate{Name: "Wu Tang Clan", SpotifyID: wuWrong, Genres: []string{"lullaby"}},
			albums: []catalog.Release{album("x1", "Rain Sounds for Sleep", "2019-01-01"), album("x2", "Ocean Waves", "2019-02-01")},
		},
		streetlifeID: {
			artist: catalog.ArtistCandidate{Name: "Streetlife", SpotifyID: streetlifeID, Genres: []string{"acid jazz", "nu jazz"}},
			albums: []catalog.Release{album("s1", "Moonlit Lounge", "2005-05-05")},
		},
		kgbID: {
			artist: catalog.ArtistCandidate{Name: "K.G.B.", SpotifyID: kgbID, Genres: []string{"chicago house"}},
			albums: []catalog.Release{album("k1", "Warehouse Nights", "2011-03-03")},
		},
	}}
}

func auditMB() *mbSource {
	return &mbSource{groups: map[string][]artists.MusicBrainzReleaseGroup{
		"wu-tang-clan": {
			mbGroup("m1", "Enter the Wu-Tang (36 Chambers)", "1993-11-09"),
			mbGroup("m2", "Wu-Tang Forever", "1997-06-03"),
			mbGroup("m3", "The W", "2000-11-21"),
		},
		"streetlife": {mbGroup("m4", "Street Life Gangsta", "1999-01-01")},
		"k-g-b":      {mbGroup("m5", "Klik Ga Bow", "1998-01-01")},
	}}
}

func runAudit(t *testing.T, c catalog.EditorialCatalog, spotify artists.SpotifyProfileSource, mb artists.MusicBrainzReleaseGroupSearcher, opts artists.AuditProfilesOptions) (artists.ProfileAuditReport, *memoryStore) {
	t.Helper()
	store := &memoryStore{catalog: c}
	opts.Now = auditNow
	report, err := artists.NewAuditProfiles(store, spotify, mb).Run(context.Background(), opts)
	require.NoError(t, err)
	return report, store
}

func find(t *testing.T, report artists.ProfileAuditReport, id string) artists.ProfileAuditResult {
	t.Helper()
	for _, r := range report.Profiles {
		if r.SpotifyID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return artists.ProfileAuditResult{}
}

func TestAuditProfilesReportsEveryConfiguredIDIndividually(t *testing.T) {
	report, store := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{})

	require.Len(t, report.Profiles, 5)
	primary := find(t, report, wuPrimary)
	require.Equal(t, artists.ProfileRolePrimary, primary.Role)
	require.Equal(t, "spotify:artist:"+wuPrimary, primary.URI)
	require.Equal(t, "Wu-Tang Clan", primary.ArtistName)
	require.Equal(t, artists.ClassVerified, primary.Classification)
	require.Equal(t, auditNow().Format("2006-01-02"), primary.CheckedOn)

	secondary := find(t, report, wuSecondary)
	require.Equal(t, artists.ProfileRoleSecondary, secondary.Role)
	require.Equal(t, artists.ClassVerified, secondary.Classification)
	require.Equal(t, []string{"The W"}, releaseNames(secondary.Releases))

	// A wrong secondary is not hidden by the authentic releases of the primary.
	wrong := find(t, report, wuWrong)
	require.Equal(t, artists.ClassSuspicious, wrong.Classification)
	require.Equal(t, artists.SeverityError, wrong.Severity)
	require.NotEmpty(t, wrong.Contradictions)

	// Disabled artists are audited too.
	kgb := find(t, report, kgbID)
	require.False(t, kgb.ArtistEnabled)

	require.Zero(t, store.saveCount, "audit must never write the catalog")
}

func releaseNames(rs []artists.ProfileRelease) []string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	return names
}

func TestAuditProfilesFlagsStreetlifeAndKGBWithoutRelyingOnGenre(t *testing.T) {
	report, _ := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{})

	for _, id := range []string{streetlifeID, kgbID} {
		r := find(t, report, id)
		require.Equal(t, artists.ClassSuspicious, r.Classification, id)
		require.True(t, hasFinding(r, artists.FindingGenreMismatch), "genre mismatch is reported")
		genre := findingByCode(r, artists.FindingGenreMismatch)
		require.Equal(t, artists.SeverityWarning, genre.Severity)
		require.True(t, hasEvidence(r, catalog.EvidenceReleaseMismatch, catalog.EvidenceNegative), "negative evidence comes from releases")
	}
}

func TestAuditProfilesGenreMismatchAloneIsOnlyAWarning(t *testing.T) {
	c := auditCatalog()
	spotify := &profileSource{profiles: map[string]profileFixture{
		streetlifeID: {
			artist: catalog.ArtistCandidate{Name: "Streetlife", SpotifyID: streetlifeID, Genres: []string{"acid jazz"}},
			albums: []catalog.Release{album("s1", "Street Life Gangsta", "1999-01-01")},
		},
	}}
	c.Artists = c.Artists[1:2]
	report, _ := runAudit(t, c, spotify, auditMB(), artists.AuditProfilesOptions{})

	r := find(t, report, streetlifeID)
	require.Equal(t, artists.ClassVerified, r.Classification)
	require.True(t, hasFinding(r, artists.FindingGenreMismatch))
	require.Equal(t, artists.SeverityWarning, findingByCode(r, artists.FindingGenreMismatch).Severity)
}

func TestAuditProfilesMusicBrainzOutageIsMissingEvidenceNotNegative(t *testing.T) {
	mb := &mbSource{err: errors.New("MusicBrainz request failed: status 503")}
	report, _ := runAudit(t, auditCatalog(), auditSpotify(), mb, artists.AuditProfilesOptions{})

	for _, r := range report.Profiles {
		require.Equal(t, artists.ClassInsufficientEvidence, r.Classification, r.SpotifyID)
		require.True(t, hasEvidence(r, catalog.EvidenceMusicBrainzUnavailable, catalog.EvidenceWarning), r.SpotifyID)
		require.False(t, hasEvidence(r, catalog.EvidenceReleaseMismatch, catalog.EvidenceNegative), r.SpotifyID)
		require.Equal(t, artists.SourceUnavailable, r.MusicBrainz.Status)
	}
}

func TestAuditProfilesSpotify404IsUnavailableNotWrong(t *testing.T) {
	spotify := auditSpotify()
	spotify.profiles[wuWrong] = profileFixture{err: artists.ErrSourceNotFound}
	report, _ := runAudit(t, auditCatalog(), spotify, auditMB(), artists.AuditProfilesOptions{})

	r := find(t, report, wuWrong)
	require.Equal(t, artists.ClassUnavailable, r.Classification)
	require.Equal(t, artists.SourceNotFound, r.SpotifyStatus)
	require.Contains(t, r.Explanation, "removed")
}

func TestAuditProfilesSpotify429StopsFurtherSpotifyCallsAndMarksUnavailable(t *testing.T) {
	spotify := auditSpotify()
	for id, f := range spotify.profiles {
		f.err = fmt.Errorf("get artist: %w", artists.ErrSourceRateLimited)
		spotify.profiles[id] = f
	}
	report, _ := runAudit(t, auditCatalog(), spotify, auditMB(), artists.AuditProfilesOptions{Concurrency: 1})

	require.Len(t, report.Profiles, 5)
	for _, r := range report.Profiles {
		require.Equal(t, artists.ClassUnavailable, r.Classification, r.SpotifyID)
		require.Equal(t, artists.SourceRateLimited, r.SpotifyStatus, r.SpotifyID)
	}
	total := 0
	for _, n := range spotify.calls {
		total += n
	}
	require.Equal(t, 1, total, "one rate-limited call, the rest are skipped")
}

func TestAuditProfilesDetectsDuplicateIDsAcrossArtists(t *testing.T) {
	c := auditCatalog()
	c.Artists[1].SpotifyIDs = []string{wuSecondary}
	report, _ := runAudit(t, c, auditSpotify(), auditMB(), artists.AuditProfilesOptions{})

	require.Len(t, report.Profiles, 6)
	var dupes []artists.ProfileAuditResult
	for _, r := range report.Profiles {
		if r.SpotifyID == wuSecondary {
			dupes = append(dupes, r)
		}
	}
	require.Len(t, dupes, 2)
	for _, r := range dupes {
		require.Equal(t, artists.ClassAmbiguous, r.Classification)
		require.True(t, hasFinding(r, artists.FindingDuplicateID))
	}
	// Each instance is evaluated against its own artist, albums fetched once.
	require.Equal(t, 1, countCalls(auditSpotifyCalls(t, c), "albums:"+wuSecondary))
}

func auditSpotifyCalls(t *testing.T, c catalog.EditorialCatalog) map[string]int {
	t.Helper()
	spotify := auditSpotify()
	runAudit(t, c, spotify, auditMB(), artists.AuditProfilesOptions{})
	return spotify.calls
}

func countCalls(calls map[string]int, key string) int { return calls[key] }

func TestAuditProfilesFetchesMusicBrainzOncePerArtist(t *testing.T) {
	mb := auditMB()
	runAudit(t, auditCatalog(), auditSpotify(), mb, artists.AuditProfilesOptions{Concurrency: 4})

	require.Equal(t, map[string]int{"wu-tang-clan": 1, "streetlife": 1, "k-g-b": 1}, mb.calls)
}

func TestAuditProfilesOutputIsDeterministic(t *testing.T) {
	first, _ := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{Concurrency: 1})
	for i := 0; i < 5; i++ {
		again, _ := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{Concurrency: 8})
		a, err := artists.FormatProfileAuditJSON(first)
		require.NoError(t, err)
		b, err := artists.FormatProfileAuditJSON(again)
		require.NoError(t, err)
		require.Equal(t, string(a), string(b))
		require.Equal(t, string(artists.FormatProfileAuditMarkdown(first)), string(artists.FormatProfileAuditMarkdown(again)))
	}
	var decoded map[string]any
	data, _ := artists.FormatProfileAuditJSON(first)
	require.NoError(t, json.Unmarshal(data, &decoded))
}

func TestAuditProfilesArtistFilter(t *testing.T) {
	report, _ := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{ArtistSlug: "wu-tang-clan"})
	require.Len(t, report.Profiles, 3)

	_, err := artists.NewAuditProfiles(&memoryStore{catalog: auditCatalog()}, auditSpotify(), auditMB()).
		Run(context.Background(), artists.AuditProfilesOptions{ArtistSlug: "nobody"})
	require.Error(t, err)
}

func TestAuditProfilesReleaseAndCreditAnomalies(t *testing.T) {
	c := auditCatalog()
	c.Artists = c.Artists[:1]
	c.Artists[0].SpotifyIDs = nil
	spotify := &profileSource{profiles: map[string]profileFixture{
		wuPrimary: {
			artist: catalog.ArtistCandidate{Name: "Wu-Tang Clan", SpotifyID: wuPrimary, Genres: []string{"east coast hip hop"}},
			albums: []catalog.Release{
				album("a1", "Enter the Wu-Tang (36 Chambers)", "1993-11-09"),
				album("a9", "", "not-a-date"),
				{SpotifyID: "a8", Name: "From The Future", ReleaseDate: "2031-01-01", Artists: []catalog.ArtistCandidate{{SpotifyID: "9zzzzzzzzzzzzzzzzzzzzz", Name: "Someone Else"}}},
			},
		},
	}}
	report, _ := runAudit(t, c, spotify, auditMB(), artists.AuditProfilesOptions{})

	r := find(t, report, wuPrimary)
	require.True(t, hasFinding(r, artists.FindingReleaseAnomaly))
	require.True(t, hasFinding(r, artists.FindingConflictingCredits))
	require.Equal(t, artists.ClassVerified, r.Classification, "an authentic album still verifies the profile")
}

func TestAuditProfilesRenamedProfileWithoutReleaseSupportIsSuspicious(t *testing.T) {
	c := auditCatalog()
	c.Artists = c.Artists[1:2]
	spotify := &profileSource{profiles: map[string]profileFixture{
		streetlifeID: {
			artist: catalog.ArtistCandidate{Name: "DJ Lounge Beats", SpotifyID: streetlifeID, Genres: []string{"east coast hip hop"}},
			albums: []catalog.Release{album("s1", "Moonlit Lounge", "2005-05-05")},
		},
	}}
	report, _ := runAudit(t, c, spotify, auditMB(), artists.AuditProfilesOptions{})
	r := find(t, report, streetlifeID)
	require.Equal(t, artists.ClassSuspicious, r.Classification)
	require.True(t, hasFinding(r, artists.FindingProfileRenamed))
}

func TestAuditProfilesHomonymNeedsMoreThanASharedName(t *testing.T) {
	c := auditCatalog()
	c.Artists = c.Artists[1:2]
	spotify := &profileSource{profiles: map[string]profileFixture{
		streetlifeID: {
			artist: catalog.ArtistCandidate{Name: "Streetlife", SpotifyID: streetlifeID, Genres: []string{"east coast hip hop"}},
			albums: []catalog.Release{album("s1", "Moonlit Lounge", "2005-05-05")},
		},
	}}
	report, _ := runAudit(t, c, spotify, auditMB(), artists.AuditProfilesOptions{})
	r := find(t, report, streetlifeID)
	require.Equal(t, artists.ClassSuspicious, r.Classification, "an identical name is not identity evidence")
	require.True(t, hasFinding(r, artists.FindingLikelyHomonym))
}

func TestAuditProfilesManualVerificationIsHonoured(t *testing.T) {
	c := auditCatalog()
	c.Artists = c.Artists[1:2]
	c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{{
		ID: streetlifeID, Status: catalog.ProfileVerified, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z",
		Evidence: []catalog.Evidence{{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/Streetlife", Rationale: "Listed as Wu affiliate"}},
	}}
	spotify := &profileSource{profiles: map[string]profileFixture{
		streetlifeID: {
			artist: catalog.ArtistCandidate{Name: "Streetlife", SpotifyID: streetlifeID},
			albums: nil,
		},
	}}
	mb := &mbSource{err: errors.New("503")}
	report, _ := runAudit(t, c, spotify, mb, artists.AuditProfilesOptions{})
	r := find(t, report, streetlifeID)
	require.Equal(t, artists.ClassVerified, r.Classification)
	require.Equal(t, catalog.ProfileVerified, r.ReviewStatus)
}

func TestAuditProfilesOfflineNeverCallsNetworkSources(t *testing.T) {
	spotify := auditSpotify()
	for id, f := range spotify.profiles {
		f.artist = catalog.ArtistCandidate{}
		f.err = artists.ErrCacheMiss
		spotify.profiles[id] = f
	}
	mb := auditMB()
	report, _ := runAudit(t, auditCatalog(), spotify, mb, artists.AuditProfilesOptions{Offline: true})

	require.True(t, report.Offline)
	require.Empty(t, mb.calls)
	for _, r := range report.Profiles {
		require.Equal(t, artists.ClassInsufficientEvidence, r.Classification)
		require.Equal(t, artists.SourceNotCached, r.SpotifyStatus)
	}
}

func TestProfileAuditMarkdownDistinguishesMissingFromNegativeEvidence(t *testing.T) {
	report, _ := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{})
	md := string(artists.FormatProfileAuditMarkdown(report))
	require.Contains(t, md, "# Spotify Profile Audit")
	require.Contains(t, md, "Negative evidence")
	require.Contains(t, md, "Missing evidence")
	require.Contains(t, md, "spotify:artist:"+wuWrong)
}

func TestApplyProfileAuditMarksSuspiciousNeedsReviewAndBlocksAmbiguous(t *testing.T) {
	c := auditCatalog()
	c.Artists[1].SpotifyIDs = []string{wuSecondary}
	report, _ := runAudit(t, c, auditSpotify(), auditMB(), artists.AuditProfilesOptions{})
	store := &memoryStore{catalog: auditCatalog()}
	apply := artists.NewApplyProfileAudit(store)

	_, err := apply.Run(context.Background(), "ignored", artists.ApplyProfileAuditInput{Report: report, Slug: "wu-tang-clan", SpotifyID: wuSecondary})
	require.ErrorIs(t, err, artists.ErrApplyBlocked)
	require.Zero(t, store.saveCount)

	res, err := apply.Run(context.Background(), "ignored", artists.ApplyProfileAuditInput{Report: report, Slug: "wu-tang-clan", SpotifyID: wuWrong})
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileNeedsReview, res.To)
	require.Equal(t, 1, store.saveCount)
	require.Equal(t, catalog.ProfileNeedsReview, store.catalog.Artists[0].StatusOf(wuWrong))

	_, err = apply.Run(context.Background(), "ignored", artists.ApplyProfileAuditInput{Report: report, Slug: "wu-tang-clan", SpotifyID: wuPrimary})
	require.ErrorIs(t, err, artists.ErrApplyBlocked, "only suspicious profiles can be applied")
}

func hasFinding(r artists.ProfileAuditResult, code artists.FindingCode) bool {
	return findingByCode(r, code).Code != ""
}

func findingByCode(r artists.ProfileAuditResult, code artists.FindingCode) artists.ProfileFinding {
	for _, f := range r.Findings {
		if f.Code == code {
			return f
		}
	}
	return artists.ProfileFinding{}
}

func hasEvidence(r artists.ProfileAuditResult, kind catalog.EvidenceKind, polarity catalog.EvidencePolarity) bool {
	for _, e := range r.Evidence {
		if e.Kind == kind && e.Polarity == polarity {
			return true
		}
	}
	return false
}

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func TestProfileAuditGoldenOutputs(t *testing.T) {
	report, _ := runAudit(t, auditCatalog(), auditSpotify(), auditMB(), artists.AuditProfilesOptions{})
	asJSON, err := artists.FormatProfileAuditJSON(report)
	require.NoError(t, err)

	for name, got := range map[string][]byte{
		"testdata/profile_audit.golden.json": asJSON,
		"testdata/profile_audit.golden.md":   artists.FormatProfileAuditMarkdown(report),
	} {
		if *updateGolden {
			require.NoError(t, os.MkdirAll("testdata", 0o755))
			require.NoError(t, os.WriteFile(name, got, 0o644))
		}
		want, err := os.ReadFile(name)
		require.NoError(t, err, "run with -update to create golden files")
		require.Equal(t, string(want), string(got), name)
	}
}
