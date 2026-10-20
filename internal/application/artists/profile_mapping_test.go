package artists_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	idA = "7CrVv4zLga71gdlMOMfjXL"
	idB = "0FCYBhXjpfQIQWNKiVEDg5"
	idC = "2gKwx0Lm0JoOrN2mY9Z0PG"
)

var mappingAt = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func mappingCatalog() catalog.EditorialCatalog {
	return catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "street", Name: "Streetlife", Category: catalog.CategoryAffiliateArtist, Enabled: true, SpotifyID: idB,
			SpotifyProfiles: []catalog.SpotifyProfile{{ID: idB, Status: catalog.ProfileCandidate}}},
		{Slug: "other", Name: "Other", Category: catalog.CategoryAffiliateArtist, SpotifyID: idC},
	}}
}

func mapReq(action artists.MappingAction, id string) artists.MappingRequest {
	return artists.MappingRequest{Slug: "street", SpotifyID: id, Action: action, Reviewer: "javi", At: mappingAt}
}

var mbEvidence = catalog.Evidence{
	Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/e70e310e-8e92-4da7-9aba-071bc6032a34", Rationale: "credited with Method Man",
}

func TestApplyMappingAcceptWithoutEvidenceIsOnlyACandidate(t *testing.T) {
	c := mappingCatalog()
	res, err := artists.ApplyMapping(&c, mapReq(artists.MapAcceptPrimary, idA))
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileCandidate, res.To)
	require.Equal(t, idB, res.Demoted)
	a := c.Artists[0]
	require.Equal(t, idA, a.SpotifyID)
	require.Equal(t, []string{idB}, a.SpotifyIDs)
	// Not verified, so sync policy keeps ignoring it.
	require.NotContains(t, a.SyncableSpotifyIDs(catalog.SyncVerifiedOnly), idA)
	require.Empty(t, catalog.ValidateEditorialCatalog(c))
}

func TestApplyMappingVerifyNeedsIndependentEvidence(t *testing.T) {
	c := mappingCatalog()
	req := mapReq(artists.MapAcceptPrimary, idA)
	req.Verify = true
	req.Evidence = []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "same name"}}
	_, err := artists.ApplyMapping(&c, req)
	require.ErrorIs(t, err, artists.ErrInvalidMapping)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)

	// An override reason alone never replaces the evidence.
	req.Evidence = nil
	req.OverrideReason = "sources unreachable"
	_, err = artists.ApplyMapping(&c, req)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	require.Equal(t, idB, c.Artists[0].SpotifyID, "a failed request must not touch the catalog artist links")

	req.Evidence = []catalog.Evidence{mbEvidence}
	res, err := artists.ApplyMapping(&c, req)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileVerified, res.To)
	p, _ := c.Artists[0].ProfileFor(idA)
	require.Equal(t, "javi", p.Reviewer)
	require.Equal(t, "2026-10-20T09:00:00Z", p.ReviewedAt)
	require.Contains(t, p.Evidence[len(p.Evidence)-1].Rationale, "Audited override by javi: sources unreachable")
	require.Empty(t, catalog.ValidateEditorialCatalog(c))
}

func TestApplyMappingEnableOnlyWhenVerified(t *testing.T) {
	c := mappingCatalog()
	c.Artists[0].Enabled = false
	req := mapReq(artists.MapAddSecondary, idA)
	req.Enable = true
	_, err := artists.ApplyMapping(&c, req)
	require.ErrorIs(t, err, artists.ErrInvalidMapping)

	req.Verify, req.Evidence = true, []catalog.Evidence{mbEvidence}
	res, err := artists.ApplyMapping(&c, req)
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Equal(t, []string{idA}, c.Artists[0].SpotifyIDs)
}

func TestApplyMappingNeverTakesAnotherArtistsID(t *testing.T) {
	c := mappingCatalog()
	_, err := artists.ApplyMapping(&c, mapReq(artists.MapAddSecondary, idC))
	require.ErrorIs(t, err, artists.ErrInvalidMapping)
	require.Contains(t, err.Error(), "already belongs to other")
	require.Equal(t, "other", c.Artists[1].Slug)
	require.Equal(t, idC, c.Artists[1].SpotifyID)
}

