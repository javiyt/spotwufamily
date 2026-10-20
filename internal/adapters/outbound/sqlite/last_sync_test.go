package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLastSyncReturnsLatestRun(t *testing.T) {
	ctx := context.Background()
	database := openMigratedDatabase(t, ctx, filepath.Join(t.TempDir(), "catalog.db"))
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, ok, err := database.LastSync(ctx)
	require.NoError(t, err)
	require.False(t, ok)

	_, err = database.DB().ExecContext(ctx, `
INSERT INTO sync_runs(started_at, finished_at, status, market) VALUES
('2026-01-01T00:00:00Z', '2026-01-01T00:10:00Z', 'failed', 'ES'),
('2026-02-01T00:00:00Z', '2026-02-01T00:10:00Z', 'completed', 'ES'),
('2026-03-01T00:00:00Z', NULL, 'running', 'ES')`)
	require.NoError(t, err)

	info, ok, err := database.LastSync(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "running", info.Status)
	require.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), info.StartedAt)
	require.True(t, info.FinishedAt.IsZero())
}
