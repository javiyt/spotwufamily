package catalogsync_test

import (
	"context"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

func planned(t *testing.T, c catalog.EditorialCatalog) int {
	t.Helper()
	usecase := catalogsync.NewSyncCatalog(fakeStore{catalog: c}, nil, nil, fixedClock{})
	report, err := usecase.Run(context.Background(), catalogsync.Options{CatalogPath: "ignored", DryRun: true})
	require.NoError(t, err)
	return report.ArtistsPlanned
}

func TestSyncDoesNotIngestUnreviewedProfilesByDefault(t *testing.T) {
	c := catalog.EditorialCatalog{Version: 1, Artists: []catalog.Artist{enabledArtist("wu-tang-clan", "34EP7KEpOjXcM2TCat1ISk")}}
	require.Zero(t, planned(t, c), "enabled must not imply reviewed")

	c.ProfileSyncPolicy = catalog.SyncAllowUnreviewed
	require.Equal(t, 1, planned(t, c))
}

func TestSyncIgnoresCandidateAndRejectedProfiles(t *testing.T) {
	const verifiedID, candidateID = "34EP7KEpOjXcM2TCat1ISk", "0H8YCcvC3MPLKnbDRasGiG"
	artist := enabledArtist("wu-tang-clan", verifiedID)
	artist.SpotifyIDs = []string{candidateID}
	artist.SpotifyProfiles = []catalog.SpotifyProfile{
		{ID: verifiedID, Status: catalog.ProfileVerified, Reviewer: "javi", ReviewedAt: "2026-01-02T15:04:05Z", Evidence: []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/1", Rationale: "linked"}}},
		{ID: candidateID, Status: catalog.ProfileCandidate},
	}
	c := catalog.EditorialCatalog{Version: 1, ProfileSyncPolicy: catalog.SyncAllowUnreviewed, Artists: []catalog.Artist{artist}}

	fetcher := &fakeFetcher{artist: catalog.ArtistCandidate{Name: "Wu-Tang Clan", SpotifyID: verifiedID}}
	repository := &fakeRepository{}
	_, _ = catalogsync.NewSyncCatalog(fakeStore{catalog: c}, fetcher, repository, fixedClock{}).
		Run(context.Background(), catalogsync.Options{CatalogPath: "ignored", Market: "ES"})

	require.Equal(t, []string{verifiedID}, fetcher.artistIDs, "only the verified profile reaches Spotify")
}

func TestSyncSkipsArtistWhoseOnlyProfileIsBlocked(t *testing.T) {
	artist := enabledArtist("a", "34EP7KEpOjXcM2TCat1ISk")
	artist.SpotifyProfiles = []catalog.SpotifyProfile{{ID: "34EP7KEpOjXcM2TCat1ISk", Status: catalog.ProfileNeedsReview}}
	c := catalog.EditorialCatalog{Version: 1, ProfileSyncPolicy: catalog.SyncAllowUnreviewed, Artists: []catalog.Artist{artist}}
	require.Zero(t, planned(t, c))
}
