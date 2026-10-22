package artists

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// BulkReconcileStore applies the reconciliations of several artists at once.
type BulkReconcileStore interface {
	ReconcileStore
	// ApplyReconciliations applies every request, in order, in one database
	// transaction: either all of them land or none does. A failure while
	// applying a request is a *BulkApplyError naming its artist; a failed
	// commit is a *BulkApplyError without one.
	ApplyReconciliations(ctx context.Context, requests []ApplyRequest) error
	// ClearProvenance forgets the applied catalog and every stage
	// checkpoint, so the database reads as unknown until it is checked.
	ClearProvenance(ctx context.Context) error
}

// BulkApplyError is a bulk apply that failed and was rolled back.
type BulkApplyError struct {
	// Slug is the artist being applied when it failed; empty when every
	// artist was applied but the transaction could not be committed.
	Slug string
	Err  error
}

func (e *BulkApplyError) Error() string {
	if e.Slug == "" {
		return fmt.Sprintf("commit bulk reconciliation: %v", e.Err)
	}
	return fmt.Sprintf("apply reconciliation of %s: %v", e.Slug, e.Err)
}

func (e *BulkApplyError) Unwrap() error { return e.Err }

// ErrBulkPlanChanged refuses a bulk apply whose plan is not the reviewed one.
var ErrBulkPlanChanged = errors.New("bulk reconcile plan changed since review")

// BulkPlan is the write-free reconciliation plan of every catalog artist.
type BulkPlan struct {
	// Reports holds one plan per catalog artist, sorted by slug.
	Reports []ReconcileReport
	// Catalog fingerprints the editorial catalog the plans were computed from.
	Catalog AppliedCatalog
	// Checksum identifies the reviewed bulk plan (BulkPlanChecksum).
	Checksum string
}

// Changes lists the plans that would change something, sorted by slug.
func (p BulkPlan) Changes() []ReconcileReport {
	var out []ReconcileReport
	for _, r := range p.Reports {
		if r.NeedsChanges() {
			out = append(out, r)
		}
	}
	return out
}

// BulkPlanChecksum hashes the (slug, plan checksum) pairs of the artists that
// need changes, sorted by slug, together with the catalog digest. Artists
// that are already reconciled do not take part: their plans apply nothing.
func BulkPlanChecksum(changes []ReconcileReport, catalogDigest string) string {
	type pair struct {
		Slug     string `json:"slug"`
		Checksum string `json:"plan_checksum"`
	}
	pairs := make([]pair, 0, len(changes))
	for _, r := range changes {
		pairs = append(pairs, pair{Slug: r.Slug, Checksum: r.PlanChecksum})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Slug < pairs[j].Slug })
	data, _ := json.Marshal(struct {
		Catalog string `json:"catalog"`
		Plans   []pair `json:"plans"`
	}{catalogDigest, pairs})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReconcileAllInput is one reviewed bulk apply.
type ReconcileAllInput struct {
	CatalogPath string
	// ConfirmChecksum must equal the reviewed BulkPlan.Checksum.
	ConfirmChecksum string
	// BackupDir receives the single pre-change backup of the run.
	BackupDir string
	Now       time.Time
}

// ReconcileAll reconciles every catalog artist at once, all or nothing.
type ReconcileAll struct {
	catalog CatalogStore
	store   BulkReconcileStore
}

func NewReconcileAll(catalog CatalogStore, store BulkReconcileStore) ReconcileAll {
	return ReconcileAll{catalog: catalog, store: store}
}

// bulkArtist is a catalog artist with its plan.
type bulkArtist struct {
	report ReconcileReport
	artist *editorialArtist
}

func (r ReconcileAll) plan(ctx context.Context, catalogPath string) (BulkPlan, []bulkArtist, error) {
	c, err := r.catalog.Load(ctx, catalogPath)
	if err != nil {
		return BulkPlan{}, nil, fmt.Errorf("load artist catalog: %w", err)
	}
	plan := BulkPlan{Catalog: FingerprintCatalog(c)}
	single := ReconcileArtist{catalog: r.catalog, store: r.store}
	var planned []bulkArtist
	for _, a := range c.Artists {
		artist := newEditorialArtist(c, a)
		report, err := single.plan(ctx, a.Slug, artist)
		if err != nil {
			return BulkPlan{}, nil, fmt.Errorf("plan %s: %w", a.Slug, err)
		}
		report.Catalog = plan.Catalog
		planned = append(planned, bulkArtist{report: report, artist: artist})
	}
	sort.Slice(planned, func(i, j int) bool { return planned[i].report.Slug < planned[j].report.Slug })
	for _, p := range planned {
		plan.Reports = append(plan.Reports, p.report)
	}
	plan.Checksum = BulkPlanChecksum(plan.Changes(), plan.Catalog.Digest)
	return plan, planned, nil
}

// Plan computes the plan of every catalog artist without writing anything.
func (r ReconcileAll) Plan(ctx context.Context, catalogPath string) (BulkPlan, error) {
	plan, _, err := r.plan(ctx, catalogPath)
	return plan, err
}

// Apply recomputes the bulk plan and refuses one other than the confirmed
// checksum. When something needs changing it writes one backup and manifest
// and applies every artist's plan in one transaction. It returns the reports
// of the artists that needed changes, in slug order: applied on success,
// still dry-run plans on failure. A failed apply was rolled back and clears
// provenance. A clean plan takes no backup and returns no report.
func (r ReconcileAll) Apply(ctx context.Context, in ReconcileAllInput) ([]ReconcileReport, error) {
	plan, planned, err := r.plan(ctx, in.CatalogPath)
	if err != nil {
		return nil, err
	}
	if in.ConfirmChecksum != plan.Checksum {
		return plan.Changes(), ErrBulkPlanChanged
	}
	var changes []bulkArtist
	for _, p := range planned {
		if p.report.NeedsChanges() {
			changes = append(changes, p)
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	reports := make([]ReconcileReport, len(changes))
	for i, c := range changes {
		reports[i] = c.report
	}
	if in.BackupDir == "" {
		return reports, fmt.Errorf("apply requires a backup directory")
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	token := now.UTC().Format(time.RFC3339)
	manifest, err := json.MarshalIndent(struct {
		Checksum string            `json:"bulk_plan_checksum"`
		Plans    []ReconcileReport `json:"plans"`
	}{plan.Checksum, reports}, "", "  ")
	if err != nil {
		return reports, fmt.Errorf("encode plan manifest: %w", err)
	}
	backup, err := r.store.BackupDatabase(ctx, BackupRequest{
		Dir:      in.BackupDir,
		Name:     backupName("all-artists", token),
		Manifest: append(manifest, '\n'),
	})
	if err != nil {
		return reports, fmt.Errorf("backup before reconcile: %w", err)
	}

	requests := make([]ApplyRequest, len(changes))
	for i, c := range changes {
		requests[i] = applyRequest(c.report, c.artist, token, backup)
	}
	if err := r.store.ApplyReconciliations(ctx, requests); err != nil {
		// The transaction rolled back: the database is as before the run.
		// The recorded provenance is dropped all the same, so the database
		// reads as unknown until it is checked again.
		if clearErr := r.store.ClearProvenance(context.WithoutCancel(ctx)); clearErr != nil {
			err = errors.Join(err, fmt.Errorf("clear provenance: %w", clearErr))
		}
		return reports, err
	}
	for i := range reports {
		markApplied(&reports[i], requests[i], backup)
	}
	return reports, nil
}
