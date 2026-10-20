package artists

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AssociationKind tells whether an association links a configured artist to an
// album or to a track.
type AssociationKind string

const (
	AssociationAlbum AssociationKind = "album"
	AssociationTrack AssociationKind = "track"
)

// Association is one active configured_artists -> album/track link together
// with the evidence needed to judge it: the Spotify artist IDs credited on the
// entity and the other configured artists that still reference it.
type Association struct {
	Kind              AssociationKind
	SpotifyID         string
	Name              string
	CreditedArtistIDs []string
	SharedWith        []string
}

// Baseline sources, from most to least authoritative.
const (
	BaselineSnapshot   = "snapshot"   // durable last-synced mapping
	BaselineConfigured = "configured" // IDs projected in configured_artist_spotify_ids (databases without a snapshot yet)
	BaselineNone       = "none"
)

// Reconciliation statuses. Drifted and awaiting-resync are "pending".
const (
	StatusClean          = "clean"
	StatusDrifted        = "drifted"
	StatusAwaitingResync = "awaiting_resync"
	StatusCompleted      = "completed"
)

// MappingBaseline is the Spotify identity mapping SQLite last synced.
type MappingBaseline struct {
	SpotifyIDs []string `json:"spotify_ids"`
	Source     string   `json:"source"`
	Checksum   string   `json:"checksum,omitempty"`
}

