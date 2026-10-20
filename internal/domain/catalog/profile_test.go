package catalog_test

import (
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

const (
	idA = "34EP7KEpOjXcM2TCat1ISk"
	idB = "0H8YCcvC3MPLKnbDRasGiG"
)

var reviewTime = time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)

func independent() catalog.Evidence {
	return catalog.Evidence{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/1", Rationale: "MusicBrainz lists this Spotify URL"}
}

func profileArtist(profiles ...catalog.SpotifyProfile) catalog.EditorialCatalog {
	return catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{{
		Slug: "a", Name: "A", SpotifyID: idA, Category: catalog.CategoryCore, SpotifyProfiles: profiles,
	}}}
}

func TestLegacyArtistsAreUnreviewedAndSyncOnlyWhenMigrated(t *testing.T) {
	artist := profileArtist().Artists[0]

	require.Equal(t, catalog.ProfileUnreviewed, artist.StatusOf(idA))
	require.Empty(t, artist.SyncableSpotifyIDs(""))
	require.Empty(t, artist.SyncableSpotifyIDs(catalog.SyncVerifiedOnly))
	require.Equal(t, []string{idA}, artist.SyncableSpotifyIDs(catalog.SyncAllowUnreviewed))
}

func TestOnlyVerifiedAndLegacyUnreviewedProfilesSync(t *testing.T) {
	for status, want := range map[catalog.ProfileStatus]bool{
		catalog.ProfileVerified: true, catalog.ProfileCandidate: false,
		catalog.ProfileNeedsReview: false, catalog.ProfileRejected: false,
	} {
		require.Equal(t, want, catalog.ProfileSyncAllowed(status, catalog.SyncAllowUnreviewed), status)
	}
	require.True(t, catalog.ProfileSyncAllowed(catalog.ProfileVerified, catalog.SyncVerifiedOnly))
	require.False(t, catalog.ProfileSyncAllowed(catalog.ProfileUnreviewed, catalog.SyncVerifiedOnly))
}

func TestReviewProfileTransitions(t *testing.T) {
	unreviewed := catalog.SpotifyProfile{ID: idA, Status: catalog.ProfileUnreviewed}
	manual := catalog.ReviewRequest{Manual: true, Reviewer: "javi", ReviewedAt: reviewTime, Evidence: []catalog.Evidence{independent()}}

	t.Run("verify records provenance", func(t *testing.T) {
		req := manual
		req.Status = catalog.ProfileVerified
		got, err := catalog.ReviewProfile(unreviewed, req)
		require.NoError(t, err)
		require.Equal(t, catalog.ProfileVerified, got.Status)
		require.Equal(t, "2026-01-02T15:04:05Z", got.ReviewedAt)
		require.Equal(t, "javi", got.Reviewer)
		require.Len(t, got.Evidence, 1)
	})
	t.Run("automation cannot verify or reject", func(t *testing.T) {
		for _, to := range []catalog.ProfileStatus{catalog.ProfileVerified, catalog.ProfileRejected} {
			req := manual
			req.Status, req.Manual = to, false
			_, err := catalog.ReviewProfile(unreviewed, req)
			require.ErrorIs(t, err, catalog.ErrInvalidTransition)
		}
	})
	t.Run("name match and credit are not independent evidence", func(t *testing.T) {
		req := manual
		req.Status = catalog.ProfileVerified
		req.Evidence = []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "same name"}, {Type: catalog.EvidenceCredit, ReleaseID: idB, Rationale: "credited"}}
		_, err := catalog.ReviewProfile(unreviewed, req)
		require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	})
	t.Run("verified cannot go back to candidate", func(t *testing.T) {
		req := manual
		req.Status = catalog.ProfileCandidate
		_, err := catalog.ReviewProfile(catalog.SpotifyProfile{ID: idA, Status: catalog.ProfileVerified}, req)
		require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	})
	t.Run("unknown status", func(t *testing.T) {
		_, err := catalog.ReviewProfile(unreviewed, catalog.ReviewRequest{Status: "bogus", Manual: true})
		require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	})
}

func TestRejectionBlocksReapprovalWithoutNewIndependentEvidence(t *testing.T) {
	rejected := catalog.SpotifyProfile{
		ID: idA, Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z",
		Evidence: []catalog.Evidence{independent()},
	}
	base := catalog.ReviewRequest{Status: catalog.ProfileVerified, Reviewer: "other", ReviewedAt: reviewTime}

	automated := base
	automated.Evidence = []catalog.Evidence{{Type: catalog.EvidenceWikidata, URL: "https://www.wikidata.org/wiki/Q1", Rationale: "new"}}
	_, err := catalog.ReviewProfile(rejected, automated)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)

	manualSame := base
	manualSame.Manual = true
	manualSame.Evidence = []catalog.Evidence{independent()} // already considered
	_, err = catalog.ReviewProfile(rejected, manualSame)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)

	manualNameOnly := base
	manualNameOnly.Manual = true
	manualNameOnly.Evidence = []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "same name"}}
	_, err = catalog.ReviewProfile(rejected, manualNameOnly)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)

	manualNew := base
	manualNew.Manual = true
	manualNew.Evidence = automated.Evidence
	got, err := catalog.ReviewProfile(rejected, manualNew)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileVerified, got.Status)
	require.Len(t, got.Evidence, 2, "history of the rejection evidence is kept")
}

func TestCatalogRejectedLookup(t *testing.T) {
	c := profileArtist(catalog.SpotifyProfile{
		ID: idB, Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z",
		Evidence: []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "different artist with same name"}},
	})
	require.True(t, c.IsRejectedSpotifyID(idB))
	require.False(t, c.IsRejectedSpotifyID(idA))
	require.Empty(t, catalog.ValidateEditorialCatalog(c))
}

