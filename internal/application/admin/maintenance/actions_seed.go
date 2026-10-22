package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// SeedPort writes the editorial catalog into the existing database without
// contacting Spotify: every artist's configured row, aliases and Spotify IDs
// (as the CLI's artists seed-db does, and as a sync does before it fetches).
// It migrates the database first, refuses a catalog that does not validate
// and returns the fingerprint of the catalog it loaded.
type SeedPort interface {
	Seed(ctx context.Context) (artists.AppliedCatalog, error)
}

// SeedDeps are the ports of db-seed.
type SeedDeps struct {
	Port           SeedPort
	DatabaseExists func() bool
	// Digest covers the editorial catalog the seed reads.
	Digest   CatalogDigest
	Locker   Locker
	Backuper Backuper
	// Provenance, when set, records the artists the seed fully applied.
	Provenance Provenance
	// Unreconciled lists the artists whose bulk reconcile plan would still
	// change something; the seed is refused while there are any, so it
	// never writes new Spotify IDs before their reconcile. Nil skips the
	// check.
	Unreconciled func(ctx context.Context) ([]string, error)
	DatabaseName string
}

type seedAction struct{ deps SeedDeps }

// NewSeed returns db-seed: under the cross-process database lock it backs
// the database up and writes every catalog artist into it, with no Spotify
// call. It records as applied only the artists whose pending change needs
// neither a reconcile nor a sync (metadata, relationships, artists added
// without Spotify IDs); Spotify ID, verified or enabled changes stay
// pending until their reconcile and sync.
func NewSeed(d SeedDeps) Action { return &seedAction{deps: d} }

func (a *seedAction) Name() string { return "db-seed" }

func (a *seedAction) Describe() Description {
	return Description{Title: "Seed database from the catalog"}
}

// seedUnreconciledReason is why db-seed waits for the bulk reconcile.
const seedUnreconciledReason = "Reconcile the artists first: some Spotify ID, verified or enabled changes are not reconciled yet"

func (a *seedAction) Available() (bool, string) {
	if !a.deps.DatabaseExists() {
		return false, noDatabaseReason
	}
	ctx, cancel := context.WithTimeout(context.Background(), availabilityTimeout)
	defer cancel()
	if err := a.reconciled(ctx); err != nil {
		var se *SafeError
		if errors.As(err, &se) {
			return false, strings.TrimSuffix(se.Message, ".")
		}
		return false, seedUnreconciledReason
	}
	return true, ""
}

// reconciled fails while the bulk reconcile still has changes to make.
func (a *seedAction) reconciled(ctx context.Context) error {
	if a.deps.Unreconciled == nil {
		return nil
	}
	slugs, err := a.deps.Unreconciled(ctx)
	if err != nil {
		return err
	}
	if len(slugs) > 0 {
		return &SafeError{Code: "unavailable", Message: seedUnreconciledReason + "."}
	}
	return nil
}

func (a *seedAction) Validate(p Params) error { return RejectUnknown(p) }

func (a *seedAction) fingerprint(ctx context.Context) (string, error) {
	if !a.deps.DatabaseExists() {
		return "", ErrNoDatabase
	}
	digest, err := a.deps.Digest(ctx)
	if err != nil {
		return "", err
	}
	return fingerprint(a.Name(), digest), nil
}

func (a *seedAction) Preview(ctx context.Context, _ Params) (Preview, error) {
	fp, err := a.fingerprint(ctx)
	if err != nil {
		return Preview{}, err
	}
	return Preview{
		Title:       a.Describe().Title,
		Fingerprint: fp,
		Scope: []ScopeLine{
			{Label: "Steps", Value: "write every catalog artist (configured row, aliases and Spotify IDs) into the database; no Spotify call"},
			{Label: "Writes", Value: a.deps.DatabaseName + "; the database is backed up first"},
		},
		Warnings: []string{"Spotify ID, verified or enabled changes still need their reconcile and sync to be applied."},
	}, nil
}

// Spec binds the job to the verified preview fingerprint (fails closed
// without one); the job re-checks it once the lock is held.
func (a *seedAction) Spec(ctx context.Context, _ Params) (jobs.Spec, error) {
	verified, err := verifiedFingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	fp, err := a.fingerprint(ctx)
	if err != nil {
		return jobs.Spec{}, err
	}
	if fp != verified {
		return jobs.Spec{}, ErrStalePreview
	}
	if err := a.reconciled(ctx); err != nil {
		return jobs.Spec{}, Safe(err)
	}
	return jobs.Spec{
		Kind:      a.Name(),
		Resources: []string{ResourceDatabase},
		Run: func(ctx context.Context, r jobs.Reporter) (jobs.Outcome, error) {
			return a.run(ctx, r, fp)
		},
	}, nil
}

func (a *seedAction) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Safe(err)
}

func (a *seedAction) run(ctx context.Context, r jobs.Reporter, fp string) (jobs.Outcome, error) {
	r.Step("locking database")
	release, err := a.deps.Locker.TryLock()
	if err != nil {
		return jobs.Outcome{}, Safe(err)
	}
	defer release()

	r.Step("checking preview")
	current, err := a.fingerprint(ctx)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	if current != fp {
		return jobs.Outcome{}, Safe(ErrStalePreview)
	}
	if err := a.reconciled(ctx); err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	r.Step("backing up database")
	if err := a.deps.Backuper.Backup(ctx); err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	// Last point where a cancel leaves everything untouched.
	if err := ctx.Err(); err != nil {
		return jobs.Outcome{}, err
	}
	r.Step("seeding")
	used, err := a.deps.Port.Seed(ctx)
	if err != nil {
		return jobs.Outcome{}, a.fail(ctx, err)
	}
	summary := fmt.Sprintf("Seeded %s from the catalog; no Spotify call was made.", plural(len(used.Artists), "artist"))
	// The seed committed: recording what it applied is not cancellable.
	if err := recordSeeded(context.WithoutCancel(ctx), a.deps.Provenance, used); err != nil {
		summary += provenanceWarning
	}
	return jobs.Outcome{Status: jobs.StatusSucceeded, Result: jobs.Result{Summary: summary}}, nil
}

// recordSeeded merges into the applied record the artists whose change the
// seed fully applied: those needing neither a reconcile nor a sync. Without
// a previous record nothing is recorded (a seed cannot vouch for Spotify
// state), and artists with a pending reconciliation are skipped by
// mergeApplied.
func recordSeeded(ctx context.Context, p Provenance, used artists.AppliedCatalog) error {
	if p == nil || used.Digest == "" {
		return nil
	}
	prev, ok, err := p.AppliedCatalog(ctx)
	if err != nil || !ok {
		return err
	}
	var runs []appliedRun
	for _, c := range artists.DiffCatalog(prev, used) {
		if !seedApplies(c) {
			continue
		}
		runs = append(runs, appliedRun{slug: c.Slug, used: used})
	}
	return mergeApplied(ctx, p, artists.StageSeed, runs, mergeSynced)
}
