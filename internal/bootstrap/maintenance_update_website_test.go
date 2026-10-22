package bootstrap

import (
	"context"
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

// statefulReleaser is a fake release script whose package and manifest
// follow prepare and publish.
type statefulReleaser struct {
	mu sync.Mutex
	p  maintenance.Prereq
}

func newStatefulReleaser() *statefulReleaser {
	return &statefulReleaser{p: maintenance.Prereq{GhAuthenticated: true, GoAvailable: true, PackageDirClean: true,
		LocalContentSHA256: localSHA, PinnedContentSHA256: pinnedSHA, LastReleaseTag: "catalog-old"}}
}

func (r *statefulReleaser) Preflight(context.Context) (maintenance.Prereq, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.p, nil
}

func (r *statefulReleaser) Prepare(context.Context) (maintenance.PackageInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.p.PackageDirClean, r.p.PreparedTag, r.p.PreparedContentSHA256 = false, "catalog-new", r.p.LocalContentSHA256
	return maintenance.PackageInfo{Tag: "catalog-new", ContentSHA256: r.p.LocalContentSHA256}, nil
}

func (r *statefulReleaser) Publish(context.Context) (maintenance.PublishInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.p.PinnedContentSHA256, r.p.PinnedMatchesLocal = r.p.LocalContentSHA256, true
	r.p.PackageDirClean, r.p.PreparedTag, r.p.PreparedContentSHA256 = true, "", ""
	return maintenance.PublishInfo{Tag: "catalog-new", Published: true}, nil
}

func (r *statefulReleaser) pinned() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.p.PinnedMatchesLocal
}

// gitlessPRPort answers like the git port without git: the catalog digest
// is the live one and the manifest is pinned once the release is published.
type gitlessPRPort struct {
	cfg    MaintenanceActionsConfig
	r      *statefulReleaser
	mu     sync.Mutex
	opened int
}

func (p *gitlessPRPort) Check(ctx context.Context) (maintenance.PRPrereq, error) {
	digest, err := newProvenance(p.cfg.DBPath, p.cfg.CatalogPath).CatalogDigest(ctx)
	if err != nil {
		return maintenance.PRPrereq{}, err
	}
	return maintenance.PRPrereq{GhAuthenticated: true, OriginResolved: true, OriginMatchesRepo: true,
		Repo: "javiyt/spotwufamily", BaseBranch: "main", ManifestMatchesLocal: p.r.pinned(),
		ManifestContentSHA256: localSHA, ReleaseTag: "catalog-new", CatalogDigest: digest,
		FilesPresent: maintenance.ReleasePRFiles, FilesChanged: []string{"data/artists.yaml", "data/catalog.release.json"}}, nil
}

func (p *gitlessPRPort) Open(context.Context, maintenance.PRRequest) (maintenance.PRResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opened++
	return maintenance.PRResult{URL: "https://github.com/javiyt/spotwufamily/pull/9"}, nil
}

// websiteService is the admin's maintenance service over real SQLite, YAML,
// export and job journal, with Spotify, the release script and git faked.
func websiteService(t *testing.T) (*maintenance.Service, *jobs.Manager, MaintenanceActionsConfig, *gitlessPRPort) {
	t.Helper()
	cfg, _ := newPrepConfig(t)
	cfg.Env = credentials
	cfg.Options = append(cfg.Options, withSyncFetcher(okSyncFetcher{}))
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")
	journal := filepath.Join(t.TempDir(), "jobs.jsonl")
	r := newStatefulReleaser()
	port := &gitlessPRPort{cfg: cfg, r: r}
	prov := newProvenance(cfg.DBPath, cfg.CatalogPath)
	rel := maintenance.ReleaseDeps{Releaser: r, Prepared: journalPrepared{path: journal},
		Locker: databaseLocker{lock: filesystem.NewResourceLock(cfg.DBPath + ".lock")}, Repo: "javiyt/spotwufamily",
		PackageName: releasePackageName, ManifestName: releaseManifestName, Provenance: prov}
	var actions []maintenance.Action
	actions = append(actions, NewSyncActions(cfg)...)
	actions = append(actions, NewPublishPrepActions(cfg)...)
	actions = append(actions, NewReconcileActions(cfg)...)
	actions = append(actions, NewSeedAction(cfg),
		maintenance.NewReleasePrepare(rel), maintenance.NewReleasePublish(rel),
		maintenance.NewReleasePR(maintenance.PRDeps{Port: port, Provenance: prov}))
	m := jobs.NewManager(filesystem.NewJobJournal(journal), time.Now, newJobID)
	s := maintenance.NewService(m, actions, []byte("secret"), time.Now, maintenance.WithPending(NewPendingDeps(cfg)))
	return s, m, cfg, port
}

func runWebsite(t *testing.T, s *maintenance.Service, m *jobs.Manager) jobs.Job {
	t.Helper()
	ctx := context.Background()
	info, ok := s.Action("update-website")
	require.True(t, ok)
	require.True(t, info.Available, info.UnavailableReason)
	_, tok, err := s.Preview(ctx, "update-website", maintenance.Params{})
	require.NoError(t, err)
	j, err := s.Run(ctx, "update-website", maintenance.Params{}, tok, "update website")
	require.NoError(t, err)
	m.Wait(j.ID)
	got, _ := m.Get(j.ID)
	return got
}

func TestUpdateWebsiteForASpotifyIDEditReachesClean(t *testing.T) {
	s, m, cfg, port := websiteService(t)
	migrateDB(t, cfg.DBPath)
	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)

	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	edited := strings.Replace(string(body), "spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU", 1)
	require.NotEqual(t, string(body), edited)
	require.NoError(t, os.WriteFile(cfg.CatalogPath, []byte(edited), 0o600))
	v, err := s.Pending(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.PendingChanges, v.State)

	j := runWebsite(t, s, m)
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%s %+v", j.Message, j.Result)
	require.Equal(t, 1, port.opened)

	v, err = s.Pending(context.Background())
	require.NoError(t, err)
	require.Equal(t, maintenance.PendingClean, v.State, "%+v", v.Stages)
}

func TestStandaloneSeedRefusesWhileTheBulkReconcileHasChanges(t *testing.T) {
	s, _, cfg, _ := websiteService(t)
	migrateDB(t, cfg.DBPath)
	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	// Synced releases make the edited mapping a reconcile change.
	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.CatalogPath,
		[]byte(strings.Replace(string(body), "spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU", 1)), 0o600))
	plan, err := reconcilePort{cfg}.PlanAll(context.Background())
	require.NoError(t, err)
	if len(plan.Changes()) == 0 {
		t.Fatal("the fixture must give the bulk reconcile a change; the test would pass vacuously")
	}
	info, ok := s.Action("db-seed")
	require.True(t, ok)
	require.True(t, info.Available, "listing never gates the seed on the bulk plan")
	_, tok, err := s.Preview(context.Background(), "db-seed", maintenance.Params{})
	require.NoError(t, err)
	_, err = s.Run(context.Background(), "db-seed", maintenance.Params{}, tok, "")
	require.ErrorContains(t, err, "Reconcile the artists first")
}
