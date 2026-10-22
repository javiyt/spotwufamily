package maintenance

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type fakeSeedPort struct {
	log *callLog
	cat catalog.EditorialCatalog
	err error
}

func (p *fakeSeedPort) Seed(context.Context) (artists.AppliedCatalog, error) {
	p.log.add("seed")
	if p.err != nil {
		return artists.AppliedCatalog{}, p.err
	}
	return artists.FingerprintCatalog(p.cat), nil
}

func seedFixture(applied, current catalog.EditorialCatalog) (SeedDeps, *fakeProvenance, *callLog) {
	log := &callLog{}
	prov := newFakeProvenance()
	a := artists.FingerprintCatalog(applied)
	prov.applied = &a
	digest := "d1"
	return SeedDeps{
		Port:           &fakeSeedPort{log: log, cat: current},
		DatabaseExists: func() bool { return true },
		Digest:         func(context.Context) (string, error) { return digest, nil },
		Locker:         &fakeLocker{log: log},
		Backuper:       &fakeBackuper{log: log},
		Provenance:     prov,
		DatabaseName:   "catalog.db",
	}, prov, log
}

func TestSeedRecordsOnlyChangesNeedingNoSync(t *testing.T) {
	current := provCatalog(provArtist("gza", "id-gza", "id-new"), provArtist("rza", "id-rza"))
	current.Artists[1].Name = "RZA renamed"
	deps, prov, log := seedFixture(baseCatalog(), current)
	_, out, _, err := runAction(t, NewSeed(deps), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Summary == "" {
		t.Fatal("no summary")
	}
	if got := log.get(); !slices.Equal(got, []string{"lock", "backup", "seed", "release"}) {
		t.Fatalf("calls %v", got)
	}
	changes := artists.DiffCatalog(*prov.applied, artists.FingerprintCatalog(current))
	if len(changes) != 1 || changes[0].Slug != "gza" || !changes[0].NeedsSync {
		t.Fatalf("still pending after seed: %+v", changes)
	}
	if _, ok := prov.checkpoints[artists.StageSeed]; !ok {
		t.Fatal("seed checkpoint missing")
	}
}

func TestSeedWithoutBaselineRecordsNothing(t *testing.T) {
	deps, prov, _ := seedFixture(baseCatalog(), metadataEdited())
	prov.applied = nil
	if _, _, _, err := runAction(t, NewSeed(deps), Params{}); err != nil {
		t.Fatal(err)
	}
	prov.assertNothing(t)
}

func TestSeedRefusesWithoutConfirmationAndFailsClosed(t *testing.T) {
	deps, prov, _ := seedFixture(baseCatalog(), metadataEdited())
	if _, err := NewSeed(deps).Spec(context.Background(), Params{}); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("spec without confirmation: %v", err)
	}
	deps.Port.(*fakeSeedPort).err = errors.New("save configured artists: /secret/path")
	_, _, _, err := runAction(t, NewSeed(deps), Params{})
	if err == nil || Safe(err).Error() == "" {
		t.Fatalf("seed failure: %v", err)
	}
	prov.assertNothing(t)
}

func TestReleasePRCheckpointsTheOpenedPullRequest(t *testing.T) {
	port := readyPRPort()
	prov := newFakeProvenance()
	deps := prDeps(port, nil)
	deps.Provenance = prov
	if _, _, _, err := runAction(t, NewReleasePR(deps), Params{}); err != nil {
		t.Fatal(err)
	}
	cp, ok := prov.checkpoints[artists.StagePR]
	if !ok || cp.CatalogDigest != prCatalogDigest || cp.DBDigest != prov.db {
		t.Fatalf("checkpoint %+v %v", cp, ok)
	}

	failing := readyPRPort()
	failing.openErr = ErrPRGit
	prov2 := newFakeProvenance()
	deps2 := prDeps(failing, nil)
	deps2.Provenance = prov2
	if _, _, _, err := runAction(t, NewReleasePR(deps2), Params{}); err == nil {
		t.Fatal("failure not reported")
	}
	prov2.assertNothing(t)
}

func TestSeedRefusesAtSpecUntilTheArtistsAreReconciled(t *testing.T) {
	deps, _, log := seedFixture(baseCatalog(), metadataEdited())
	var plans atomic.Int32
	unreconciled := []string{"gza"}
	deps.Unreconciled = func(context.Context) ([]string, error) { plans.Add(1); return unreconciled, nil }
	a := NewSeed(deps)
	// Listing and availability never run the bulk plan.
	if ok, _ := a.Available(); !ok {
		t.Fatal("seed availability must not depend on the bulk plan")
	}
	if ok, _ := a.(QuickAvailability).AvailableNow(); !ok {
		t.Fatal("quick availability")
	}
	s := NewService(jobs.NewManager(&memStore{}, time.Now, func() string { return "j" }), []Action{a}, []byte("s"), time.Now)
	_ = s.Actions()
	if n := plans.Load(); n != 0 {
		t.Fatalf("listing ran the bulk plan %d times", n)
	}
	_, err := a.Spec(confirmedCtx(t, a, Params{}), Params{})
	var se *SafeError
	if !errors.As(err, &se) || se.Message != seedUnreconciledReason+"." {
		t.Fatalf("spec while artists are not reconciled: %v", err)
	}
	if len(log.get()) != 0 {
		t.Fatalf("calls %v", log.get())
	}
	// A plan error has its own reason, not "reconcile first".
	deps.Unreconciled = func(context.Context) ([]string, error) { return nil, errors.New("database is locked (SQLITE_BUSY)") }
	_, err = NewSeed(deps).Spec(confirmedCtx(t, NewSeed(deps), Params{}), Params{})
	if !errors.As(err, &se) || se.Message != seedCheckFailedReason+"." {
		t.Fatalf("plan error: %v", err)
	}
	unreconciled = nil
	deps.Unreconciled = func(context.Context) ([]string, error) { return unreconciled, nil }
	if _, _, _, err := runAction(t, NewSeed(deps), Params{}); err != nil {
		t.Fatalf("seed with nothing to reconcile: %v", err)
	}
}

func TestSeedRecordsAnArtistRemovedWithoutIDs(t *testing.T) {
	applied := provCatalog(provArtist("gza", "id-gza"), catalog.Artist{Slug: "old", Name: "old", Enabled: true})
	deps, prov, _ := seedFixture(applied, provCatalog(provArtist("gza", "id-gza")))
	if _, _, _, err := runAction(t, NewSeed(deps), Params{}); err != nil {
		t.Fatal(err)
	}
	if changes := artists.DiffCatalog(*prov.applied, artists.FingerprintCatalog(provCatalog(provArtist("gza", "id-gza")))); len(changes) != 0 {
		t.Fatalf("still pending: %+v", changes)
	}
}
