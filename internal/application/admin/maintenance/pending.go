package maintenance

import (
	"context"
	"errors"

	"github.com/javiyt/spotwufamily/internal/application/artists"
)

// PendingState summarises whether the website data is up to date.
type PendingState string

const (
	// PendingUnknown: no applied catalog is recorded (legacy database), or
	// the database or the catalog could not be read; never "up to date".
	PendingUnknown PendingState = "unknown"
	// PendingClean: every stage is done for the current catalog and
	// database digests.
	PendingClean PendingState = "clean"
	// PendingChanges: at least one stage is still to do.
	PendingChanges PendingState = "pending"
	// PendingOffline: there is no database.
	PendingOffline PendingState = "offline"
)

// StageNeed says whether a stage has to run for the current digests.
type StageNeed string

const (
	NeedRequired  StageNeed = "required"
	NeedOptional  StageNeed = "optional"
	NeedNotNeeded StageNeed = "not_needed"
	// NeedManual stages are not run by the admin (the local site build) or
	// only observed (the GitHub Pages deploy).
	NeedManual StageNeed = "manual"
)

// StageStatus is the state of one stage.
type StageStatus string

const (
	StageDone    StageStatus = "done"
	StageTodo    StageStatus = "todo"
	StageBlocked StageStatus = "blocked"
	StageUnknown StageStatus = "unknown"
)

// Stage names of the update-website plan, in run order.
const (
	PlanValidate       = "validate"
	PlanReconcile      = "reconcile"
	PlanSeed           = "seed"
	PlanSync           = "sync"
	PlanVerify         = "verify"
	PlanSnapshot       = "snapshot"
	PlanExport         = "export"
	PlanBuild          = "build"
	PlanReleasePrepare = "release-prepare"
	PlanReleasePublish = "release-publish"
	PlanPR             = "pr"
	PlanDeploy         = "deploy"
)

// StageView is one stage of the pending-changes checklist. Every text is
// fixed and path-free.
type StageView struct {
	Name   string
	Title  string
	Need   StageNeed
	Status StageStatus
	// BlockedReason explains a blocked (or still checking) stage.
	BlockedReason string
	// Note explains the need (why a stage is not needed, how a manual one
	// is done).
	Note string
	// Action is the maintenance action that runs the stage, if any.
	Action string
}

// PendingView is what changed in the catalog since the database last applied
// it and which stages bring the website up to date.
type PendingView struct {
	State   PendingState
	Changes []artists.ArtistChange
	Stages  []StageView
	// CatalogDigest is the first 12 characters of the catalog digest.
	CatalogDigest string
	// Updated reports that every stage the admin runs, through the pull
	// request, is done for the current digests. It never means the
	// published site was deployed: that happens on GitHub after merging.
	Updated bool
}

// ErrCatalogInvalid is returned by PendingDeps.Catalog when the catalog loads
// but does not validate.
var ErrCatalogInvalid = &SafeError{Code: "validation", Message: catalogInvalidReason + "."}

// Fixed reasons and notes of the pending view.
const (
	catalogInvalidReason    = "The editorial catalog does not validate; fix it in the editor first"
	catalogUnreadableReason = "The editorial catalog could not be read"
	databaseUnreadableNote  = "The database or its update record could not be read"
	stageMissingReason      = "This step is not available in this admin"
	releaseMissingReason    = "The release actions are not configured in this admin"
	baselineNote            = "The database records no applied catalog (legacy database): its state is unknown. " +
		"Run the bulk reconcile dry run as a check; a full update records the baseline."
	syncNotNeededNote = "Not every edit needs a Spotify call: only Spotify ID, verified or enabled changes are synced."
	buildNote         = "Not run from the admin: build the site locally with make site-build and check it with make audit-fast."
	deployNote        = "Check GitHub Pages after merging the pull request; merging and the deploy happen on GitHub."
	prOpenNote        = "A pull request may already be open for this content; running again reuses it."
)

// PendingDeps are what Pending and update-website read.
type PendingDeps struct {
	// DatabaseExists reports whether the database file exists.
	DatabaseExists func() bool
	// Catalog loads the editorial catalog and returns its fingerprint. A
	// catalog that loads but does not validate returns ErrCatalogInvalid.
	Catalog func(ctx context.Context) (artists.AppliedCatalog, error)
	// Provenance reads the applied catalog, the stage checkpoints and the
	// database digest.
	Provenance Provenance
}

// WithPending gives the Service what Pending reads and registers the
// update-website action, which runs the registered stage actions.
func WithPending(d PendingDeps) Option {
	return func(s *Service) { s.pending = &d }
}

type stageDef struct {
	name, title, action string
	params          Params
}

