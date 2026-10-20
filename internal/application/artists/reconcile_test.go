package artists_test

import (
	"context"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
	"github.com/stretchr/testify/require"
)

type reconcileCatalog struct{ c catalog.EditorialCatalog }

func (r reconcileCatalog) Load(context.Context, string) (catalog.EditorialCatalog, error) {
	return r.c, nil
}

func (r reconcileCatalog) Save(context.Context, string, catalog.EditorialCatalog) error { return nil }

type memoryReconcileStore struct {
	associations []artists.Association
	retired      []string
	retiredAt    string
}

func (m *memoryReconcileStore) ActiveAssociations(context.Context, string) ([]artists.Association, error) {
	return m.associations, nil
}

func (m *memoryReconcileStore) RetireAssociations(_ context.Context, _ string, albums, tracks []string, at string) error {
	m.retired = append(append(m.retired, albums...), tracks...)
	m.retiredAt = at
	return nil
}

func (m *memoryReconcileStore) RestoreAssociations(context.Context, string, string) (int, int, error) {
	return 0, 0, nil
}

func TestReconcileClassifiesAndKeepsUnverifiable(t *testing.T) {
	store := &memoryReconcileStore{associations: []artists.Association{
		{Kind: artists.AssociationAlbum, SpotifyID: "wu", CreditedArtistIDs: []string{"linked"}},
		{Kind: artists.AssociationAlbum, SpotifyID: "homonym", CreditedArtistIDs: []string{"rejected"}},
		{Kind: artists.AssociationTrack, SpotifyID: "nocredits"},
	}}
	cat := reconcileCatalog{c: catalog.EditorialCatalog{Artists: []catalog.Artist{{Slug: "a", SpotifyID: "linked"}}}}

	report, err := artists.NewReconcileArtist(cat, store).Run(context.Background(), artists.ReconcileInput{Slug: "a", Apply: true, Now: time.Unix(0, 0)})
	require.NoError(t, err)
	require.Equal(t, []string{"homonym"}, store.retired)
	require.Equal(t, artists.ReconcileCounts{Tracks: 1}, report.Unverifiable)
	require.Equal(t, artists.ReconcileCounts{Albums: 1, Tracks: 1}, report.After)
}

func TestReconcileRejectsUnknownArtistAndConflictingModes(t *testing.T) {
	use := artists.NewReconcileArtist(reconcileCatalog{}, &memoryReconcileStore{})
	_, err := use.Run(context.Background(), artists.ReconcileInput{Slug: "missing"})
	require.ErrorContains(t, err, "not found")
	_, err = use.Run(context.Background(), artists.ReconcileInput{Slug: "a", Apply: true, RestoreRetiredAt: "x"})
	require.ErrorContains(t, err, "cannot be combined")
}

func TestProfileAuditReportsRejectedHistoryWithoutLinkedProfiles(t *testing.T) {
	cat := reconcileCatalog{c: catalog.EditorialCatalog{Artists: []catalog.Artist{{
		Slug: "k-g-b", Name: "K.G.B.",
		SpotifyProfiles: []catalog.SpotifyProfile{{
			ID: "2gKwx0Lm0JoOrN2mY9Z0PG", Status: catalog.ProfileRejected, Reviewer: "javi", ReviewedAt: "2026-10-20T08:20:21Z",
			Evidence: []catalog.Evidence{{Type: catalog.EvidenceMusicBrainz, URL: "https://musicbrainz.org/artist/x", Rationale: "mixed homonym"}},
		}},
	}}}}

	report, err := artists.NewAuditProfiles(cat, nil, nil).Run(context.Background(), artists.AuditProfilesOptions{ArtistSlug: "k-g-b", Offline: true})
	require.NoError(t, err)
	require.Empty(t, report.Profiles)
	require.Len(t, report.RejectedProfiles, 1)
	require.Equal(t, "2gKwx0Lm0JoOrN2mY9Z0PG", report.RejectedProfiles[0].SpotifyID)
	markdown := string(artists.FormatProfileAuditMarkdown(report))
	require.Contains(t, markdown, "Rejected profiles (history)")
	require.Contains(t, markdown, "mixed homonym")
}
