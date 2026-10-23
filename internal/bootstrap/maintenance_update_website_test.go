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
	sqliteadapter "github.com/javiyt/spotwufamily/internal/adapters/outbound/sqlite"
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

// configuredRow reads what the export reads for an artist.
func configuredRow(t *testing.T, dbPath, slug string) (string, int, []string) {
	t.Helper()
	database, err := sqliteadapter.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	var sid string
	var enabled int
	require.NoError(t, database.DB().QueryRow(
		`SELECT COALESCE(spotify_id, ''), enabled FROM configured_artists WHERE slug = ?`, slug).Scan(&sid, &enabled))
	rows, err := database.DB().Query(
		`SELECT spotify_id FROM configured_artist_spotify_ids WHERE artist_slug = ? ORDER BY position`, slug)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return sid, enabled, ids
}

// websiteEdit runs update-website after a baseline full sync and an edit
// of the catalog, and returns the job and the pending state after it.
func websiteEdit(t *testing.T, edit func(string) string) (jobs.Job, maintenance.PendingView, MaintenanceActionsConfig) {
	t.Helper()
	return websiteEditFrom(t, "", edit)
}

// websiteEditFrom starts from catalogYAML ("" keeps the fixture).
func websiteEditFrom(t *testing.T, catalogYAML string, edits ...func(string) string) (jobs.Job, maintenance.PendingView, MaintenanceActionsConfig) {
	t.Helper()
	s, m, cfg, _ := websiteService(t)
	if catalogYAML != "" {
		require.NoError(t, os.WriteFile(cfg.CatalogPath, []byte(catalogYAML), 0o600))
	}
	migrateDB(t, cfg.DBPath)
	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	var j jobs.Job
	for i, edit := range edits {
		if i > 0 {
			// Reconcile backups are named to the second: a second bulk
			// reconcile within the same second refuses to overwrite one.
			time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
		}
		body, err := os.ReadFile(cfg.CatalogPath)
		require.NoError(t, err)
		edited := edit(string(body))
		require.NotEqual(t, string(body), edited)
		require.NoError(t, os.WriteFile(cfg.CatalogPath, []byte(edited), 0o600))
		j = runWebsite(t, s, m)
		require.Equal(t, jobs.StatusSucceeded, j.Status, "%s", j.Message)
	}
	v, err := s.Pending(context.Background())
	require.NoError(t, err)
	return j, v, cfg
}

func TestUpdateWebsiteNeverPublishesADatabaseThatDisagreesWithTheCatalog(t *testing.T) {
	j, v, cfg := websiteEditLenient(t, func(body string) string {
		body = strings.Replace(body, "spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU", 1)
		return strings.Replace(body, "enabled: true", "enabled: false", 1)
	})
	sid, enabled, ids := configuredRow(t, cfg.DBPath, "wu-tang-clan")
	matches := sid == "4gzpq5DPGxSnKTe4SA8HAU" && enabled == 0 && len(ids) == 1 && ids[0] == "4gzpq5DPGxSnKTe4SA8HAU"
	if !matches {
		require.NotEqual(t, jobs.StatusSucceeded, j.Status, "succeeded with spotify_id=%s enabled=%d ids=%v", sid, enabled, ids)
		require.NotEqual(t, maintenance.PendingClean, v.State)
		return
	}
	require.Equal(t, jobs.StatusSucceeded, j.Status, "%s", j.Message)
	require.Equal(t, maintenance.PendingClean, v.State, "%+v", v.Stages)
}

