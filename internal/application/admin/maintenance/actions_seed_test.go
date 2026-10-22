package maintenance

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/artists"
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

func TestSeedIsUnavailableUntilTheArtistsAreReconciled(t *testing.T) {
	deps, _, log := seedFixture(baseCatalog(), metadataEdited())
	deps.Unreconciled = func(context.Context) ([]string, error) { return []string{"gza"}, nil }
	a := NewSeed(deps)
	ok, reason := a.Available()
	if ok || reason != seedUnreconciledReason {
		t.Fatalf("available %v %q", ok, reason)
	}
	if _, err := a.Spec(confirmedCtx(t, a, Params{}), Params{}); err == nil {
		t.Fatal("spec accepted while artists are not reconciled")
	}
	if len(log.get()) != 0 {
		t.Fatalf("calls %v", log.get())
	}
	deps.Unreconciled = func(context.Context) ([]string, error) { return nil, nil }
	if ok, _ := NewSeed(deps).Available(); !ok {
		t.Fatal("seed unavailable with nothing to reconcile")
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
