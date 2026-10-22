package maintenance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/catalogaudit"
	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// fakeProvenance is an in-memory Provenance.
type fakeProvenance struct {
	mu          sync.Mutex
	applied     *artists.AppliedCatalog
	checkpoints map[string]artists.StageCheckpoint
	appliedN    int
	db          string
	catalog     string
	pending     []string
	recordErr   error
}

func newFakeProvenance() *fakeProvenance {
	return &fakeProvenance{checkpoints: map[string]artists.StageCheckpoint{}, db: "db-1", catalog: "cat-now"}
}

func (f *fakeProvenance) AppliedCatalog(context.Context) (artists.AppliedCatalog, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applied == nil {
		return artists.AppliedCatalog{}, false, nil
	}
	return *f.applied, true, nil
}

func (f *fakeProvenance) RecordApplied(_ context.Context, a artists.AppliedCatalog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.applied = &a
	f.appliedN++
	return nil
}

func (f *fakeProvenance) Checkpoint(_ context.Context, stage string) (artists.StageCheckpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp, ok := f.checkpoints[stage]
	return cp, ok, nil
}

func (f *fakeProvenance) RecordCheckpoint(_ context.Context, cp artists.StageCheckpoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.checkpoints[cp.Stage] = cp
	return nil
}

func (f *fakeProvenance) PendingReconciliations(context.Context) ([]string, error) {
	return f.pending, nil
}

func (f *fakeProvenance) DatabaseDigest(context.Context) (string, error) { return f.db, nil }
func (f *fakeProvenance) CatalogDigest(context.Context) (string, error)  { return f.catalog, nil }

func (f *fakeProvenance) assertNothing(t *testing.T) {
	t.Helper()
	if f.appliedN != 0 || len(f.checkpoints) != 0 {
		t.Fatalf("nothing may be recorded: applied=%d checkpoints=%v", f.appliedN, f.checkpoints)
	}
}

func provArtist(slug string, ids ...string) catalog.Artist {
	profiles := make([]catalog.SpotifyProfile, 0, len(ids))
	for _, id := range ids {
		profiles = append(profiles, catalog.SpotifyProfile{ID: id, Status: catalog.ProfileVerified})
	}
	return catalog.Artist{Slug: slug, Name: slug, SpotifyIDs: ids, SpotifyProfiles: profiles, Enabled: true}
}

func provCatalog(artists ...catalog.Artist) catalog.EditorialCatalog {
	return catalog.EditorialCatalog{Version: 1, Artists: artists}
}

func fingerprintOf(c catalog.EditorialCatalog) *artists.AppliedCatalog {
	fp := artists.FingerprintCatalog(c)
	return &fp
}

// --- sync ---

func TestSyncAllSuccessRecordsCatalogItUsed(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	used := fingerprintOf(provCatalog(provArtist("gza", "g1"), provArtist("rza", "r1")))
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 2, ArtistsProcessed: 2}, Catalog: used}

	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	if prov.applied == nil || prov.applied.Digest != used.Digest || prov.applied.AppliedAt == "" {
		t.Fatalf("a whole-catalog sync records the catalog it read: %+v", prov.applied)
	}
	cp := prov.checkpoints[artists.StageSync]
	if cp.CatalogDigest != used.Digest || cp.DBDigest != "db-1" || cp.At == "" {
		t.Fatalf("sync checkpoint: %+v", cp)
	}
	if strings.Contains(out.Result.Summary, "could not be recorded") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestSyncArtistMergesOnlyThatArtist(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	prev := fingerprintOf(provCatalog(provArtist("gza", "g1"), provArtist("rza", "r1")))
	prov.applied = prev
	// The run read a catalog where both artists changed; only gza was synced.
	used := fingerprintOf(provCatalog(provArtist("gza", "g1", "g2"), provArtist("rza", "r1", "r2")))
	fx.runner.results["gza"] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsProcessed: 1}, Catalog: used}

	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "artist", "artists": "gza"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	changes := artists.DiffCatalog(*prov.applied, *used)
	if len(changes) != 1 || changes[0].Slug != "rza" || !changes[0].NeedsSync {
		t.Fatalf("rza must still need a sync after syncing only gza: %+v", changes)
	}
	if cp := prov.checkpoints[artists.StageSync]; cp.CatalogDigest != prov.applied.Digest {
		t.Fatalf("checkpoint must carry the merged digest: %+v vs %s", cp, prov.applied.Digest)
	}
}

