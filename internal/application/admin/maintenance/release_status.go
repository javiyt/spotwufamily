package maintenance

import (
	"slices"
	"time"
)

// ReleaseStatus is the cached release preflight the publication checklist
// shows. It never comes from a preflight run on the caller's goroutine.
type ReleaseStatus struct {
	// Prereq is the last preflight report; zero until Checked is set or when
	// CheckFailed.
	Prereq Prereq
	// CheckFailed means the last check could not run: python3 or the
	// release script is missing, it failed, or it timed out.
	CheckFailed bool
	// Checked is when the last check ended; zero while none has.
	Checked time.Time
	// Checking is true while a background check runs.
	Checking bool
	// LastPrepared is the content hash recorded by the latest successful
	// release-prepare, "" when there is none.
	LastPrepared string
	// PreparedMatchesLocal is true when that record and the verified package
	// on disk both hold the current local content.
	PreparedMatchesLocal bool
	// PrepareBlockers and PublishBlockers are fixed reasons why the action
	// cannot run now, in a fixed order; empty means ready. The preview and
	// the run re-check everything with a fresh preflight regardless.
	PrepareBlockers []string
	PublishBlockers []string
	// Display names, never paths.
	Repo, PackageName, ManifestName string
}

// releaseStatuser is implemented by the release actions.
type releaseStatuser interface {
	releaseStatus() ReleaseStatus
	forceRecheck()
}

// ReleaseStatus returns the cached release preflight, starting a background
// check (single-flight) when there is none or it expired. It returns
// promptly even while the GitHub CLI or the release script hangs. ok is
// false when no release action is registered.
func (s *Service) ReleaseStatus() (ReleaseStatus, bool) {
	a := s.releaseSource()
	if a == nil {
		return ReleaseStatus{}, false
	}
	return a.releaseStatus(), true
}

// RecheckRelease discards the cached preflight of every release action and
// starts a new background check for the status source; it never waits.
func (s *Service) RecheckRelease() {
	s.invalidateRelease()
	if a := s.releaseSource(); a != nil {
		a.releaseStatus()
	}
}

// invalidateRelease discards every release action's cached check without
// starting one; the next listing or status starts it.
func (s *Service) invalidateRelease() {
	for _, a := range s.actions {
		if r, ok := a.(releaseStatuser); ok {
			r.forceRecheck()
		}
	}
}

// releaseSource prefers release-publish (its cache is the one the publish
// gate reads), else release-prepare.
func (s *Service) releaseSource() releaseStatuser {
	var found releaseStatuser
	for _, a := range s.actions {
		if r, ok := a.(releaseStatuser); ok {
			if a.Name() == "release-publish" {
				return r
			}
			if found == nil {
				found = r
			}
		}
	}
	return found
}

func (a *releaseAction) snapshot() (ReleaseStatus, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ReleaseStatus{
		Prereq: a.cached, CheckFailed: a.cacheErr != nil, Checked: a.cachedAt, Checking: a.refreshing,
		Repo: a.deps.Repo, PackageName: a.deps.PackageName, ManifestName: a.deps.ManifestName,
	}, a.cachedPrepared, a.cachedPrepOK
}

func (a *releaseAction) releaseStatus() ReleaseStatus {
	a.kick()
	st, prepared, prepOK := a.snapshot()
	st.Prereq.PreparedAssets = slices.Clone(st.Prereq.PreparedAssets)

	switch {
	case st.Checked.IsZero():
		st.PrepareBlockers = []string{releaseCheckingReason}
		st.PublishBlockers = []string{releaseCheckingReason}
		return st
	case st.CheckFailed:
		st.Prereq = Prereq{}
		st.PrepareBlockers = []string{releaseScriptReason}
		st.PublishBlockers = []string{releaseScriptReason}
		return st
	}
	if prepOK {
		st.LastPrepared = prepared
	}
	if reason := a.prepareReason(st.Prereq); reason != "" {
		st.PrepareBlockers = []string{reason}
	}
	if !st.Prereq.GhAuthenticated {
		st.PublishBlockers = append(st.PublishBlockers, ghNotAuthenticatedReason)
	}
	if publishBlockedBy(st.Prereq, prepared, prepOK) != nil {
		st.PublishBlockers = append(st.PublishBlockers, nothingPreparedWarning)
	} else {
		st.PreparedMatchesLocal = true
	}
	return st
}