var stageDefs = []stageDef{
	{name: PlanValidate, title: "Validate the catalog"},
	{name: PlanReconcile, title: "Reconcile all artists", action: "reconcile-all-apply"},
	{name: PlanSeed, title: "Seed the database from the catalog", action: "db-seed"},
	{name: PlanSync, title: "Sync from Spotify", action: "sync", params: Params{"scope": syncScopeAll, "resume": "true"}},
	{name: PlanVerify, title: "Verify the database", action: "db-verify"},
	{name: PlanSnapshot, title: "Write the database snapshot", action: "db-snapshot"},
	{name: PlanExport, title: "Export the catalog for the site", action: "export"},
	{name: PlanBuild, title: "Build and audit the site locally"},
	{name: PlanReleasePrepare, title: "Prepare the catalog release", action: "release-prepare"},
	{name: PlanReleasePublish, title: "Publish the catalog release", action: "release-publish"},
	{name: PlanPR, title: "Open the catalog pull request", action: "release-pr"},
	{name: PlanDeploy, title: "GitHub Pages deploy"},
}

// checkpointStages are the provenance stages whose digests are compared.
var checkpointStages = []string{artists.StageSnapshot, artists.StageExport, artists.StageReleasePrepare,
	artists.StageRelease, artists.StagePR}

// pendingFacts are the digests a plan is computed from.
type pendingFacts struct {
	offline    bool
	unreadable bool
	catalogErr error
	catalog    artists.AppliedCatalog
	// applied: a baseline (applied catalog) is recorded.
	applied     bool
	changes     []artists.ArtistChange
	seedNeeded  bool
	pendingRecs []string
	dbDigest    string
	checkpoints map[string]artists.StageCheckpoint
}

func (f pendingFacts) usable() bool { return !f.offline && !f.unreadable && f.catalogErr == nil }

// matches reports whether stage last completed against exactly the current
// catalog and database digests.
func (f pendingFacts) matches(stage string) bool {
	cp, ok := f.checkpoints[stage]
	return ok && f.catalogErr == nil && cp.CatalogDigest != "" && cp.CatalogDigest == f.catalog.Digest &&
		cp.DBDigest != "" && cp.DBDigest == f.dbDigest
}

// facts reads the digests. Read failures are facts (unreadable), only a
// cancelled context is an error.
func (s *Service) facts(ctx context.Context) (pendingFacts, error) {
	d := s.pending
	f := pendingFacts{checkpoints: map[string]artists.StageCheckpoint{}}
	f.catalog, f.catalogErr = d.Catalog(ctx)
	if err := ctx.Err(); err != nil {
		return pendingFacts{}, err
	}
	if !d.DatabaseExists() {
		f.offline = true
		return f, nil
	}
	p := d.Provenance
	if p == nil {
		f.unreadable = true
		return f, nil
	}
	applied, ok, err := p.AppliedCatalog(ctx)
	if err == nil {
		f.dbDigest, err = p.DatabaseDigest(ctx)
	}
	if err == nil {
		f.pendingRecs, err = p.PendingReconciliations(ctx)
	}
	for _, stage := range checkpointStages {
		if err != nil {
			break
		}
		var cp artists.StageCheckpoint
		var has bool
		if cp, has, err = p.Checkpoint(ctx, stage); err == nil && has {
			f.checkpoints[stage] = cp
		}
	}
	if cerr := ctx.Err(); cerr != nil {
		return pendingFacts{}, cerr
	}
	if err != nil || f.dbDigest == "" {
		f.unreadable = true
		return f, nil
	}
	if ok && f.catalogErr == nil {
		f.applied = true
		f.changes = artists.DiffCatalog(applied, f.catalog)
		f.seedNeeded = len(f.changes) > 0 || applied.Digest != f.catalog.Digest
	}
	return f, nil
}

// needs computes, from digests only, which stages are required. Without a
// baseline the database stages are all required: a full reconcile, seed
// and sync record it.
func needs(f pendingFacts) map[string]StageNeed {
	req := func(b bool) StageNeed {
		if b {
			return NeedRequired
		}
		return NeedNotNeeded
	}
	var reconcile, seed, sync bool
	if !f.applied {
		reconcile, seed, sync = true, true, true
	} else {
		reconcile = len(f.pendingRecs) > 0
		for _, c := range f.changes {
			reconcile = reconcile || c.NeedsReconcile
			sync = sync || c.NeedsSync
		}
		seed = f.seedNeeded
	}
	db := reconcile || seed || sync
	snapshot := db || !f.matches(artists.StageSnapshot)
	export := db || !f.matches(artists.StageExport)
	publish := db || !f.matches(artists.StageRelease)
	prepare := db || (!f.matches(artists.StageReleasePrepare) && !f.matches(artists.StageRelease))
	pr := publish || !f.matches(artists.StagePR)
	return map[string]StageNeed{
		PlanValidate:       NeedRequired,
		PlanReconcile:      req(reconcile),
		PlanSeed:           req(seed),
		PlanSync:           req(sync),
		PlanVerify:         req(db || snapshot || export || prepare || publish),
		PlanSnapshot:       req(snapshot),
		PlanExport:         req(export),
		PlanBuild:          NeedManual,
		PlanReleasePrepare: req(prepare),
		PlanReleasePublish: req(publish),
		PlanPR:             req(pr),
		PlanDeploy:         NeedManual,
	}
}