func TestSyncArtistWithoutPreviousRecordStaysUnknown(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	used := fingerprintOf(provCatalog(provArtist("gza", "g1"), provArtist("rza", "r1")))
	fx.runner.results["gza"] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsProcessed: 1}, Catalog: used}

	if _, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "artist", "artists": "gza"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	prov.assertNothing(t)
}

func TestSyncUnsuccessfulRunsRecordNothing(t *testing.T) {
	used := fingerprintOf(provCatalog(provArtist("gza", "g1")))
	cases := map[string]func(fx *syncFixture){
		"rate limited": func(fx *syncFixture) {
			fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 2, ArtistsProcessed: 1, ArtistsFailed: 1, RunID: 2}, Catalog: used}
			fx.runner.errs[""] = &catalogsync.PartialSyncError{Err: retryAfterErr{time.Second}}
		},
		"artist failures": func(fx *syncFixture) {
			fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 2, ArtistsProcessed: 1, ArtistsFailed: 1}, Catalog: used}
			fx.runner.errs[""] = &catalogsync.ArtistSyncsFailedError{Count: 1}
		},
		"snapshot failed": func(fx *syncFixture) {
			fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsProcessed: 1}, Catalog: used, SnapshotErr: errors.New("disk full")}
		},
		"fatal": func(fx *syncFixture) {
			fx.runner.errs[""] = errors.New("begin sync run: disk I/O error")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newSyncFixture(true)
			prov := newFakeProvenance()
			fx.deps.Provenance = prov
			setup(fx)
			_, out, _, _ := runAction(t, NewSync(fx.deps), Params{"scope": "all"})
			if out.Status == jobs.StatusSucceeded {
				t.Fatalf("fixture must not succeed: %+v", out)
			}
			prov.assertNothing(t)
		})
	}
}

func TestSyncCancelledRecordsNothing(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	used := fingerprintOf(provCatalog(provArtist("gza", "g1"), provArtist("rza", "r1")))
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 2, ArtistsProcessed: 2}, Catalog: used}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.runner.onRun = func(string) { cancel() }

	spec, err := specFor(t, NewSync(fx.deps), Params{"scope": "all"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run(ctx, &recReporter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancelled, got %v", err)
	}
	prov.assertNothing(t)
}

func TestSyncDryRunRecordsNothing(t *testing.T) {
	fx := newSyncFixture(false)
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{DryRun: true, ArtistsPlanned: 1},
		Catalog: fingerprintOf(provCatalog(provArtist("gza", "g1")))}
	if _, out, _, err := runAction(t, NewSyncDryRun(fx.deps), Params{"scope": "all"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	prov.assertNothing(t)
}

func TestSyncRecordFailureKeepsSuccessWithWarning(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	prov.recordErr = errors.New("database is locked at /secret/catalog.db")
	fx.deps.Provenance = prov
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsProcessed: 1},
		Catalog: fingerprintOf(provCatalog(provArtist("gza", "g1")))}

	_, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("the sync itself succeeded: %+v %v", out, err)
	}
	if !strings.Contains(out.Result.Summary, "could not be recorded") || strings.Contains(out.Result.Summary, "/secret") {
		t.Fatalf("summary %q", out.Result.Summary)
	}
}

func TestSyncAllKeepsPendingReconciliationArtistsUnapplied(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	prov.pending = []string{"rza"}
	fx.deps.Provenance = prov
	prev := fingerprintOf(provCatalog(provArtist("gza", "g1"), provArtist("rza", "r1", "r2")))
	prov.applied = prev
	// rza lost a Spotify ID: the sync succeeds but its orphans wait for a reconcile.
	used := fingerprintOf(provCatalog(provArtist("gza", "g1", "g2"), provArtist("rza", "r1")))
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 2, ArtistsProcessed: 2}, Catalog: used}

	if _, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	changes := artists.DiffCatalog(*prov.applied, *used)
	if len(changes) != 1 || changes[0].Slug != "rza" || !changes[0].NeedsReconcile {
		t.Fatalf("rza must still need its reconcile, gza is applied: %+v", changes)
	}
	if cp := prov.checkpoints[artists.StageSync]; cp.CatalogDigest != prov.applied.Digest {
		t.Fatalf("checkpoint %+v", cp)
	}
}

func TestSyncAllWithoutPreviousRecordAndPendingStaysUnknown(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	prov.pending = []string{"rza"}
	fx.deps.Provenance = prov
	fx.runner.results[""] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 2, ArtistsProcessed: 2},
		Catalog: fingerprintOf(provCatalog(provArtist("gza", "g1"), provArtist("rza", "r1")))}

	if _, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "all"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	prov.assertNothing(t)
}

