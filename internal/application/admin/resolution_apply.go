package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/javiyt/spotwufamily/internal/application/artists"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const maxNoteLength = 1000

// ErrOverrideRequired means automated evidence contradicts the profile, so the
// reviewer must state why they verify it anyway.
var ErrOverrideRequired = errors.New("automated evidence contradicts this profile; an override reason is required to verify it")

// ResolveRequest is one human decision about a Spotify profile.
type ResolveRequest struct {
	Slug      string
	SpotifyID string
	// Action is a MappingAction or ActionVerify.
	Action   string
	Revision string
	Reviewer string
	Note     string
	// Evidence is the source material the reviewer supplied.
	Evidence       []catalog.Evidence
	Verify         bool
	OverrideReason string
	Enable         bool
	// Confirm applies the change; without it only a preview is produced.
	Confirm bool
}

// Impact predicts what a mapping change does downstream.
type Impact struct {
	SyncBefore []string
	SyncAfter  []string
	// SyncAdded and SyncRemoved are the IDs sync will start or stop ingesting.
	SyncAdded   []string
	SyncRemoved []string
	// LinkedBefore and LinkedAfter are the linked IDs in order, primary first.
	LinkedBefore  []string
	LinkedAfter   []string
	EnabledBefore bool
	EnabledAfter  bool
	// ReconcileKnown is false when the database is unavailable.
	ReconcileKnown  bool
	ReconcileBefore int
	ReconcileAfter  int
	Notes           []string
}

// ResolvePreview is the confirmation step: the exact catalog change, its
// downstream impact and any gate that applies.
type ResolvePreview struct {
	Request  ResolveRequest
	Artist   catalog.Artist
	Result   artists.MappingResult
	Changes  []FieldChange
	Impact   Impact
	Warnings []string
	// Contradictions come from the evidence audit; a verify then needs an
	// override reason.
	Contradictions []string
	NeedsOverride  bool
}

// ResolveResult reports a stored decision.
type ResolveResult struct {
	Preview  ResolvePreview
	Revision string
	// LogError is set when the decision was stored but the audit trail could not be written.
	LogError string
}

// PreviewResolution validates a decision on a throwaway copy and reports what
// it would change, without writing anything.
func (s Service) PreviewResolution(ctx context.Context, req ResolveRequest) (ResolvePreview, error) {
	if s.res == nil {
		return ResolvePreview{}, ErrReadOnly
	}
	if s.updater == nil {
		return ResolvePreview{}, ErrReadOnly
	}
	c, err := s.load(ctx)
	if err != nil {
		return ResolvePreview{}, err
	}
	preview, _, err := s.planResolution(ctx, c, req)
	return preview, err
}

// ApplyResolution stores a confirmed decision under the catalog lock. It never
// touches the database: changed mappings show up as pending reconciliation,
// which `artists reconcile` resolves.
func (s Service) ApplyResolution(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	if s.res == nil || s.updater == nil {
		return ResolveResult{}, ErrReadOnly
	}
	if !req.Confirm {
		return ResolveResult{}, &ConfirmationError{Action: "profile_mapping"}
	}
	// The evidence audit may call external sources, so it runs before the lock
	// is taken and its verdict is reused inside the locked update.
	c, err := s.load(ctx)
	if err != nil {
		return ResolveResult{}, err
	}
	audit := s.auditForVerify(ctx, c, req)

	var result ResolveResult
	var after catalog.EditorialCatalog
	err = s.updater.Update(ctx, s.catalogPath, func(current catalog.EditorialCatalog) (catalog.EditorialCatalog, error) {
		preview, next, err := s.planResolutionWith(ctx, current, req, audit)
		if err != nil {
			return current, err
		}
		result.Preview = preview
		after = next
		return next, nil
	})
	if err != nil {
		return ResolveResult{}, err
	}
	if i := after.FindArtist(req.Slug); i >= 0 {
		result.Revision = ArtistRevision(after.Artists[i])
	}

	if log := s.res.deps.Log; log != nil {
		entry := ReviewLogEntry{
			At: s.res.deps.Now().UTC(), Actor: strings.TrimSpace(req.Reviewer), Slug: req.Slug, SpotifyID: req.SpotifyID,
			Action: req.Action, From: string(result.Preview.Result.From), To: string(result.Preview.Result.To),
			Note: strings.TrimSpace(req.Note), OverrideReason: strings.TrimSpace(req.OverrideReason),
			IDsBefore: result.Preview.Impact.LinkedBefore, IDsAfter: result.Preview.Impact.LinkedAfter,
		}
		for _, e := range req.Evidence {
			if e.URL != "" {
				entry.EvidenceURLs = append(entry.EvidenceURLs, e.URL)
			}
		}
		if err := log.Append(ctx, entry); err != nil {
			result.LogError = "the decision was saved, but the audit log could not be written"
		}
	}
	return result, nil
}