// availability reports whether an action can run now.
type availability func(Action) (bool, string)

// quickAvailable never waits for a costly check (listings, the panel).
func quickAvailable(a Action) (bool, string) {
	if q, ok := a.(QuickAvailability); ok {
		return q.AvailableNow()
	}
	return a.Available()
}

func fullAvailable(a Action) (bool, string) { return a.Available() }

// isCheckingReason reports an availability that is not known yet.
func isCheckingReason(reason string) bool {
	return reason == prCheckingReason || reason == releaseCheckingReason
}

func isReleaseStage(name string) bool {
	return name == PlanReleasePrepare || name == PlanReleasePublish || name == PlanPR
}

// view builds the checklist for f.
func (s *Service) view(f pendingFacts, avail availability) PendingView {
	v := PendingView{Changes: f.changes}
	if len(f.catalog.Digest) >= 12 && f.catalogErr == nil {
		v.CatalogDigest = f.catalog.Digest[:12]
	}
	need := needs(f)
	for _, d := range stageDefs {
		v.Stages = append(v.Stages, s.stageView(d, need, f, avail))
	}
	switch {
	case f.offline:
		v.State = PendingOffline
	case !f.usable() || !f.applied:
		v.State = PendingUnknown
	default:
		v.State = PendingClean
		for _, st := range v.Stages {
			if st.Need == NeedRequired && st.Status != StageDone {
				v.State = PendingChanges
			}
		}
	}
	v.Updated = v.State == PendingClean
	return v
}

func (s *Service) stageView(d stageDef, need map[string]StageNeed, f pendingFacts, avail availability) StageView {
	st := StageView{Name: d.name, Title: d.title, Need: need[d.name], Action: d.action}
	switch d.name {
	case PlanValidate:
		switch {
		case f.catalogErr == nil:
			st.Status = StageDone
		case errors.Is(f.catalogErr, ErrCatalogInvalid):
			st.Status, st.BlockedReason = StageBlocked, catalogInvalidReason
		default:
			st.Status, st.BlockedReason = StageBlocked, catalogUnreadableReason
		}
		return st
	case PlanBuild:
		st.Status, st.Note = StageUnknown, buildNote
		return st
	case PlanDeploy:
		st.Status, st.Note = StageUnknown, deployNote
		return st
	}
	switch {
	case f.offline && d.name != PlanPR:
		st.Need, st.Status, st.BlockedReason = NeedRequired, StageBlocked, noDatabaseReason
		return st
	case f.offline:
		st.Need, st.Status = NeedRequired, StageTodo
		return st
	case f.catalogErr != nil:
		st.Need, st.Status, st.BlockedReason = NeedRequired, StageBlocked, catalogInvalidOrUnreadable(f.catalogErr)
		return st
	case f.unreadable:
		st.Need, st.Status, st.BlockedReason = NeedRequired, StageUnknown, databaseUnreadableNote
		return st
	}
	if d.name == PlanSeed && !f.applied {
		st.Status, st.Note, st.Action = StageUnknown, baselineNote, "reconcile-all-dry-run"
		return st
	}
	if st.Need != NeedRequired {
		st.Status = StageDone
		if d.name == PlanSync {
			st.Note = syncNotNeededNote
		}
		return st
	}
	if d.name == PlanPR {
		st.Note = prOpenNote
	}
	a, ok := s.byName[d.action]
	if !ok {
		st.Status, st.BlockedReason = StageBlocked, stageMissingReason
		if isReleaseStage(d.name) {
			st.BlockedReason = releaseMissingReason
		}
		return st
	}
	okNow, reason := avail(a)
	switch {
	case okNow:
		st.Status = StageTodo
	case isCheckingReason(reason):
		st.Status, st.BlockedReason = StageUnknown, reason
	case d.name == PlanPR && need[PlanReleasePublish] == NeedRequired && (reason == prManifestReason || reason == prNothingReason):
		// The publish stage pins the manifest this pull request carries.
		st.Status = StageTodo
	case d.name == PlanPR && reason == prNothingReason:
		// origin already has this catalog and manifest (merged).
		st.Need, st.Status, st.Note = NeedNotNeeded, StageDone, ""
	default:
		st.Status, st.BlockedReason = StageBlocked, reason
	}
	return st
}

func catalogInvalidOrUnreadable(err error) string {
	if errors.Is(err, ErrCatalogInvalid) {
		return catalogInvalidReason
	}
	return catalogUnreadableReason
}

// Pending reports what changed in the catalog since the database applied it
// and the state of every update-website stage. It is computed from digests
// only (the catalog fingerprint, the applied record, the database digest and
// the stage checkpoints): "clean" is never inferred from a flag or a file
// time. Availability is read without waiting for costly checks.
func (s *Service) Pending(ctx context.Context) (PendingView, error) {
	if s.pending == nil {
		return PendingView{}, ErrUnavailable
	}
	f, err := s.facts(ctx)
	if err != nil {
		return PendingView{}, err
	}
	return s.view(f, quickAvailable), nil
}
