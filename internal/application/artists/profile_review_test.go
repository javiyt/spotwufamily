package artists_test

import (
	"context"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

const (
	reviewIDA = "34EP7KEpOjXcM2TCat1ISk"
	reviewIDB = "0H8YCcvC3MPLKnbDRasGiG"
)

var reviewAt = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func reviewStore() *memoryStore {
	return &memoryStore{catalog: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "a", Name: "A", SpotifyID: reviewIDA, Category: catalog.CategoryCore, Enabled: true},
		{Slug: "b", Name: "B", Category: catalog.CategoryCore},
	}}}
}

func evidence() []catalog.Evidence {
	return []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/1", Rationale: "MusicBrainz lists it"}}
}

func TestReviewProfileVerifiesLegacyID(t *testing.T) {
	store := reviewStore()
	result, err := artists.NewReviewProfile(store).Run(context.Background(), "x", artists.ReviewProfileInput{
		Slug: "a", SpotifyID: reviewIDA, Status: catalog.ProfileVerified, Evidence: evidence(), Reviewer: "javi", ReviewedAt: reviewAt,
	})
	require.NoError(t, err)
	require.True(t, result.Saved)
	require.Equal(t, catalog.ProfileUnreviewed, result.From)
	profile := store.catalog.Artists[0].SpotifyProfiles[0]
	require.Equal(t, catalog.ProfileVerified, profile.Status)
	require.Equal(t, "2026-03-04T05:06:07Z", profile.ReviewedAt)
	require.Equal(t, reviewIDA, store.catalog.Artists[0].SpotifyID, "legacy fields are untouched")
}

func TestReviewProfileRequiresLinkForNewID(t *testing.T) {
	store := reviewStore()
	in := artists.ReviewProfileInput{Slug: "a", SpotifyID: reviewIDB, Status: catalog.ProfileCandidate, ReviewedAt: reviewAt}
	_, err := artists.NewReviewProfile(store).Run(context.Background(), "x", in)
	require.ErrorContains(t, err, "--link")

	in.Link = true
	result, err := artists.NewReviewProfile(store).Run(context.Background(), "x", in)
	require.NoError(t, err)
	require.True(t, result.Linked)
	require.Equal(t, []string{reviewIDB}, store.catalog.Artists[0].SpotifyIDs)
}

func TestReviewProfileRejectUnlinksAndBlocksReapproval(t *testing.T) {
	store := reviewStore()
	run := artists.NewReviewProfile(store)
	result, err := run.Run(context.Background(), "x", artists.ReviewProfileInput{
		Slug: "a", SpotifyID: reviewIDA, Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: reviewAt,
		Evidence: []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "different artist with the same name"}},
	})
	require.NoError(t, err)
	require.True(t, result.Unlinked)
	require.Empty(t, store.catalog.Artists[0].AllSpotifyIDs())
	require.False(t, store.catalog.Artists[0].Enabled)
	require.True(t, store.catalog.IsRejectedSpotifyID(reviewIDA))

	// Another artist cannot claim the rejected ID, and name-only evidence cannot lift the rejection.
	_, err = run.Run(context.Background(), "x", artists.ReviewProfileInput{Slug: "b", SpotifyID: reviewIDA, Status: catalog.ProfileCandidate, Link: true, ReviewedAt: reviewAt})
	require.ErrorContains(t, err, "already belongs")
	_, err = run.Run(context.Background(), "x", artists.ReviewProfileInput{
		Slug: "a", SpotifyID: reviewIDA, Status: catalog.ProfileVerified, Link: true, Reviewer: "javi", ReviewedAt: reviewAt,
		Evidence: []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "same name"}},
	})
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	require.Empty(t, store.catalog.Artists[0].AllSpotifyIDs())
}

func TestReviewProfileDryRunDoesNotSave(t *testing.T) {
	store := reviewStore()
	_, err := artists.NewReviewProfile(store).Run(context.Background(), "x", artists.ReviewProfileInput{
		Slug: "a", SpotifyID: reviewIDA, Status: catalog.ProfileCandidate, DryRun: true, ReviewedAt: reviewAt,
	})
	require.NoError(t, err)
	require.Zero(t, store.saveCount)
}

func TestResolveApplyRecordsCandidateAndSkipsRejected(t *testing.T) {
	store := &memoryStore{catalog: catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{
		{Slug: "gravediggaz", Name: "Gravediggaz", Category: catalog.CategoryAffiliateGroup, EditorialOrder: 1},
		{Slug: "other", Name: "Other", Category: catalog.CategoryCore, EditorialOrder: 2, SpotifyProfiles: []catalog.SpotifyProfile{{
			ID: "0CH4f9m2L3TRaA5oErU2p0", Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceNameMatch, Rationale: "not the same group"}},
		}}},
	}}}
	searcher := candidateSearcher{candidates: map[string][]catalog.ArtistCandidate{
		"gravediggaz": {{Name: "Gravediggaz", SpotifyID: "0CH4f9m2L3TRaA5oErU2p0", RelatedArtistEvidence: []string{`credited on Wu-Tang Clan track "Triumph"`}}},
	}}

	report, err := artists.NewResolveArtists(store, searcher).Apply(context.Background(), "x", artists.ApplyResolveOptions{EnableResolved: true})
	require.NoError(t, err)
	require.Empty(t, report.Applied)
	require.Equal(t, "gravediggaz", report.Skipped[0].Slug)
	require.Contains(t, report.Skipped[0].Reason, "rejected")
	require.False(t, store.catalog.Artists[0].Enabled)

	searcher.candidates["gravediggaz"][0].SpotifyID = "1111111111111111111111"
	_, err = artists.NewResolveArtists(store, searcher).Apply(context.Background(), "x", artists.ApplyResolveOptions{EnableResolved: true})
	require.NoError(t, err)
	got := store.catalog.Artists[0]
	require.Equal(t, catalog.ProfileCandidate, got.StatusOf("1111111111111111111111"))
	require.Empty(t, got.SyncableSpotifyIDs(catalog.SyncAllowUnreviewed), "a name match never reaches sync")
}
