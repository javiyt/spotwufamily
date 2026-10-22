package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	creditRelease = "A1111111111111111111aa"
	creditTrack1  = "T1111111111111111111aa"
	creditTrack2  = "T2222222222222222222aa"
	creditTrack3  = "T3333333333333333333aa"
	creditTrack4  = "T4444444444444444444aa"
	creditTrack5  = "T5555555555555555555aa"
	otherWuID     = "9ZzZzZzZzZzZzZzZzZzZzZ" // same name as the group, different ID
)

func newcomerCredit(id, name string, others ...catalog.ArtistCandidate) catalog.Track {
	credits := append([]catalog.ArtistCandidate{{SpotifyID: newcomerID, Name: "Newcomer"}}, others...)
	return catalog.Track{SpotifyID: id, Name: name, Artists: credits}
}

var wuCredit = catalog.ArtistCandidate{SpotifyID: wuID, Name: "Wu-Tang Clan"}

// creditCard opens the newcomer profile by ID (a manual card) after letting the
// test shape the tracklist and catalog.
func creditCard(t *testing.T, tracks []catalog.Track, mutate func(*resolveFixture, *admin.ResolutionDeps)) (admin.Card, *resolveFixture) {
	t.Helper()
	f := newResolveFixture(t, func(f *resolveFixture, d *admin.ResolutionDeps) {
		f.spotify.tracks = map[string][]catalog.Track{creditRelease: tracks}
		if mutate != nil {
			mutate(f, d)
		}
	})
	wb, err := f.svc.ResolveWorkbench(context.Background(), "newcomer", newcomerID)
	require.NoError(t, err)
	require.Len(t, wb.Candidates, 1)
	return wb.Candidates[0], f
}

func TestCreditsWithVerifiedGroupAreActionableSupportingEvidence(t *testing.T) {
	card, _ := creditCard(t, []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit)}, nil)

	require.Equal(t, admin.CreditsChecked, card.Credits.Status)
	require.Len(t, card.Credits.Suggestions, 1)
	s := card.Credits.Suggestions[0]
	require.Equal(t, newcomerID, s.TargetProfileID)
	require.Equal(t, "wu-tang-clan", s.GroupSlug)
	require.Equal(t, wuID, s.GroupSpotifyID)
	require.Equal(t, creditTrack1, s.TrackID)
	require.Equal(t, "Opener", s.TrackName)
	require.Equal(t, creditRelease, s.ReleaseID)
	require.Equal(t, "First Light", s.ReleaseName)
	require.Equal(t, "https://open.spotify.com/track/"+creditTrack1, s.URL)
	require.Equal(t, admin.CreditSupporting, s.Classification)
	require.NotEmpty(t, s.Provenance)
	require.Contains(t, s.Rationale, "not independent")

	// The suggestion maps onto the existing credit evidence and stays valid.
	ev := s.Evidence()
	require.Equal(t, catalog.EvidenceCredit, ev.Type)
	require.Equal(t, creditTrack1, ev.TrackID)
	require.Equal(t, creditRelease, ev.ReleaseID)
	require.Empty(t, catalog.ValidateEvidence(ev))
	require.False(t, ev.Independent())
}

func TestCreditsAreNotTrustedWithoutExactVerifiedGroupAndTrackEvidence(t *testing.T) {
	cases := []struct {
		name   string
		tracks []catalog.Track
		mutate func(*resolveFixture, *admin.ResolutionDeps)
	}{
		{
			name:   "unverified group",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit)},
			mutate: func(f *resolveFixture, _ *admin.ResolutionDeps) {
				f.store.c.Artists[0].SpotifyProfiles[0].Status = catalog.ProfileCandidate
			},
		},
		{
			name:   "rejected group",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit)},
			mutate: func(f *resolveFixture, _ *admin.ResolutionDeps) {
				f.store.c.Artists[0].SpotifyProfiles[0].Status = catalog.ProfileRejected
			},
		},
		{
			name:   "same name different id",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener", catalog.ArtistCandidate{SpotifyID: otherWuID, Name: "Wu-Tang Clan"})},
		},
		{
			name:   "group not credited on the track",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener")},
		},
		{
			name: "target not credited on the track",
			tracks: []catalog.Track{{SpotifyID: creditTrack1, Name: "Opener",
				Artists: []catalog.ArtistCandidate{wuCredit, {SpotifyID: otherWuID, Name: "Someone"}}}},
		},
		{
			name: "same id credited under conflicting names",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit,
				catalog.ArtistCandidate{SpotifyID: wuID, Name: "Wu Tang Killa Bees"})},
		},
		{
			name: "same name credited with a different id beside the real one",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit,
				catalog.ArtistCandidate{SpotifyID: otherWuID, Name: "Wu-Tang Clan"})},
		},
		{
			name:   "credit without an id",
			tracks: []catalog.Track{newcomerCredit(creditTrack1, "Opener", catalog.ArtistCandidate{Name: "Wu-Tang Clan"})},
		},
		{
			name:   "track without a usable id",
			tracks: []catalog.Track{newcomerCredit("T1", "Opener", wuCredit)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card, _ := creditCard(t, tc.tracks, tc.mutate)
			require.Empty(t, card.Credits.Suggestions, "no fabricated or untrusted credit evidence")
			require.NotEqual(t, admin.CreditsRateLimited, card.Credits.Status)
		})
	}
}