// Checkpoint is a recorded per-artist sync checkpoint.
type Checkpoint struct {
	RunID      int64  `json:"run_id"`
	Status     string `json:"status"`
	SpotifyIDs string `json:"spotify_ids"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// MetadataRefresh is the cached last metadata refresh of an artist.
type MetadataRefresh struct {
	RefreshedAt string `json:"refreshed_at"`
	SpotifyIDs  string `json:"spotify_ids"`
}

// PendingReconciliation is the journal row of a reconciliation.
type PendingReconciliation struct {
	Slug                   string   `json:"slug"`
	Status                 string   `json:"status"`
	PreviousIDs            []string `json:"previous_ids"`
	TargetIDs              []string `json:"target_ids"`
	PlanChecksum           string   `json:"plan_checksum,omitempty"`
	ResyncFingerprint      string   `json:"-"`
	RetiredAt              string   `json:"retired_at,omitempty"`
	BackupPath             string   `json:"backup_path,omitempty"`
	BackupChecksum         string   `json:"backup_checksum,omitempty"`
	SnapshotChecksumBefore string   `json:"snapshot_checksum_before,omitempty"`
	DetectedAt             string   `json:"detected_at,omitempty"`
	UpdatedAt              string   `json:"updated_at,omitempty"`
	LastError              string   `json:"last_error,omitempty"`
}

// Pending tells whether consistency between YAML and SQLite is still to be restored.
func (p PendingReconciliation) Pending() bool {
	return p.Status == StatusDrifted || p.Status == StatusAwaitingResync
}

// ImpactState is the read-only database state a plan is computed from.
type ImpactState struct {
	Baseline        MappingBaseline
	Checkpoints     []Checkpoint
	MetadataRefresh *MetadataRefresh
	Reconciliation  *PendingReconciliation
}

// BackupRequest asks for a local backup that never replaces an existing file.
type BackupRequest struct {
	Dir  string
	Name string
	// Manifest is the deterministic plan stored beside the backup.
	Manifest []byte
}

// BackupInfo identifies a created backup and the logical-snapshot content it holds.
type BackupInfo struct {
	Path             string `json:"path"`
	ManifestPath     string `json:"manifest_path"`
	SHA256           string `json:"sha256"`
	SnapshotChecksum string `json:"snapshot_sha256"`
}

// ApplyRequest is one transactional reconciliation of a configured artist.
type ApplyRequest struct {
	Slug              string
	Token             string // retired_at rollback token
	AlbumIDs          []string
	TrackIDs          []string
	PreviousIDs       []string
	TargetIDs         []string
	CurrentRefreshIDs string // joined IDs a still-valid metadata refresh carries
	Fingerprint       string // fingerprint checkpoints must carry to stay valid
	ResyncFingerprint string // empty when no replacement sync is needed
	PlanChecksum      string
	Backup            BackupInfo
}

// ReconcileStore is the persistence port for association reconciliation.
type ReconcileStore interface {
	// ActiveAssociations lists the active album and track associations of a
	// configured artist.
	ActiveAssociations(ctx context.Context, slug string) ([]Association, error)
	// Impact reads baseline mapping, checkpoints, caches and pending state
	// without writing anything.
	Impact(ctx context.Context, slug string) (ImpactState, error)
	// PendingReconciliations lists every non-completed journal row.
	PendingReconciliations(ctx context.Context) ([]PendingReconciliation, error)
	// BackupDatabase writes a consistent local copy of the database.
	BackupDatabase(ctx context.Context, request BackupRequest) (BackupInfo, error)
	// ApplyReconciliation retires associations, invalidates checkpoints and
	// caches and updates the journal in one transaction. Shared Spotify
	// artists, albums and tracks are never touched.
	ApplyReconciliation(ctx context.Context, request ApplyRequest) error
	// RestoreAssociations reverts an ApplyReconciliation identified by its token.
	RestoreAssociations(ctx context.Context, slug, retiredAt string) (albums, tracks int, err error)
}

// ReconcileClass is the verdict for one association.
type ReconcileClass string

const (
	// ReconcileSupported: an entity credit points at a currently linked profile.
	ReconcileSupported ReconcileClass = "supported"
	// ReconcileOrphan: credits exist but none points at a linked profile (or the
	// artist has no linked profile at all).
	ReconcileOrphan ReconcileClass = "orphan"
	// ReconcileUnverifiable: no credits are stored, so nothing can be proven. It
	// is reported and kept.
	ReconcileUnverifiable ReconcileClass = "unverifiable"
)

// Decision actions proposed by a plan. All are reversible with --restore.
const (
	ActionUnlink             = "unlink_association"
	ActionPreserveShared     = "preserve_shared_media"
	ActionKeep               = "keep_association"
	ActionResync             = "resync_replacement_ids"
	ActionInvalidateCheckpt  = "invalidate_checkpoint"
	ActionInvalidateCache    = "invalidate_metadata_cache"
	ActionUpdateSnapshot     = "update_mapping_snapshot"
	ActionReportUnreferenced = "report_unreferenced_media"
)

type ReconcileInput struct {
	CatalogPath string
	Slug        string
	// Apply performs the plan. Without it the run is a write-free dry run.
	Apply bool
	// ConfirmChecksum must equal the dry-run plan checksum for Apply to proceed.
	ConfirmChecksum string
	// BackupDir receives the pre-change backup taken by Apply.
	BackupDir string
	// RestoreRetiredAt reverts a previous Apply identified by its retired_at.
	RestoreRetiredAt string
	Now              time.Time
}

type ReconcileItem struct {
	Kind              AssociationKind `json:"kind"`
	SpotifyID         string          `json:"spotify_id"`
	Name              string          `json:"name"`
	Class             ReconcileClass  `json:"class"`
	CreditedArtistIDs []string        `json:"credited_artist_ids,omitempty"`
	SharedWith        []string        `json:"shared_with,omitempty"`
}

type ReconcileCounts struct {
	Albums int `json:"albums"`
	Tracks int `json:"tracks"`
}

// IdentityDiff compares the last-synced mapping with the reviewed YAML mapping.
type IdentityDiff struct {
	BaselineSource string   `json:"baseline_source"`
	Previous       []string `json:"previous"`
	Current        []string `json:"current"`
	Added          []string `json:"added"`
	Removed        []string `json:"removed"`
	Kept           []string `json:"kept"`
	Drift          bool     `json:"drift"`
}

// Decision is one proposed, reversible step.
type Decision struct {
	Action string `json:"action"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type ReconcileReport struct {
	Slug      string   `json:"slug"`
	LinkedIDs []string `json:"linked_ids"`
	DryRun    bool     `json:"dry_run"`
	// Status is clean, drifted, awaiting_resync, applied or restored.
	Status string `json:"status"`
	// PlanChecksum identifies the reviewed plan; pass it as ConfirmChecksum.
	PlanChecksum string       `json:"plan_checksum"`
	Identity     IdentityDiff `json:"identity"`
	// CreditedArtistIDs are the distinct Spotify artists credited on the affected associations.
	CreditedArtistIDs []string `json:"credited_artist_ids"`
	// RetiredAt is the rollback token: pass it back as RestoreRetiredAt.
	RetiredAt string          `json:"retired_at,omitempty"`
	Before    ReconcileCounts `json:"before"`
	After     ReconcileCounts `json:"after"`
	Supported ReconcileCounts `json:"supported"`
	Orphan    ReconcileCounts `json:"orphan"`
	// Unverifiable associations are kept.
	Unverifiable ReconcileCounts `json:"unverifiable"`
	// SharedOrphans are orphan entities that other configured artists still
	// reference; they stay in the catalog.
	SharedOrphans ReconcileCounts `json:"shared_orphans"`
	// UnreferencedAfter are orphan entities no configured artist references once
	// retired. They are only reported: global rows are never deleted here.
	UnreferencedAfter ReconcileCounts `json:"unreferenced_after"`
	// StaleCheckpoints are completed sync checkpoints tied to other IDs.
	StaleCheckpoints []Checkpoint           `json:"stale_checkpoints"`
	MetadataStale    bool                   `json:"metadata_cache_stale"`
	ResyncRequired   bool                   `json:"resync_required"`
	Decisions        []Decision             `json:"decisions"`
	Pending          *PendingReconciliation `json:"pending,omitempty"`
	Backup           *BackupInfo            `json:"backup,omitempty"`
	Restored         *ReconcileCounts       `json:"restored,omitempty"`
	Items            []ReconcileItem        `json:"items"`
}

// NeedsChanges tells whether Apply would change anything. Drift already covered
// by an applied reconciliation that waits for its resync is not a change.
func (r ReconcileReport) NeedsChanges() bool {
	covered := r.Pending != nil && r.Pending.Status == StatusAwaitingResync &&
		idSetChecksum(r.Pending.TargetIDs) == idSetChecksum(r.Identity.Current)

	return r.Orphan.Albums+r.Orphan.Tracks > 0 || len(r.StaleCheckpoints) > 0 || r.MetadataStale || (r.Identity.Drift && !covered)
}

func idSetChecksum(ids []string) string {
	return fingerprint(sortedCopyOrEmpty(ids))
}

type ReconcileArtist struct {
	catalog CatalogStore
	store   ReconcileStore
}

func NewReconcileArtist(catalog CatalogStore, store ReconcileStore) ReconcileArtist {
	return ReconcileArtist{catalog: catalog, store: store}
}

// Run diffs the last-synced identity mapping against the reviewed YAML mapping
// of one configured artist, classifies its active associations, and optionally
// applies the reviewed plan or restores a previous application.
func (r ReconcileArtist) Run(ctx context.Context, in ReconcileInput) (ReconcileReport, error) {
	if in.Apply && in.RestoreRetiredAt != "" {
		return ReconcileReport{}, fmt.Errorf("apply and restore cannot be combined")
	}
	c, err := r.catalog.Load(ctx, in.CatalogPath)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("load artist catalog: %w", err)
	}
	var artist *editorialArtist
	for i := range c.Artists {
		if c.Artists[i].Slug == in.Slug {
			a := c.Artists[i]
			artist = &editorialArtist{
				linked:   a.AllSpotifyIDs(),
				syncable: a.SyncableSpotifyIDs(c.ProfileSyncPolicy),
				enabled:  a.Enabled,
			}
			break
		}
	}
	if artist == nil {
		return ReconcileReport{}, fmt.Errorf("artist %q not found", in.Slug)
	}

	if in.RestoreRetiredAt != "" {
		albums, tracks, err := r.store.RestoreAssociations(ctx, in.Slug, in.RestoreRetiredAt)
		if err != nil {
			return ReconcileReport{}, fmt.Errorf("restore associations: %w", err)
		}
		report, err := r.plan(ctx, in.Slug, artist)
		if err != nil {
			return report, err
		}
		report.Status = "restored"
		report.DryRun = false
		report.RetiredAt = in.RestoreRetiredAt
		report.Restored = &ReconcileCounts{Albums: albums, Tracks: tracks}
		return report, nil
	}

	report, err := r.plan(ctx, in.Slug, artist)
	if err != nil {
		return report, err
	}
	report.DryRun = !in.Apply
	if !in.Apply {
		return report, nil
	}
	if !report.NeedsChanges() {
		// Idempotent: nothing left to do, whatever token the caller holds.
		return report, nil
	}
	if in.ConfirmChecksum == "" {
		return report, fmt.Errorf("apply requires --confirm %s from a reviewed dry run", report.PlanChecksum)
	}
	if in.ConfirmChecksum != report.PlanChecksum {
		return report, fmt.Errorf("plan changed since review (confirmed %s, current %s); rerun the dry run", in.ConfirmChecksum, report.PlanChecksum)
	}
	if in.BackupDir == "" {
		return report, fmt.Errorf("apply requires a backup directory")
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	token := now.UTC().Format(time.RFC3339)

	manifest, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, fmt.Errorf("encode plan manifest: %w", err)
	}
	backup, err := r.store.BackupDatabase(ctx, BackupRequest{
		Dir:      in.BackupDir,
		Name:     backupName(in.Slug, token),
		Manifest: append(manifest, '\n'),
	})
	if err != nil {
		return report, fmt.Errorf("backup before reconcile: %w", err)
	}

	request := ApplyRequest{
		Slug:              in.Slug,
		Token:             token,
		PreviousIDs:       report.Identity.Previous,
		TargetIDs:         report.Identity.Current,
		CurrentRefreshIDs: strings.Join(artist.linked, ","),
		Fingerprint:       fingerprint(artist.syncable),
		PlanChecksum:      report.PlanChecksum,
		Backup:            backup,
	}
	if report.ResyncRequired {
		request.ResyncFingerprint = request.Fingerprint
	}
	for _, item := range report.Items {
		if item.Class != ReconcileOrphan {
			continue
		}
		if item.Kind == AssociationAlbum {
			request.AlbumIDs = append(request.AlbumIDs, item.SpotifyID)
		} else {
			request.TrackIDs = append(request.TrackIDs, item.SpotifyID)
		}
	}
	if err := r.store.ApplyReconciliation(ctx, request); err != nil {
		return report, fmt.Errorf("apply reconciliation (database unchanged, backup kept at %s): %w", backup.Path, err)
	}

	report.RetiredAt = token
	report.Backup = &backup
	report.Status = "applied"
	if report.ResyncRequired {
		report.Status = StatusAwaitingResync
	}
	report.After = ReconcileCounts{
		Albums: report.Before.Albums - len(request.AlbumIDs),
		Tracks: report.Before.Tracks - len(request.TrackIDs),
	}

	return report, nil
}