// auditForVerify gathers contradicting evidence for a verify decision. Other
// decisions need no audit: rejecting or quarantining never relies on it.
func (s Service) auditForVerify(ctx context.Context, c catalog.EditorialCatalog, req ResolveRequest) *artists.ProfileAuditResult {
	if !wantsVerify(req) {
		return nil
	}
	deps := s.res.deps
	var mb artists.MusicBrainzReleaseGroupSearcher = offlineMusicBrainz{}
	if deps.MusicBrainz != nil {
		mb = deps.MusicBrainz
	}
	ctx, cancel := context.WithTimeout(ctx, externalTimeout)
	defer cancel()
	result, err := artists.NewAuditProfiles(s.store, newMemoSource(deps.Source), mb).EvaluateProfile(
		ctx, c, req.Slug, req.SpotifyID, artists.ProfileRoleSecondary,
		artists.AuditProfilesOptions{Offline: deps.MusicBrainz == nil, Now: deps.Now})
	if err != nil {
		return nil
	}
	return &result
}

func wantsVerify(req ResolveRequest) bool {
	return req.Action == ActionVerify || req.Verify
}

func (s Service) planResolution(ctx context.Context, c catalog.EditorialCatalog, req ResolveRequest) (ResolvePreview, catalog.EditorialCatalog, error) {
	return s.planResolutionWith(ctx, c, req, s.auditForVerify(ctx, c, req))
}

// planResolutionWith applies req to a copy of c: revision check, shared
// mapping rules, evidence gate, whole-catalog validation and impact.
func (s Service) planResolutionWith(ctx context.Context, c catalog.EditorialCatalog, req ResolveRequest, audit *artists.ProfileAuditResult) (ResolvePreview, catalog.EditorialCatalog, error) {
	preview := ResolvePreview{Request: req}
	index := c.FindArtist(req.Slug)
	if index < 0 {
		return preview, c, fmt.Errorf("%w: artist %q", ErrNotFound, req.Slug)
	}
	before := c.Artists[index]
	if current := ArtistRevision(before); req.Revision != "" && req.Revision != current {
		return preview, c, s.profileConflict(before, current, req.Revision)
	}
	if err := checkResolveText(req); err != nil {
		return preview, c, err
	}

	mapping, err := mappingFor(before, req)
	mapping.At = s.res.deps.Now().UTC()
	if err != nil {
		return preview, c, err
	}
	if audit != nil && mapping.Verify {
		preview.Contradictions = append(preview.Contradictions, audit.Contradictions...)
		for _, e := range audit.Evidence {
			if e.Polarity == catalog.EvidenceNegative {
				preview.NeedsOverride = true
			}
		}
		if audit.Classification == artists.ClassSuspicious || audit.Classification == artists.ClassAmbiguous {
			preview.NeedsOverride = true
		}
		if preview.NeedsOverride && strings.TrimSpace(req.OverrideReason) == "" {
			return preview, c, ErrOverrideRequired
		}
	}

	next := c.Clone()
	result, err := artists.ApplyMapping(&next, mapping)
	preview.Result = result
	if err != nil {
		return preview, c, err
	}
	if err := validateChange(c, next, req.Slug); err != nil {
		return preview, c, err
	}

	after := next.Artists[next.FindArtist(req.Slug)]
	preview.Artist = after
	preview.Changes = diffArtists(before, after)
	preview.Impact = s.impact(ctx, c, next, before, after, result)
	preview.Warnings = resolveWarnings(result, preview.Impact, audit)
	return preview, next, nil
}

func (s Service) profileConflict(current catalog.Artist, currentRev, requested string) error {
	conflict := &ConflictError{Slug: current.Slug, CurrentRevision: currentRev}
	if base, ok := s.snapshots.get(requested); ok {
		conflict.BaseKnown, conflict.Changed = true, diffArtists(base, current)
	}
	return conflict
}

func checkResolveText(req ResolveRequest) error {
	for name, value := range map[string]string{"note": req.Note, "override reason": req.OverrideReason, "reviewer": req.Reviewer} {
		if len(value) > maxNoteLength {
			return fmt.Errorf("%w: the %s is longer than %d characters", artists.ErrInvalidMapping, name, maxNoteLength)
		}
	}
	for _, e := range req.Evidence {
		if issues := catalog.ValidateEvidence(e); len(issues) > 0 {
			return fmt.Errorf("%w: %s", artists.ErrInvalidMapping, issues[0].Error())
		}
	}
	return nil
}

