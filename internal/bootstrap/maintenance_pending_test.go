package bootstrap

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

var _ httpadmin.PendingProvider = (*maintenance.Service)(nil)

// pendingService is a real maintenance service over a temp SQLite database
// and YAML catalog, with the database stage actions wired as in the admin.
func pendingService(t *testing.T, cfg MaintenanceActionsConfig) *maintenance.Service {
	t.Helper()
	var actions []maintenance.Action
	actions = append(actions, NewPublishPrepActions(cfg)...)
	actions = append(actions, NewReconcileActions(cfg)...)
	actions = append(actions, NewSeedAction(cfg))
	m := jobs.NewManager(&memJobStore{}, time.Now, newJobID)
	return maintenance.NewService(m, actions, []byte("secret"), time.Now, maintenance.WithPending(NewPendingDeps(cfg)))
}

type memJobStore struct{}

func (memJobStore) Save(context.Context, jobs.Job) error     { return nil }
func (memJobStore) Load(context.Context) ([]jobs.Job, error) { return nil, nil }

func pendingStage(t *testing.T, s *maintenance.Service, name string) maintenance.StageView {
	t.Helper()
	v, err := s.Pending(context.Background())
	require.NoError(t, err)
	for _, st := range v.Stages {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no stage %s", name)
	return maintenance.StageView{}
}

func TestPendingStagesFollowRealDigests(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")
	migrateDB(t, cfg.DBPath)

	// Legacy database: unknown, never clean.
	s := pendingService(t, cfg)
	v, err := s.Pending(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.PendingUnknown, v.State)

	writeApplied(t, cfg.DBPath, currentFingerprint(t, cfg.CatalogPath))
	require.Equal(t, maintenance.StageTodo, pendingStage(t, s, maintenance.PlanSnapshot).Status)

	for _, name := range []string{"db-snapshot", "export"} {
		out, err := runSpec(t, prepActionsByName(cfg)[name], maintenance.Params{})
		require.NoError(t, err)
		require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	}
	// A new service over the same database sees the checkpoints as done.
	s = pendingService(t, cfg)
	for _, name := range []string{maintenance.PlanSeed, maintenance.PlanSnapshot, maintenance.PlanExport} {
		st := pendingStage(t, s, name)
		require.Equal(t, maintenance.NeedNotNeeded, st.Need, name)
		require.Equal(t, maintenance.StageDone, st.Status, name)
	}

	// A database write breaks the match: the snapshot is to do again.
	database, err := sqliteadapter.Open(cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.SaveConfiguredArtists(context.Background(),
		[]catalog.Artist{{Slug: "extra-artist", Name: "Extra", Category: "core"}}))
	require.NoError(t, database.Close())
	st := pendingStage(t, s, maintenance.PlanSnapshot)
	require.Equal(t, maintenance.NeedRequired, st.Need)
	require.Equal(t, maintenance.StageTodo, st.Status)
	for _, name := range []string{"db-snapshot", "export"} {
		out, err := runSpec(t, prepActionsByName(cfg)[name], maintenance.Params{})
		require.NoError(t, err)
		require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	}
	require.Equal(t, maintenance.StageDone, pendingStage(t, s, maintenance.PlanSnapshot).Status)

	// A metadata edit with the mtime preserved: seed required, sync not.
	info, err := os.Stat(cfg.CatalogPath)
	require.NoError(t, err)
	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	edited := bytes.Replace(body, []byte("name: Wu-Tang Clan"), []byte("name: Wu-Tang Clan (edited)"), 1)
	require.NotEqual(t, body, edited)
	require.NoError(t, os.WriteFile(cfg.CatalogPath, edited, 0o600))
	require.NoError(t, os.Chtimes(cfg.CatalogPath, info.ModTime(), info.ModTime()))
	require.Equal(t, maintenance.NeedRequired, pendingStage(t, s, maintenance.PlanSeed).Need)
	require.Equal(t, maintenance.NeedNotNeeded, pendingStage(t, s, maintenance.PlanSync).Need)
	require.Equal(t, maintenance.NeedRequired, pendingStage(t, s, maintenance.PlanSnapshot).Need)

	// The real seed applies the metadata change; only then is the seed done.
	out, err := runSpec(t, NewSeedAction(cfg), maintenance.Params{})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	require.Equal(t, maintenance.StageDone, pendingStage(t, s, maintenance.PlanSeed).Status)
	require.Equal(t, maintenance.NeedRequired, pendingStage(t, s, maintenance.PlanExport).Need,
		"the export checkpoint was taken against the previous catalog and database")
}