func TestApplyMappingRejectUnlinksAndDisablesLastID(t *testing.T) {
	c := mappingCatalog()
	_, err := artists.ApplyMapping(&c, mapReq(artists.MapReject, idB))
	require.ErrorIs(t, err, artists.ErrInvalidMapping, "a note is required")

	req := mapReq(artists.MapReject, idB)
	req.Note = "Streetlife Originals is a nu jazz act"
	res, err := artists.ApplyMapping(&c, req)
	require.NoError(t, err)
	require.True(t, res.Unlinked)
	require.True(t, res.Disabled)
	a := c.Artists[0]
	require.Empty(t, a.AllSpotifyIDs())
	p, _ := a.ProfileFor(idB)
	require.Equal(t, catalog.ProfileRejected, p.Status)
	require.Equal(t, "javi", p.Reviewer)
	require.True(t, c.IsRejectedSpotifyID(idB))
	require.Empty(t, catalog.ValidateEditorialCatalog(c))

	// A rejection cannot be lifted without new independent evidence.
	_, err = artists.ApplyMapping(&c, mapReq(artists.MapAddSecondary, idB))
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)
}

func TestApplyMappingInconclusiveAndQuarantine(t *testing.T) {
	c := mappingCatalog()
	_, err := artists.ApplyMapping(&c, mapReq(artists.MapInconclusive, idB))
	require.ErrorIs(t, err, artists.ErrInvalidMapping)

	req := mapReq(artists.MapInconclusive, idB)
	req.Note = "MusicBrainz was down"
	res, err := artists.ApplyMapping(&c, req)
	require.NoError(t, err)
	require.Equal(t, catalog.ProfileNeedsReview, res.To)
	require.Equal(t, idB, c.Artists[0].SpotifyID, "inconclusive keeps the link")
	require.Empty(t, c.Artists[0].SyncableSpotifyIDs(catalog.SyncAllowUnreviewed))

	req = mapReq(artists.MapQuarantine, idB)
	req.Note = "mixed homonym"
	res, err = artists.ApplyMapping(&c, req)
	require.NoError(t, err)
	require.True(t, res.Unlinked)
	require.Equal(t, catalog.ProfileNeedsReview, c.Artists[0].StatusOf(idB))
	require.Empty(t, catalog.ValidateEditorialCatalog(c))
}

func TestApplyMappingRemoveAndReorder(t *testing.T) {
	c := mappingCatalog()
	c.Artists[0].SpotifyIDs = []string{idA}
	c.Artists[0].SpotifyProfiles = append(c.Artists[0].SpotifyProfiles, catalog.SpotifyProfile{ID: idA, Status: catalog.ProfileCandidate})

	res, err := artists.ApplyMapping(&c, mapReq(artists.MapReorderPrimary, idA))
	require.NoError(t, err)
	require.Equal(t, idB, res.Demoted)
	require.Equal(t, idA, c.Artists[0].SpotifyID)
	require.Equal(t, []string{idB}, c.Artists[0].SpotifyIDs)

	_, err = artists.ApplyMapping(&c, mapReq(artists.MapReorderPrimary, idA))
	require.ErrorIs(t, err, artists.ErrInvalidMapping, "already primary")

	res, err = artists.ApplyMapping(&c, mapReq(artists.MapRemove, idB))
	require.NoError(t, err)
	require.True(t, res.Unlinked)
	_, tracked := c.Artists[0].ProfileFor(idB)
	require.False(t, tracked, "a removed non-rejected profile is forgotten")
	require.Equal(t, []string{idA}, c.Artists[0].AllSpotifyIDs())
	require.Empty(t, catalog.ValidateEditorialCatalog(c))
}

func TestApplyMappingValidatesInput(t *testing.T) {
	c := mappingCatalog()
	for _, req := range []artists.MappingRequest{
		{Slug: "street", SpotifyID: "short", Action: artists.MapRemove, Reviewer: "j", At: mappingAt},
		{Slug: "street", SpotifyID: idA, Action: "teleport", Reviewer: "j", At: mappingAt},
		{Slug: "nobody", SpotifyID: idA, Action: artists.MapAddSecondary, Reviewer: "j", At: mappingAt},
		{Slug: "street", SpotifyID: idA, Action: artists.MapAddSecondary, At: mappingAt},
		{Slug: "street", SpotifyID: idA, Action: artists.MapAddSecondary, Reviewer: "j"},
	} {
		_, err := artists.ApplyMapping(&c, req)
		require.ErrorIs(t, err, artists.ErrInvalidMapping, "%+v", req)
	}
}

func TestApplyMappingFailureLeavesShallowCloneUntouched(t *testing.T) {
	original := mappingCatalog()
	original.Artists[0].Enabled = false
	shallow := original.Clone() // shares the artists' inner slices

	req := mapReq(artists.MapAddSecondary, idA)
	req.Enable = true // refused after the profile was already reviewed in memory
	_, err := artists.ApplyMapping(&shallow, req)
	require.ErrorIs(t, err, artists.ErrInvalidMapping)

	require.Empty(t, original.Artists[0].SpotifyIDs)
	require.Len(t, original.Artists[0].SpotifyProfiles, 1)
	require.Empty(t, shallow.Artists[0].SpotifyIDs)
	require.Len(t, shallow.Artists[0].SpotifyProfiles, 1)
}