func mappingFor(artist catalog.Artist, req ResolveRequest) (artists.MappingRequest, error) {
	m := artists.MappingRequest{
		Slug: req.Slug, SpotifyID: req.SpotifyID, Reviewer: req.Reviewer, Note: req.Note, Evidence: req.Evidence,
		Verify: req.Verify, OverrideReason: req.OverrideReason, Enable: req.Enable,
	}
	switch req.Action {
	case ActionVerify:
		// Verifying keeps the link where it is.
		m.Verify = true
		if artist.SpotifyID == req.SpotifyID {
			m.Action = artists.MapAcceptPrimary
		} else {
			m.Action = artists.MapAddSecondary
		}
		if !artist.HasSpotifyID(req.SpotifyID) {
			return m, fmt.Errorf("%w: %s is not linked to %s; accept it instead", artists.ErrInvalidMapping, req.SpotifyID, artist.Slug)
		}
	default:
		m.Action = artists.MappingAction(req.Action)
	}
	if !artists.IsKnownMappingAction(m.Action) {
		return m, fmt.Errorf("%w: unknown action %q", artists.ErrInvalidMapping, req.Action)
	}
	return m, nil
}

func (s Service) impact(ctx context.Context, beforeCat, afterCat catalog.EditorialCatalog, before, after catalog.Artist, res artists.MappingResult) Impact {
	policy := beforeCat.ProfileSyncPolicy
	im := Impact{
		SyncBefore: before.SyncableSpotifyIDs(policy), SyncAfter: after.SyncableSpotifyIDs(afterCat.ProfileSyncPolicy),
		LinkedBefore: before.AllSpotifyIDs(), LinkedAfter: after.AllSpotifyIDs(),
		EnabledBefore: before.Enabled, EnabledAfter: after.Enabled,
	}
	im.SyncAdded, im.SyncRemoved = diffIDs(im.SyncBefore, im.SyncAfter)

	if s.ops != nil {
		one := func(a catalog.Artist) (int, error) {
			return s.ops.PendingReconciliation(ctx, catalog.EditorialCatalog{Version: beforeCat.Version, Artists: []catalog.Artist{a}})
		}
		if b, err := one(before); err == nil {
			if a, err := one(after); err == nil {
				im.ReconcileKnown, im.ReconcileBefore, im.ReconcileAfter = true, b, a
			}
		}
	}

	switch {
	case len(im.SyncAdded) > 0:
		im.Notes = append(im.Notes, "The next sync will start ingesting "+strings.Join(im.SyncAdded, ", ")+".")
	}
	if len(im.SyncRemoved) > 0 {
		im.Notes = append(im.Notes, "The next sync will stop ingesting "+strings.Join(im.SyncRemoved, ", ")+"; rows already synced from it are not deleted by this change.")
	}
	if len(im.SyncAdded) == 0 && len(im.SyncRemoved) == 0 && sameIDs(im.LinkedBefore, im.LinkedAfter) {
		im.Notes = append(im.Notes, "Nothing is linked or unlinked, so sync and the database are unaffected.")
	}
	if !res.Action.ChangesMapping() {
		return im
	}
	if im.ReconcileKnown && im.ReconcileAfter > im.ReconcileBefore {
		im.Notes = append(im.Notes, fmt.Sprintf("%d more synced link(s) will be pending reconciliation. Run `spotwufamily artists reconcile --artist %s --dry-run` to review them.", im.ReconcileAfter-im.ReconcileBefore, after.Slug))
	} else if len(im.SyncRemoved) > 0 || res.Unlinked || res.Demoted != "" {
		im.Notes = append(im.Notes, fmt.Sprintf("Database rows are not changed here. After saving, run `spotwufamily artists reconcile --artist %s --dry-run` to see whether synced albums and tracks need to follow.", after.Slug))
	}
	return im
}

func resolveWarnings(res artists.MappingResult, im Impact, audit *artists.ProfileAuditResult) []string {
	var out []string
	if res.Disabled {
		out = append(out, "The artist has no Spotify ID left, so it will be disabled.")
	}
	if res.Enabled {
		out = append(out, "The artist will be enabled and its verified profile will be ingested by the next sync.")
	}
	if res.To == catalog.ProfileCandidate {
		out = append(out, "The profile is linked as a candidate. Sync ignores it until it is verified with independent evidence.")
	}
	if audit != nil && audit.SpotifyStatus != artists.SourceOK {
		out = append(out, "Spotify evidence was not available ("+string(audit.SpotifyStatus)+"); verification rests only on the evidence you supplied.")
	}
	if audit != nil && audit.MusicBrainz.Status != artists.SourceOK {
		out = append(out, "MusicBrainz was not compared ("+string(audit.MusicBrainz.Status)+"); that is missing evidence, not a contradiction.")
	}
	return out
}

func diffIDs(before, after []string) (added, removed []string) {
	in := func(list []string, id string) bool {
		for _, v := range list {
			if v == id {
				return true
			}
		}
		return false
	}
	for _, id := range after {
		if !in(before, id) {
			added = append(added, id)
		}
	}
	for _, id := range before {
		if !in(after, id) {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
