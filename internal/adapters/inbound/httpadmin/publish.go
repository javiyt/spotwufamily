package httpadmin

import (
	"context"
	"net/http"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

// Publisher is optionally implemented by an Operator that has the release
// actions. Both methods must return promptly: they read and refresh a cached
// preflight in the background, never on the request.
type Publisher interface {
	// ReleaseStatus returns the cached release preflight; ok is false when
	// no release action is registered.
	ReleaseStatus() (maintenance.ReleaseStatus, bool)
	// RecheckRelease discards the cached preflight and starts a background
	// check.
	RecheckRelease()
}

// PendingProvider is optionally implemented by an Operator that can report
// the pending catalog changes and the update-website stages. Pending is
// computed from digests on request (it reads the database), so callers
// should cache it.
type PendingProvider interface {
	Pending(ctx context.Context) (maintenance.PendingView, error)
}

const (
	publishPath = "/publish"
	// releaseLogEntries is how many release log entries the page lists.
	releaseLogEntries = 10
	// defaultManifestName is shown when no release action names it.
	defaultManifestName = "data/catalog.release.json"
)

func (s *Server) registerPublish() {
	s.routes[publishPath] = route{methods: []string{http.MethodGet}, handler: s.handlePublish}
	s.routes[publishPath+"/recheck"] = route{methods: []string{http.MethodPost}, handler: s.handlePublishRecheck}
	if p, ok := s.op.(Publisher); ok {
		s.pub = p
	}
	if p, ok := s.op.(PendingProvider); ok {
		s.pend = p
		s.routes["/api/v1/pending"] = route{methods: []string{http.MethodGet}, handler: s.apiPending}
	}
}

// prereqRow is one prerequisite. The remediation is split around one code
// span so every part is escaped by the template.
type prereqRow struct {
	Name, Needed         string
	State, Class, Detail string
	FixPre, FixCode      string
	FixPost              string
}

type publishPage struct {
	Fresh freshView
	// Release is false when no release action is configured.
	Release      bool
	Checking     bool // no check has finished yet
	Rechecking   bool // a newer check is running
	CheckedAt    string
	CheckedAge   string
	CheckFailed  bool
	Prereqs      []prereqRow
	LocalShort   string
	PinnedShort  string
	LastTag      string
	Differs      string
	PreparedTag  string
	PreparedSHA  string
	PreparedNote string
	PrepareReady bool
	PrepareWhy   []string
	PublishReady bool
	PublishWhy   []string
	// DataWarning: freshness is unchecked, outdated by a catalog write, or
	// says derived data needs refreshing. It warns; it does not block.
	DataWarning  bool
	PackageName  string
	ManifestName string
	Repo         string
	Recent       []opEntryRow
	RecentErr    string
}

// reload refreshes the page (with or without JS) while a check runs.
func (p publishPage) reload() bool { return p.Release && (p.Checking || p.Rechecking) }

// shortSHA keeps the first 12 characters of a content hash.
func shortSHA(sha string) string {
	r := []rune(sha)
	if len(r) > 12 {
		return string(r[:12])
	}
	return sha
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func pass(ok bool) (state, class string) {
	if ok {
		return "PASS", "ok"
	}
	return "FAIL", "bad"
}

func prereqRows(st maintenance.ReleaseStatus) []prereqRow {
	pkg := st.PackageName
	if st.CheckFailed {
		unknown := func(name, needed string) prereqRow {
			return prereqRow{Name: name, Needed: needed, State: "UNKNOWN", Class: "warn", Detail: "unknown (the release script could not run)"}
		}
		return []prereqRow{
			{Name: "python3 and the release script", Needed: "both steps", State: "FAIL", Class: "bad", Detail: "could not run",
				FixPre: "Install python3 and run the admin from the repository checkout so ", FixCode: "scripts/automation/catalog-release.py",
				FixPost: " is found, then re-check."},
			unknown("Go available", "prepare"),
			unknown("GitHub CLI authenticated", "publish"),
			unknown("Package directory empty", "prepare"),
			unknown("Local database", "prepare"),
		}
	}
	p := st.Prereq
	rows := []prereqRow{{Name: "python3 and the release script", Needed: "both steps", State: "PASS", Class: "ok", Detail: "ran"}}

	goRow := prereqRow{Name: "Go available", Needed: "prepare", Detail: "available"}
	goRow.State, goRow.Class = pass(p.GoAvailable)
	if !p.GoAvailable {
		goRow.Detail, goRow.FixPre, goRow.FixCode, goRow.FixPost = "missing", "Install Go and make sure ", "go", " is on PATH, then re-check."
	}
	gh := prereqRow{Name: "GitHub CLI authenticated", Needed: "publish", Detail: "authenticated"}
	gh.State, gh.Class = pass(p.GhAuthenticated)
	if !p.GhAuthenticated {
		gh.Detail, gh.FixPre, gh.FixCode, gh.FixPost = "not authenticated", "Run ", "gh auth login", " in a terminal, then re-check."
	}
	dir := prereqRow{Name: "Package directory empty", Needed: "prepare", State: "PASS", Class: "ok", Detail: pkg + " is empty"}
	switch {
	case p.PackageDirClean:
	case p.PreparedTag != "":
		dir.State, dir.Class, dir.Detail = "IN USE", "warn", "holds the prepared package "+p.PreparedTag
		dir.FixPre, dir.FixCode, dir.FixPost = "Publish it in step 2, or empty or remove ", pkg, " to prepare again."
	default:
		dir.State, dir.Class, dir.Detail = "FAIL", "bad", "not empty"
		dir.FixPre, dir.FixCode, dir.FixPost = "Empty or remove ", pkg, ", then re-check."
	}
	db := prereqRow{Name: "Local database", Needed: "prepare", Detail: "found"}
	db.State, db.Class = pass(p.LocalContentSHA256 != "")
	if p.LocalContentSHA256 == "" {
		db.Detail, db.FixPre = "not found", "Run a sync, or rebuild the database from the snapshot, from Operations."
	}
	return append(rows, goRow, gh, dir, db)
}

func (s *Server) publishPageData(r *http.Request) publishPage {
	page := publishPage{Fresh: s.freshView(), ManifestName: defaultManifestName}
	f := page.Fresh
	page.DataWarning = !f.Checked || f.Stale || f.SeedNeeded || f.SeedUnknown || f.SnapshotNeeded || f.ExportNeeded || f.RebuildNeeded ||
		f.Offline || f.Unreadable
	if s.pub != nil {
		if st, ok := s.pub.ReleaseStatus(); ok {
			s.fillRelease(&page, st)
		}
	}
	entries, err := s.op.RecentOperations(r.Context(), 100)
	page.RecentErr = s.recentLogError(err)
	for _, e := range entries {
		if len(page.Recent) == releaseLogEntries {
			break
		}
		if specFor(e.Action).Guided != publishPath {
			continue
		}
		row := opEntryRow{At: rfc3339(e.At), Action: e.Action, Event: e.Kind, Outcome: e.Outcome, Summary: e.Summary}
		if jobIDPattern.MatchString(e.JobID) {
			row.JobID, row.ShortJobID = e.JobID, shortID(e.JobID)
		}
		page.Recent = append(page.Recent, row)
	}
	return page
}

func (s *Server) fillRelease(page *publishPage, st maintenance.ReleaseStatus) {
	page.Release = true
	page.PackageName, page.Repo = st.PackageName, st.Repo
	if st.ManifestName != "" {
		page.ManifestName = st.ManifestName
	}
	if st.Checked.IsZero() {
		page.Checking = true
		page.PrepareWhy, page.PublishWhy = st.PrepareBlockers, st.PublishBlockers
		return
	}
	page.Rechecking = st.Checking
	page.CheckedAt = rfc3339(st.Checked)
	page.CheckedAge = max(s.now().Sub(st.Checked), 0).Round(time.Second).String()
	page.CheckFailed = st.CheckFailed
	page.Prereqs = prereqRows(st)
	page.PrepareWhy, page.PublishWhy = st.PrepareBlockers, st.PublishBlockers
	page.PrepareReady = !st.CheckFailed && len(st.PrepareBlockers) == 0
	// Publishing needs every gate the service reports, plus the two this
	// page states (a matching prepared package and gh) checked again here.
	page.PublishReady = !st.CheckFailed && len(st.PublishBlockers) == 0 && st.PreparedMatchesLocal && st.Prereq.GhAuthenticated
	if st.CheckFailed {
		return
	}
	p := st.Prereq
	page.LocalShort, page.PinnedShort = orUnknown(shortSHA(p.LocalContentSHA256)), orUnknown(shortSHA(p.PinnedContentSHA256))
	page.LastTag = p.LastReleaseTag
	switch {
	case p.LocalContentSHA256 == "" || p.PinnedContentSHA256 == "":
		page.Differs = "unknown"
	case p.LocalContentSHA256 != p.PinnedContentSHA256:
		page.Differs = "yes: publishing would change the catalog the site serves"
	default:
		page.Differs = "no: the local content is already the pinned release"
	}
	page.PreparedTag, page.PreparedSHA = p.PreparedTag, shortSHA(p.PreparedContentSHA256)
	switch {
	case p.PreparedTag == "":
		page.PreparedNote = "No package is prepared."
	case st.PreparedMatchesLocal:
		page.PreparedNote = "The prepared package matches the local content."
	default:
		page.PreparedNote = "The prepared package does not match the local content; prepare it again."
	}
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "publish", "Publication checklist", s.publishPageData(r))
}

func (s *Server) handlePublishRecheck(w http.ResponseWriter, r *http.Request) {
	if !s.postChecked(w, r) {
		return
	}
	if s.pub == nil {
		s.refused(w, r, "unavailable", "Release actions are not configured.", publishPath)
		return
	}
	s.pub.RecheckRelease()
	s.done(w, r, "Re-checking release prerequisites in the background.", publishPath+"#prerequisites")
}