type editorialArtist struct {
	linked   []string
	syncable []string
	enabled  bool
}

// plan computes the write-free report for one artist.
func (r ReconcileArtist) plan(ctx context.Context, slug string, artist *editorialArtist) (ReconcileReport, error) {
	report := ReconcileReport{Slug: slug, LinkedIDs: sortedCopyOrEmpty(artist.linked), DryRun: true}

	impact, err := r.store.Impact(ctx, slug)
	if err != nil {
		return report, fmt.Errorf("read reconciliation state: %w", err)
	}
	associations, err := r.store.ActiveAssociations(ctx, slug)
	if err != nil {
		return report, fmt.Errorf("read associations: %w", err)
	}

	report.Identity = diffIdentity(impact.Baseline, artist.linked)
	report.Pending = impact.Reconciliation

	linkedSet := make(map[string]struct{}, len(artist.linked))
	for _, id := range artist.linked {
		linkedSet[id] = struct{}{}
	}

	credited := map[string]struct{}{}
	for _, a := range associations {
		item := ReconcileItem{
			Kind:              a.Kind,
			SpotifyID:         a.SpotifyID,
			Name:              a.Name,
			Class:             classifyAssociation(a, linkedSet),
			CreditedArtistIDs: sortedCopy(a.CreditedArtistIDs),
			SharedWith:        sortedCopy(a.SharedWith),
		}
		report.Items = append(report.Items, item)
		*countsFor(&report.Before, a.Kind)++

		switch item.Class {
		case ReconcileSupported:
			*countsFor(&report.Supported, a.Kind)++
		case ReconcileUnverifiable:
			*countsFor(&report.Unverifiable, a.Kind)++
		case ReconcileOrphan:
			*countsFor(&report.Orphan, a.Kind)++
			for _, id := range a.CreditedArtistIDs {
				credited[id] = struct{}{}
			}
			if len(a.SharedWith) > 0 {
				*countsFor(&report.SharedOrphans, a.Kind)++
			} else {
				*countsFor(&report.UnreferencedAfter, a.Kind)++
			}
		}
	}
	sort.Slice(report.Items, func(i, j int) bool {
		a, b := report.Items[i], report.Items[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.SpotifyID < b.SpotifyID
	})
	report.CreditedArtistIDs = idSetKeys(credited)
	report.After = ReconcileCounts{
		Albums: report.Before.Albums - report.Orphan.Albums,
		Tracks: report.Before.Tracks - report.Orphan.Tracks,
	}

	currentFingerprint := fingerprint(artist.syncable)
	report.StaleCheckpoints = []Checkpoint{}
	hasCurrent := false
	for _, cp := range impact.Checkpoints {
		if cp.Status != "completed" {
			continue
		}
		if cp.SpotifyIDs == currentFingerprint {
			hasCurrent = true
			continue
		}
		report.StaleCheckpoints = append(report.StaleCheckpoints, cp)
	}
	sort.Slice(report.StaleCheckpoints, func(i, j int) bool { return report.StaleCheckpoints[i].RunID < report.StaleCheckpoints[j].RunID })
	if m := impact.MetadataRefresh; m != nil && m.SpotifyIDs != strings.Join(artist.linked, ",") {
		report.MetadataStale = true
	}
	report.ResyncRequired = artist.enabled && len(artist.syncable) > 0 && !hasCurrent

	report.Status = StatusClean
	switch {
	case impact.Reconciliation != nil && impact.Reconciliation.Status == StatusAwaitingResync:
		report.Status = StatusAwaitingResync
	case report.NeedsChanges():
		report.Status = StatusDrifted
	}
	report.Decisions = buildDecisions(report)
	report.PlanChecksum = planChecksum(report)

	return report, nil
}

func diffIdentity(baseline MappingBaseline, current []string) IdentityDiff {
	diff := IdentityDiff{
		BaselineSource: baseline.Source,
		Previous:       sortedCopyOrEmpty(baseline.SpotifyIDs),
		Current:        sortedCopyOrEmpty(current),
		Added:          []string{},
		Removed:        []string{},
		Kept:           []string{},
	}
	if diff.BaselineSource == "" {
		diff.BaselineSource = BaselineNone
	}
	prev := toSet(diff.Previous)
	cur := toSet(diff.Current)
	for _, id := range diff.Current {
		if _, ok := prev[id]; ok {
			diff.Kept = append(diff.Kept, id)
		} else {
			diff.Added = append(diff.Added, id)
		}
	}
	for _, id := range diff.Previous {
		if _, ok := cur[id]; !ok {
			diff.Removed = append(diff.Removed, id)
		}
	}
	// Without any baseline nothing can be compared: classification still runs.
	diff.Drift = baseline.Source != BaselineNone && baseline.Source != "" && (len(diff.Added) > 0 || len(diff.Removed) > 0)

	return diff
}

func buildDecisions(report ReconcileReport) []Decision {
	decisions := []Decision{}
	for _, item := range report.Items {
		target := fmt.Sprintf("%s:%s", item.Kind, item.SpotifyID)
		switch item.Class {
		case ReconcileOrphan:
			decisions = append(decisions, Decision{Action: ActionUnlink, Target: target, Reason: "credits exist but none points at a linked profile"})
			if len(item.SharedWith) > 0 {
				decisions = append(decisions, Decision{Action: ActionPreserveShared, Target: target, Reason: "still referenced by " + strings.Join(item.SharedWith, ",")})
			} else {
				decisions = append(decisions, Decision{Action: ActionReportUnreferenced, Target: target, Reason: "no configured artist references it after unlinking; row kept for a separate reviewed prune"})
			}
		case ReconcileUnverifiable:
			decisions = append(decisions, Decision{Action: ActionKeep, Target: target, Reason: "no stored credits, nothing can be proven"})
		}
	}
	for _, cp := range report.StaleCheckpoints {
		decisions = append(decisions, Decision{Action: ActionInvalidateCheckpt, Target: fmt.Sprintf("run:%d", cp.RunID), Reason: "checkpoint is tied to " + cp.SpotifyIDs})
	}
	if report.MetadataStale {
		decisions = append(decisions, Decision{Action: ActionInvalidateCache, Target: report.Slug, Reason: "metadata refresh carries other Spotify IDs"})
	}
	if report.ResyncRequired {
		decisions = append(decisions, Decision{Action: ActionResync, Target: strings.Join(report.Identity.Current, ","), Reason: "no completed checkpoint for the current IDs"})
	}
	if report.Identity.Drift {
		decisions = append(decisions, Decision{Action: ActionUpdateSnapshot, Target: report.Slug, Reason: "record the reviewed mapping once the replacement sync completes"})
	}
	sort.SliceStable(decisions, func(i, j int) bool {
		if decisions[i].Action != decisions[j].Action {
			return decisions[i].Action < decisions[j].Action
		}
		return decisions[i].Target < decisions[j].Target
	})

	return decisions
}

// planChecksum hashes only what the plan does, never timestamps or state of
// previous runs, so a reviewed dry run can be confirmed later.
func planChecksum(report ReconcileReport) string {
	type basisItem struct {
		Kind  AssociationKind `json:"kind"`
		ID    string          `json:"id"`
		Class ReconcileClass  `json:"class"`
	}
	type basis struct {
		Slug        string      `json:"slug"`
		Previous    []string    `json:"previous"`
		Current     []string    `json:"current"`
		Items       []basisItem `json:"items"`
		Checkpoints []int64     `json:"checkpoints"`
		Cache       bool        `json:"cache"`
		Resync      bool        `json:"resync"`
	}
	b := basis{
		Slug:     report.Slug,
		Previous: report.Identity.Previous,
		Current:  report.Identity.Current,
		Cache:    report.MetadataStale,
		Resync:   report.ResyncRequired,
	}
	for _, item := range report.Items {
		b.Items = append(b.Items, basisItem{Kind: item.Kind, ID: item.SpotifyID, Class: item.Class})
	}
	for _, cp := range report.StaleCheckpoints {
		b.Checkpoints = append(b.Checkpoints, cp.RunID)
	}
	data, _ := json.Marshal(b)
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func backupName(slug, token string) string {
	replacer := strings.NewReplacer(":", "", "-", "")
	return fmt.Sprintf("%s-%s", slug, replacer.Replace(token))
}

func fingerprint(ids []string) string {
	data, err := json.Marshal(ids)
	if err != nil {
		return ""
	}

	return string(data)
}

// PendingStatus lists artists whose YAML identity mapping and SQLite disagree.
type PendingStatus struct {
	Pending []PendingReconciliation `json:"pending"`
}

// Status reports every pending reconciliation: journal rows plus artists whose
// reviewed mapping differs from the last-synced snapshot but were not yet
// seeded. It is read-only.
func (r ReconcileArtist) Status(ctx context.Context, catalogPath string) (PendingStatus, error) {
	rows, err := r.store.PendingReconciliations(ctx)
	if err != nil {
		return PendingStatus{}, fmt.Errorf("read pending reconciliations: %w", err)
	}
	c, err := r.catalog.Load(ctx, catalogPath)
	if err != nil {
		return PendingStatus{}, fmt.Errorf("load artist catalog: %w", err)
	}

	known := map[string]struct{}{}
	for _, row := range rows {
		known[row.Slug] = struct{}{}
	}
	for _, a := range c.Artists {
		if _, ok := known[a.Slug]; ok {
			continue
		}
		impact, err := r.store.Impact(ctx, a.Slug)
		if err != nil {
			return PendingStatus{}, fmt.Errorf("read reconciliation state %s: %w", a.Slug, err)
		}
		diff := diffIdentity(impact.Baseline, a.AllSpotifyIDs())
		if !diff.Drift {
			continue
		}
		rows = append(rows, PendingReconciliation{Slug: a.Slug, Status: StatusDrifted, PreviousIDs: diff.Previous, TargetIDs: diff.Current})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slug < rows[j].Slug })
	if rows == nil {
		rows = []PendingReconciliation{}
	}

	return PendingStatus{Pending: rows}, nil
}

func classifyAssociation(a Association, linked map[string]struct{}) ReconcileClass {
	for _, id := range a.CreditedArtistIDs {
		if _, ok := linked[id]; ok {
			return ReconcileSupported
		}
	}
	if len(linked) == 0 || len(a.CreditedArtistIDs) > 0 {
		return ReconcileOrphan
	}

	return ReconcileUnverifiable
}

func countsFor(c *ReconcileCounts, kind AssociationKind) *int {
	if kind == AssociationAlbum {
		return &c.Albums
	}

	return &c.Tracks
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)

	return out
}

// ClassifyAssociation exposes the reconciliation verdict for read-only reports.
func ClassifyAssociation(a Association, linked map[string]struct{}) ReconcileClass {
	return classifyAssociation(a, linked)
}

func sortedCopyOrEmpty(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)

	return out
}

func idSetKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)

	return out
}

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}

	return set
}
