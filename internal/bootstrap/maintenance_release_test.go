package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

var (
	localSHA  = strings.Repeat("a", 64)
	pinnedSHA = strings.Repeat("b", 64)
)

// countingReleaser is a fake Releaser that never runs anything.
type countingReleaser struct {
	mu                            sync.Mutex
	prereq                        maintenance.Prereq
	err                           error
	preflights, prepares, publish int
}

func (r *countingReleaser) Preflight(context.Context) (maintenance.Prereq, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preflights++
	return r.prereq, r.err
}

func (r *countingReleaser) Prepare(context.Context) (maintenance.PackageInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prepares++
	return maintenance.PackageInfo{Tag: "catalog-new", ContentSHA256: localSHA}, nil
}

func (r *countingReleaser) Publish(context.Context) (maintenance.PublishInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publish++
	return maintenance.PublishInfo{Tag: "catalog-new", Published: true}, nil
}

func (r *countingReleaser) counts() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.preflights, r.prepares, r.publish
}

func readyReleaser() *countingReleaser {
	return &countingReleaser{prereq: maintenance.Prereq{
		GhAuthenticated: true, GoAvailable: true, PackageDirClean: true,
		LocalContentSHA256: localSHA, PinnedContentSHA256: pinnedSHA, LastReleaseTag: "catalog-old",
	}}
}

func releaseConfig(t *testing.T, r maintenance.Releaser) ReleaseActionsConfig {
	t.Helper()
	dir := t.TempDir()
	return ReleaseActionsConfig{
		RepoRoot:    dir,
		Repo:        "javiyt/spotwufamily",
		DBPath:      filepath.Join(dir, "catalog.db"),
		JournalPath: filepath.Join(dir, "admin", "jobs.jsonl"),
		Releaser:    r,
	}
}

func TestReleaseActionsAreRegistered(t *testing.T) {
	actions, err := NewReleaseActions(releaseConfig(t, readyReleaser()))
	require.NoError(t, err)
	var names []string
	for _, a := range actions {
		names = append(names, a.Name())
	}
	require.Equal(t, []string{"release-prepare", "release-publish", "release-pr"}, names)
}

func TestNewReleaseActionsRejectsInvalidRepo(t *testing.T) {
	cfg := releaseConfig(t, nil)
	cfg.Repo = "evil/repo; rm -rf /"
	_, err := NewReleaseActions(cfg)
	require.Error(t, err)
}

func TestJournalPreparedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	store := journalPrepared{path: path}
	_, ok := store.LastPrepared()
	require.False(t, ok, "no journal, nothing prepared")

	j := filesystem.NewJobJournal(path)
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	report := func(sha string) *jobs.Result {
		return &jobs.Result{Report: &jobs.Report{Name: "release-prepare",
			Columns: []string{"tag", "content_sha256", "assets"}, Rows: [][]string{{"catalog-x", sha, ""}}}}
	}
	require.NoError(t, j.Save(ctx, jobs.Job{ID: "1", Kind: "release-prepare", Status: jobs.StatusSucceeded, FinishedAt: at, Result: report(pinnedSHA)}))
	require.NoError(t, j.Save(ctx, jobs.Job{ID: "2", Kind: "release-prepare", Status: jobs.StatusSucceeded, FinishedAt: at.Add(time.Hour), Result: report(localSHA)}))
	require.NoError(t, j.Save(ctx, jobs.Job{ID: "3", Kind: "release-prepare", Status: jobs.StatusFailed, FinishedAt: at.Add(2 * time.Hour), Result: report(strings.Repeat("c", 64))}))
	require.NoError(t, j.Save(ctx, jobs.Job{ID: "4", Kind: "db-snapshot", Status: jobs.StatusSucceeded, FinishedAt: at.Add(3 * time.Hour), Result: report(strings.Repeat("d", 64))}))
	sha, ok := store.LastPrepared()
	require.True(t, ok)
	require.Equal(t, localSHA, sha, "the latest successful release-prepare wins")

	require.NoError(t, j.Save(ctx, jobs.Job{ID: "5", Kind: "release-prepare", Status: jobs.StatusSucceeded, FinishedAt: at.Add(4 * time.Hour),
		Result: &jobs.Result{Summary: "no report"}}))
	_, ok = store.LastPrepared()
	require.False(t, ok, "a latest success without a valid report proves nothing")
}