func TestSyncArtistWithPendingReconciliationRecordsNothing(t *testing.T) {
	fx := newSyncFixture(true)
	prov := newFakeProvenance()
	prov.pending = []string{"gza"}
	fx.deps.Provenance = prov
	prov.applied = fingerprintOf(provCatalog(provArtist("gza", "g1", "g2")))
	fx.runner.results["gza"] = SyncRunResult{Report: catalogsync.Report{ArtistsPlanned: 1, ArtistsProcessed: 1},
		Catalog: fingerprintOf(provCatalog(provArtist("gza", "g1")))}

	if _, out, _, err := runAction(t, NewSync(fx.deps), Params{"scope": "artist", "artists": "gza"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %+v %v", out, err)
	}
	prov.assertNothing(t)
}

// --- reconcile-apply ---

func reconcileApplied(fx *reconcileFixture, used artists.AppliedCatalog, resync bool) {
	fx.port.applyResult = func() (artists.ReconcileReport, error) {
		plan, err := fx.port.use.Run(context.Background(), artists.ReconcileInput{Slug: "gza"})
		plan.Backup = &artists.BackupInfo{Path: "/secret/backups/x.db"}
		plan.ResyncRequired = resync
		plan.Status = "applied"
		plan.Catalog = used
		return plan, err
	}
}

// Reconcile rewrites associations and sync state only, never the configured
// artists the export reads: it checkpoints itself and never records the
// catalog as applied (db-seed and sync do).
func TestReconcileApplyCheckpointsButNeverApplies(t *testing.T) {
	fx := newReconcileFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	prev := fingerprintOf(provCatalog(provArtist("gza", "old"), provArtist("rza", "r1")))
	prov.applied = prev
	edited := provCatalog(provArtist("gza", "new"), provArtist("rza", "r1", "r2"))
	used := artists.FingerprintCatalog(edited)
	reconcileApplied(fx, used, false)

	_, out, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"})
	if err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("apply: %+v %v", out, err)
	}
	if prov.appliedN != 0 || prov.applied != prev {
		t.Fatal("reconcile recorded the catalog as applied")
	}
	if ch := artists.DiffCatalog(*prov.applied, used); len(ch) == 0 || !ch[0].NeedsSync {
		t.Fatalf("gza must still need its seed/sync: %+v", ch)
	}
	if cp := prov.checkpoints[artists.StageReconcile]; cp.CatalogDigest != used.Digest || cp.DBDigest != "db-1" {
		t.Fatalf("reconcile checkpoint %+v", cp)
	}
}

func TestReconcileApplyAwaitingResyncRecordsNothing(t *testing.T) {
	fx := newReconcileFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	prov.applied = fingerprintOf(provCatalog(provArtist("gza", "old")))
	reconcileApplied(fx, artists.FingerprintCatalog(provCatalog(provArtist("gza", "new"))), true)

	if _, out, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("apply: %+v %v", out, err)
	}
	if prov.appliedN != 0 {
		t.Fatalf("an artist awaiting its resync is not applied: %d", prov.appliedN)
	}
}

func TestReconcileApplyStillPendingRecordsNothing(t *testing.T) {
	fx := newReconcileFixture()
	prov := newFakeProvenance()
	prov.pending = []string{"gza"}
	fx.deps.Provenance = prov
	prov.applied = fingerprintOf(provCatalog(provArtist("gza", "old")))
	reconcileApplied(fx, artists.FingerprintCatalog(provCatalog(provArtist("gza", "new"))), false)

	if _, out, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("apply: %+v %v", out, err)
	}
	if prov.appliedN != 0 {
		t.Fatalf("reconcile recorded the catalog as applied")
	}
}

func TestReconcileApplyFailureRecordsNothing(t *testing.T) {
	fx := newReconcileFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	prov.applied = fingerprintOf(provCatalog(provArtist("gza", "old")))
	fx.db.applyErr = errors.New("constraint failed")

	if _, _, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"}); err == nil {
		t.Fatal("apply must fail")
	}
	prov.assertNothing(t)
}

func TestReconcileApplyWithoutPreviousRecordStaysUnknown(t *testing.T) {
	fx := newReconcileFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	reconcileApplied(fx, artists.FingerprintCatalog(provCatalog(provArtist("gza", "new"))), false)

	if _, out, _, err := runAction(t, NewReconcileApply(fx.deps), Params{"artist": "gza"}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("apply: %+v %v", out, err)
	}
	if prov.appliedN != 0 {
		t.Fatalf("reconcile recorded the catalog as applied")
	}
}

