package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/stretchr/testify/require"
)

var _ artists.ProvenanceStore = (*sqliteadapter.Database)(nil)

func TestProvenanceRoundTrip(t *testing.T) {
	ctx := context.Background()
	database, err := sqliteadapter.Open(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	require.NoError(t, database.Migrate(ctx))

	applied := artists.AppliedCatalog{
		Digest: "abc",
		Artists: []artists.ArtistFingerprint{
			{Slug: "method-man", SpotifyIDs: []string{"a", "b"}, Verified: true, Enabled: true, Relationships: []string{"x"}, Metadata: "m"},
		},
		AppliedAt: "2026-10-22T10:00:00Z",
	}
	require.NoError(t, database.RecordApplied(ctx, applied))
	got, ok, err := database.AppliedCatalog(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, applied, got)

	applied.Digest = "def"
	require.NoError(t, database.RecordApplied(ctx, applied))
	got, _, err = database.AppliedCatalog(ctx)
	require.NoError(t, err)
	require.Equal(t, "def", got.Digest)

	_, ok, err = database.Checkpoint(ctx, artists.StageSync)
	require.NoError(t, err)
	require.False(t, ok)

	cp := artists.StageCheckpoint{Stage: artists.StageSync, CatalogDigest: "c1", DBDigest: "d1", At: "2026-10-22T10:01:00Z"}
	require.NoError(t, database.RecordCheckpoint(ctx, cp))
	cp.DBDigest = "d2"
	require.NoError(t, database.RecordCheckpoint(ctx, cp))
	gotCP, ok, err := database.Checkpoint(ctx, artists.StageSync)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, cp, gotCP)
}

func TestProvenanceEmptyOnLegacyDB(t *testing.T) {
	ctx := context.Background()
	database, err := sqliteadapter.Open(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()

	migrations, err := sqliteadapter.EmbeddedMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(migrations), 7)
	require.NoError(t, sqliteadapter.ApplyMigrations(database.DB(), migrations[:6]))
	require.NoError(t, database.Migrate(ctx))

	_, ok, err := database.AppliedCatalog(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = database.Checkpoint(ctx, artists.StageExport)
	require.NoError(t, err)
	require.False(t, ok)
}