func TestReleasePrepareThenPublishThroughJournal(t *testing.T) {
	r := readyReleaser()
	cfg := releaseConfig(t, r)
	actions, err := NewReleaseActions(cfg)
	require.NoError(t, err)
	var n int
	m := jobs.NewManager(filesystem.NewJobJournal(cfg.JournalPath), time.Now, func() string { n++; return string(rune('a' + n)) })
	svc := maintenance.NewService(m, actions, []byte("secret"), time.Now)
	ctx := context.Background()

	_, tok, err := svc.Preview(ctx, "release-publish", nil)
	require.NoError(t, err)
	_, err = svc.Run(ctx, "release-publish", nil, tok, "publish release")
	require.ErrorIs(t, err, maintenance.ErrNothingPrepared)

	_, tok, err = svc.Preview(ctx, "release-prepare", nil)
	require.NoError(t, err)
	job, err := svc.Run(ctx, "release-prepare", nil, tok, "")
	require.NoError(t, err)
	m.Wait(job.ID)
	got, _ := m.Get(job.ID)
	require.Equal(t, jobs.StatusSucceeded, got.Status, "%+v", got)

	// The package is now on disk.
	r.mu.Lock()
	r.prereq.PackageDirClean = false
	r.prereq.PreparedTag, r.prereq.PreparedContentSHA256 = "catalog-new", localSHA
	r.mu.Unlock()

	_, tok, err = svc.Preview(ctx, "release-publish", nil)
	require.NoError(t, err)
	job, err = svc.Run(ctx, "release-publish", nil, tok, "publish release")
	require.NoError(t, err)
	m.Wait(job.ID)
	got, _ = m.Get(job.ID)
	require.Equal(t, jobs.StatusSucceeded, got.Status, "%+v", got)
	require.Equal(t, "Published catalog release catalog-new.", got.Result.Summary)
	_, _, published := r.counts()
	require.Equal(t, 1, published)
}

// TestNoAutomaticPublishAfterEdit edits the catalog and then previews and
// runs every other registered action (and Freshness) with a fake Releaser:
// none of them may ever publish.
func TestNoAutomaticPublishAfterEdit(t *testing.T) {
	cfg, _ := newPrepConfig(t)
	cfg.Options = []MaintenanceOption{WithWikipedia("http://127.0.0.1:1/w/api.php", "")}
	migrateDB(t, cfg.DBPath)
	r := readyReleaser()
	rcfg := releaseConfig(t, r)
	rcfg.DBPath = cfg.DBPath

	// An edit of the editorial catalog.
	data, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.CatalogPath, append(data, []byte("\n# edited\n")...), 0o644))

	releaseActions, err := NewReleaseActions(rcfg)
	require.NoError(t, err)
	var all []maintenance.Action
	all = append(all, NewDiscoveryAuditRefreshActions(cfg)...)
	all = append(all, NewSyncActions(cfg)...)
	all = append(all, NewReconcileActions(cfg)...)
	all = append(all, NewPublishPrepActions(cfg)...)
	all = append(all, releaseActions...)

	paramSets := []maintenance.Params{
		{}, {"offline": "true"}, {"artist": "wu-tang-clan"}, {"artist": "wu-tang-clan", "offline": "true"},
		{"dry_run": "true"}, {"force": "true"},
	}
	ran := 0
	for _, a := range all {
		if a.Name() == "release-publish" {
			continue
		}
		for _, p := range paramSets {
			if a.Validate(p) != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			spec, err := confirmedSpec(t, a, p)
			if err == nil && spec.Run != nil {
				_, _ = spec.Run(ctx, nopReporter{})
				ran++
			}
			cancel()
		}
	}
	require.Positive(t, ran)
	src := NewFreshnessSource(FreshnessConfig{
		CatalogPath: cfg.CatalogPath, LedgerPath: cfg.LedgerPath, DBPath: cfg.DBPath,
		SnapshotPath: cfg.SnapshotPath, ExportOutputDir: cfg.ExportOutputDir,
		ExportStaticDir: cfg.ExportStaticDir, ExportContentDir: cfg.ExportContentDir, Releaser: r,
	})
	_, err = maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(src)).Freshness(context.Background())
	require.NoError(t, err)

	_, prepares, published := r.counts()
	require.Equal(t, 0, published, "no action other than release-publish may publish")
	require.Equal(t, 1, prepares, "only release-prepare prepares")
}

func TestFreshnessSourceReadsReleaseHashesOncePerCall(t *testing.T) {
	cfg := freshnessCfg(t)
	migrateDB(t, cfg.DBPath)
	r := readyReleaser()
	cfg.Releaser = r
	svc := maintenance.NewService(nil, nil, nil, time.Now, maintenance.WithFreshness(NewFreshnessSource(cfg)))

	f, err := svc.Freshness(context.Background())
	require.NoError(t, err)
	require.True(t, f.DiffersFromPublished)
	preflights, _, _ := r.counts()
	require.Equal(t, 1, preflights, "one preflight per Freshness call")

	_, err = svc.Freshness(context.Background())
	require.NoError(t, err)
	preflights, _, _ = r.counts()
	require.Equal(t, 2, preflights, "not cached across calls")

	src := NewFreshnessSource(cfg)
	pinned, ok := src.PinnedContentSHA()
	require.True(t, ok)
	require.Equal(t, pinnedSHA, pinned)
	local, ok := src.LocalContentSHA()
	require.True(t, ok)
	require.Equal(t, localSHA, local)
}

func TestFreshnessSourceReleaseHashesAbsent(t *testing.T) {
	cfg := freshnessCfg(t)
	for name, r := range map[string]maintenance.Releaser{
		"no releaser":        nil,
		"preflight fails":    &countingReleaser{err: errors.New("python3 missing")},
		"no pinned manifest": &countingReleaser{prereq: maintenance.Prereq{}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Releaser = r
			src := NewFreshnessSource(cfg)
			_, ok := src.PinnedContentSHA()
			require.False(t, ok)
			_, ok = src.LocalContentSHA()
			require.False(t, ok)
		})
	}
}