func TestReconcileDryRunRecordsNothing(t *testing.T) {
	fx := newReconcileFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	prov.applied = fingerprintOf(provCatalog(provArtist("gza", "old")))
	if _, _, _, err := runAction(t, NewReconcileDryRun(fx.deps), Params{"artist": "gza"}); err != nil {
		t.Fatal(err)
	}
	prov.assertNothing(t)
}

// --- snapshot, export, refresh-export ---

func TestPrepStagesCheckpointButNeverApply(t *testing.T) {
	cases := map[string][]string{
		"db-snapshot":    {artists.StageSnapshot},
		"export":         {artists.StageExport},
		"refresh-export": {artists.StageSnapshot, artists.StageExport},
		"db-verify":      nil,
		"audit":          nil,
	}
	for name, stages := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newPrepFixture(t)
			prov := newFakeProvenance()
			fx.deps.Provenance = prov
			_, out, _, err := runAction(t, prepActions(fx.deps)[name], Params{})
			if err != nil || out.Status != jobs.StatusSucceeded {
				t.Fatalf("run: %+v %v", out, err)
			}
			if prov.appliedN != 0 {
				t.Fatal("snapshot and export must never record the catalog as applied")
			}
			if len(prov.checkpoints) != len(stages) {
				t.Fatalf("checkpoints %v, want %v", prov.checkpoints, stages)
			}
			for _, s := range stages {
				cp := prov.checkpoints[s]
				if cp.CatalogDigest != "cat-now" || cp.DBDigest != "db-1" {
					t.Fatalf("%s checkpoint %+v", s, cp)
				}
			}
		})
	}
}

func TestPrepFailedOrPartialRecordsNothing(t *testing.T) {
	cases := map[string]struct {
		action string
		setup  func(fx *prepFixture)
	}{
		"snapshot failed":                      {"db-snapshot", func(fx *prepFixture) { fx.snapshot.err = errors.New("disk full") }},
		"export failed":                        {"export", func(fx *prepFixture) { fx.export.err = errors.New("disk full") }},
		"refresh export failed after snapshot": {"refresh-export", func(fx *prepFixture) { fx.export.err = errors.New("disk full") }},
		"refresh audit failed (partial)": {"refresh-export", func(fx *prepFixture) {
			fx.audit.report = catalogaudit.Report{Checks: []catalogaudit.Check{{Name: "export", Status: catalogaudit.StatusStale}}}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newPrepFixture(t)
			prov := newFakeProvenance()
			fx.deps.Provenance = prov
			tc.setup(fx)
			_, out, _, _ := runAction(t, prepActions(fx.deps)[tc.action], Params{})
			if out.Status == jobs.StatusSucceeded {
				t.Fatalf("fixture must not succeed: %+v", out)
			}
			prov.assertNothing(t)
		})
	}
}

// --- release ---

func TestReleasePrepareAndPublishCheckpoint(t *testing.T) {
	fx := newReleaseFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	if _, out, _, err := runAction(t, NewReleasePrepare(fx.deps), Params{}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("prepare: %+v %v", out, err)
	}
	if cp, ok := prov.checkpoints[artists.StageReleasePrepare]; !ok || cp.DBDigest != "db-1" || cp.CatalogDigest != "cat-now" {
		t.Fatalf("prepare checkpoint %+v", prov.checkpoints)
	}
	if _, ok := prov.checkpoints[artists.StageRelease]; ok {
		t.Fatal("preparing is not publishing")
	}

	fx.prepared()
	if _, out, _, err := runAction(t, NewReleasePublish(fx.deps), Params{}); err != nil || out.Status != jobs.StatusSucceeded {
		t.Fatalf("publish: %+v %v", out, err)
	}
	if cp, ok := prov.checkpoints[artists.StageRelease]; !ok || cp.DBDigest != "db-1" {
		t.Fatalf("release checkpoint %+v", prov.checkpoints)
	}
	if prov.appliedN != 0 {
		t.Fatal("release must never record the catalog as applied")
	}
}

func TestReleasePublishFailureRecordsNothing(t *testing.T) {
	fx := newReleaseFixture()
	prov := newFakeProvenance()
	fx.deps.Provenance = prov
	fx.prepared()
	fx.releaser.pubErr = errors.New("gh: upload failed")
	if _, _, _, err := runAction(t, NewReleasePublish(fx.deps), Params{}); err == nil {
		t.Fatal("publish must fail")
	}
	prov.assertNothing(t)
}