func TestUpdateWebsiteConvergesForEveryKindOfEdit(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) string
		sid  string
		on   int
	}{
		{"spotify id change", func(b string) string {
			return strings.Replace(b, "spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU", 1)
		}, "4gzpq5DPGxSnKTe4SA8HAU", 1},
		{"disabled toggle", func(b string) string { return strings.Replace(b, "enabled: true", "enabled: false", 1) },
			"34EP7KEpOjXcM2TCat1ISk", 0},
		{"metadata only", func(b string) string { return strings.Replace(b, `notes: ""`, `notes: "edited"`, 1) },
			"34EP7KEpOjXcM2TCat1ISk", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, v, cfg := websiteEdit(t, tc.edit)
			require.Equal(t, jobs.StatusSucceeded, j.Status, "%s", j.Message)
			require.Equal(t, maintenance.PendingClean, v.State, "%+v", v.Stages)
			sid, enabled, ids := configuredRow(t, cfg.DBPath, "wu-tang-clan")
			require.Equal(t, tc.sid, sid)
			require.Equal(t, tc.on, enabled)
			require.Equal(t, []string{tc.sid}, ids)
		})
	}
}

// websiteEditLenient is websiteEdit without requiring the run to succeed.
func websiteEditLenient(t *testing.T, edit func(string) string) (jobs.Job, maintenance.PendingView, MaintenanceActionsConfig) {
	t.Helper()
	s, m, cfg, _ := websiteService(t)
	migrateDB(t, cfg.DBPath)
	out, err := runSpec(t, syncActionsByName(cfg)["sync"], maintenance.Params{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, jobs.StatusSucceeded, out.Status, out.Result.Summary)
	body, err := os.ReadFile(cfg.CatalogPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.CatalogPath, []byte(edit(string(body))), 0o600))
	j := runWebsite(t, s, m)
	v, err := s.Pending(context.Background())
	require.NoError(t, err)
	return j, v, cfg
}

const twoArtistCatalogYAML = maintenanceCatalogYAML + `  - slug: gza
    name: GZA
    spotify_id: 6UZjVTlh5AgyGUAcwS3Kny
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 2
    notes: ""
`

func TestUpdateWebsiteConvergesForARevertedIDSetAndARemovedArtist(t *testing.T) {
	t.Run("reverted id set", func(t *testing.T) {
		to := func(b string) string {
			return strings.Replace(b, "spotify_id: 34EP7KEpOjXcM2TCat1ISk", "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU", 1)
		}
		back := func(b string) string {
			return strings.Replace(b, "spotify_id: 4gzpq5DPGxSnKTe4SA8HAU", "spotify_id: 34EP7KEpOjXcM2TCat1ISk", 1)
		}
		_, v, cfg := websiteEditFrom(t, "", to, back)
		require.Equal(t, maintenance.PendingClean, v.State, "%+v", v.Stages)
		sid, _, ids := configuredRow(t, cfg.DBPath, "wu-tang-clan")
		require.Equal(t, "34EP7KEpOjXcM2TCat1ISk", sid)
		require.Equal(t, []string{"34EP7KEpOjXcM2TCat1ISk"}, ids)
	})
	t.Run("removed artist", func(t *testing.T) {
		remove := func(b string) string { return b[:strings.Index(b, "  - slug: gza")] }
		_, v, cfg := websiteEditFrom(t, twoArtistCatalogYAML, remove)
		require.Equal(t, maintenance.PendingClean, v.State, "%+v", v.Stages)
		database, err := sqliteadapter.Open(cfg.DBPath)
		require.NoError(t, err)
		defer func() { require.NoError(t, database.Close()) }()
		var active int
		require.NoError(t, database.DB().QueryRow(`SELECT active FROM configured_artists WHERE slug = 'gza'`).Scan(&active))
		require.Equal(t, 0, active)
	})
}

func TestPreparedSidecarIsReadBackAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	s := journalPrepared{path: filepath.Join(dir, "admin", "jobs.jsonl")}
	require.NoError(t, s.RecordPrepared(localSHA))
	sha, ok := s.LastPrepared()
	require.True(t, ok)
	require.Equal(t, localSHA, sha)
	entries, err := os.ReadDir(filepath.Join(dir, "admin"))
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasSuffix(e.Name(), ".tmp"), e.Name())
	}
	require.Error(t, s.RecordPrepared("not-a-hash"))
}