func TestValidateProfiles(t *testing.T) {
	verified := catalog.SpotifyProfile{ID: idA, Status: catalog.ProfileVerified, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z", Evidence: []catalog.Evidence{independent()}}

	tests := map[string]struct {
		mutate func(*catalog.EditorialCatalog)
		field  string
	}{
		"valid verified": {func(c *catalog.EditorialCatalog) { c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{verified} }, ""},
		"unknown status": {func(c *catalog.EditorialCatalog) {
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{{ID: idA, Status: "x"}}
		}, "status"},
		"bad id": {func(c *catalog.EditorialCatalog) {
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{{ID: "short", Status: catalog.ProfileCandidate}}
		}, ".id"},
		"duplicate profile": {func(c *catalog.EditorialCatalog) {
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{{ID: idA, Status: catalog.ProfileCandidate}, {ID: idA, Status: catalog.ProfileCandidate}}
		}, "duplicate Spotify profile"},
		"verified without evidence": {func(c *catalog.EditorialCatalog) {
			v := verified
			v.Evidence = nil
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{v}
		}, "evidence"},
		"verified unlinked": {func(c *catalog.EditorialCatalog) {
			v := verified
			v.ID = idB
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{v}
		}, "must be linked"},
		"verified no reviewer": {func(c *catalog.EditorialCatalog) {
			v := verified
			v.Reviewer = ""
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{v}
		}, "reviewer"},
		"rejected but linked": {func(c *catalog.EditorialCatalog) {
			v := verified
			v.Status = catalog.ProfileRejected
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{v}
		}, "must not be linked"},
		"unreviewed with reviewer": {func(c *catalog.EditorialCatalog) {
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{{ID: idA, Status: catalog.ProfileUnreviewed, Reviewer: "x"}}
		}, "must not carry"},
		"non-utc timestamp": {func(c *catalog.EditorialCatalog) {
			v := verified
			v.ReviewedAt = "2026-01-02T15:04:05+01:00"
			c.Artists[0].SpotifyProfiles = []catalog.SpotifyProfile{v}
		}, "UTC"},
		"unknown policy": {func(c *catalog.EditorialCatalog) { c.ProfileSyncPolicy = "yolo" }, "profile_sync_policy"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := profileArtist()
			tt.mutate(&c)
			issues := catalog.ValidateEditorialCatalog(c)
			if tt.field == "" {
				require.Empty(t, issues)
				return
			}
			require.NotEmpty(t, issues)
			joined := ""
			for _, issue := range issues {
				joined += issue.Error() + "\n"
			}
			require.Contains(t, joined, tt.field)
		})
	}
}

func TestProfileIDsAreGloballyUnique(t *testing.T) {
	c := catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "a", Name: "A", SpotifyID: idA, Category: catalog.CategoryCore},
		{Slug: "b", Name: "B", Category: catalog.CategoryCore, SpotifyProfiles: []catalog.SpotifyProfile{{ID: idA, Status: catalog.ProfileCandidate}}},
	}}
	issues := catalog.ValidateEditorialCatalog(c)
	require.Len(t, issues, 1)
	require.Contains(t, issues[0].Error(), "duplicate Spotify ID")
}

func TestEvidenceValidation(t *testing.T) {
	tests := map[string]struct {
		evidence catalog.Evidence
		ok       bool
	}{
		"musicbrainz":       {independent(), true},
		"http":              {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "http://example.com", Rationale: "x"}, false},
		"credentials":       {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "https://u:p@example.com", Rationale: "x"}, false},
		"ip literal":        {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "https://127.0.0.1/a", Rationale: "x"}, false},
		"localhost":         {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "https://localhost/a", Rationale: "x"}, false},
		"javascript":        {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "javascript:alert(1)", Rationale: "x"}, false},
		"custom port":       {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "https://example.com:8443/a", Rationale: "x"}, false},
		"wrong host":        {catalog.Evidence{Type: catalog.EvidenceMusicBrainz, URL: "https://evil.example/a", Rationale: "x"}, false},
		"lookalike host":    {catalog.Evidence{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org.evil.example/a", Rationale: "x"}, false},
		"wikipedia":         {catalog.Evidence{Type: catalog.EvidenceWikipedia, URL: "https://en.wikipedia.org/wiki/RZA", Rationale: "x"}, true},
		"missing rationale": {catalog.Evidence{Type: catalog.EvidenceOfficialSite, URL: "https://example.com", Rationale: " "}, false},
		"bad release id":    {catalog.Evidence{Type: catalog.EvidenceCredit, ReleaseID: "nope", Rationale: "x"}, false},
		"credit by release": {catalog.Evidence{Type: catalog.EvidenceCredit, ReleaseID: idB, Rationale: "credited"}, true},
		"credit nothing":    {catalog.Evidence{Type: catalog.EvidenceCredit, Rationale: "credited"}, false},
		"unknown type":      {catalog.Evidence{Type: "gossip", Rationale: "x"}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tt.ok, len(catalog.ValidateEvidence(tt.evidence)) == 0)
		})
	}
}

func TestUnlinkSpotifyIDsKeepsRejections(t *testing.T) {
	a := catalog.Artist{SpotifyID: idA, SpotifyIDs: []string{idB}, SpotifyProfiles: []catalog.SpotifyProfile{
		{ID: idA, Status: catalog.ProfileCandidate}, {ID: "AAAAAAAAAAAAAAAAAAAAAA", Status: catalog.ProfileRejected},
	}}
	a.UnlinkSpotifyIDs()
	require.Empty(t, a.AllSpotifyIDs())
	require.Len(t, a.SpotifyProfiles, 1)
	require.Equal(t, catalog.ProfileRejected, a.SpotifyProfiles[0].Status)
}