func TestCreditOnlySelectionCannotVerifyAProfile(t *testing.T) {
	card, f := creditCard(t, []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit)}, nil)
	require.Len(t, card.Credits.Suggestions, 1)
	ctx := context.Background()

	wb, err := f.svc.ResolveWorkbench(ctx, "newcomer", "")
	require.NoError(t, err)
	link := req("newcomer", newcomerID, "accept_primary")
	link.Revision = wb.Revision
	_, err = f.svc.ApplyResolution(ctx, link)
	require.NoError(t, err)

	wb, err = f.svc.ResolveWorkbench(ctx, "newcomer", "")
	require.NoError(t, err)
	v := req("newcomer", newcomerID, admin.ActionVerify)
	v.Revision, v.Evidence = wb.Revision, []catalog.Evidence{card.Credits.Suggestions[0].Evidence()}
	_, err = f.svc.ApplyResolution(ctx, v)
	require.ErrorIs(t, err, catalog.ErrInvalidTransition)
	got := f.store.c.Artists[f.store.c.FindArtist("newcomer")]
	require.NotEqual(t, catalog.ProfileVerified, got.StatusOf(newcomerID))
}

func TestCreditsAreDeduplicatedBoundedAndOrdered(t *testing.T) {
	tracks := []catalog.Track{
		newcomerCredit(creditTrack5, "Fifth", wuCredit),
		newcomerCredit(creditTrack1, "First", wuCredit),
		newcomerCredit(creditTrack2, "Second", wuCredit),
		newcomerCredit(creditTrack1, "First", wuCredit), // same track listed twice
		newcomerCredit(creditTrack3, "Third", wuCredit),
		newcomerCredit(creditTrack4, "Fourth", wuCredit),
	}
	card, _ := creditCard(t, tracks, nil)

	var ids []string
	for _, s := range card.Credits.Suggestions {
		ids = append(ids, s.TrackID)
	}
	require.Equal(t, []string{creditTrack1, creditTrack2, creditTrack3}, ids, "sorted by track id, one per track, capped")
	require.Equal(t, 2, card.Credits.Omitted)
}

func TestCreditsReportMissingCoverageInsteadOfNoCredits(t *testing.T) {
	t.Run("track cache miss", func(t *testing.T) {
		card, _ := creditCard(t, nil, func(f *resolveFixture, d *admin.ResolutionDeps) {
			d.Source = trackFailure{fakeSpotify: f.spotify, err: artists.ErrCacheMiss}
		})
		require.Equal(t, admin.CreditsNotChecked, card.Credits.Status)
		require.Empty(t, card.Credits.Suggestions)
		require.NotEmpty(t, card.Credits.Message)
	})

	t.Run("rate limited carries retry-after", func(t *testing.T) {
		card, _ := creditCard(t, nil, func(f *resolveFixture, d *admin.ResolutionDeps) {
			d.Source = trackFailure{fakeSpotify: f.spotify, err: rateLimited{wait: 30 * time.Second}}
		})
		require.Equal(t, admin.CreditsRateLimited, card.Credits.Status)
		require.Equal(t, 30*time.Second, card.Credits.RetryAfter)
	})

	t.Run("no release to inspect", func(t *testing.T) {
		f := newResolveFixture(t, nil)
		wb, err := f.svc.ResolveWorkbench(context.Background(), "multi", "")
		require.NoError(t, err)
		require.Equal(t, admin.CreditsNotChecked, wb.Attached[0].Credits.Status)
	})

	t.Run("no verified group to compare with", func(t *testing.T) {
		card, _ := creditCard(t, []catalog.Track{newcomerCredit(creditTrack1, "Opener", wuCredit)}, func(f *resolveFixture, _ *admin.ResolutionDeps) {
			f.store.c.Artists[0].SpotifyProfiles[0].Status = catalog.ProfileCandidate
		})
		require.Equal(t, admin.CreditsChecked, card.Credits.Status)
	})
}

// trackFailure fails only the tracklist read, so the rest of the card loads.
type trackFailure struct {
	*fakeSpotify
	err error
}

func (t trackFailure) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return nil, t.err
}
