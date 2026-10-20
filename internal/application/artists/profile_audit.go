package artists

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

const (
	profileAuditReportVersion = 1
	defaultProfileConcurrency = 4
	maxReleaseSamples         = 10
	maxTrackSampleReleases    = 2
	maxTracksPerRelease       = 3
	maxMusicBrainzSamples     = 3
)

// Source errors adapters map their transport failures to, so the audit can
// tell "the source said no" from "the source could not answer".
var (
	ErrSourceNotFound    = errors.New("source resource not found")
	ErrSourceRateLimited = errors.New("source rate limit exceeded")
	ErrSourceUnavailable = errors.New("source unavailable")
)

// SpotifyProfileSource reads the public Spotify data of one artist profile.
// GetAlbumTracks is optional and used for track samples and credits when the
// implementation provides it.
type SpotifyProfileSource interface {
	GetArtist(context.Context, string) (catalog.ArtistCandidate, error)
	GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error)
}

type spotifyTrackSource interface {
	GetAlbumTracks(context.Context, string) ([]catalog.Track, error)
}

type ProfileRole string

const (
	ProfileRolePrimary   ProfileRole = "primary"
	ProfileRoleSecondary ProfileRole = "secondary"
)

type ProfileClassification string

const (
	ClassVerified             ProfileClassification = "verified"
	ClassSuspicious           ProfileClassification = "suspicious"
	ClassAmbiguous            ProfileClassification = "ambiguous"
	ClassUnavailable          ProfileClassification = "unavailable"
	ClassInsufficientEvidence ProfileClassification = "insufficient_evidence"
)

type ProfileSeverity string

const (
	SeverityInfo    ProfileSeverity = "info"
	SeverityWarning ProfileSeverity = "warning"
	SeverityError   ProfileSeverity = "error"
)

// SourceStatus describes how a data source answered; anything other than
// SourceOK means evidence is missing, never that it is negative.
type SourceStatus string

const (
	SourceOK          SourceStatus = "ok"
	SourceNotFound    SourceStatus = "not_found"
	SourceRateLimited SourceStatus = "rate_limited"
	SourceUnavailable SourceStatus = "unavailable"
	SourceNotCached   SourceStatus = "not_cached"
	SourceOffline     SourceStatus = "offline"
)

type FindingCode string

const (
	FindingDuplicateID        FindingCode = "duplicate_id"
	FindingReleaseAnomaly     FindingCode = "release_anomaly"
	FindingGenreMismatch      FindingCode = "genre_mismatch"
	FindingProfileRenamed     FindingCode = "profile_renamed"
	FindingUnrelatedAlbums    FindingCode = "unrelated_albums"
	FindingConflictingCredits FindingCode = "conflicting_credits"
	FindingLikelyHomonym      FindingCode = "likely_homonym"
)

type ProfileFinding struct {
	Code     FindingCode     `json:"code"`
	Severity ProfileSeverity `json:"severity"`
	Detail   string          `json:"detail"`
}

type ProfileRelease struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Date string `json:"date,omitempty"`
	URL  string `json:"url,omitempty"`
}

type ProfileMusicBrainz struct {
	Status        SourceStatus `json:"status"`
	ReleaseGroups int          `json:"release_groups"`
	Matched       int          `json:"matched"`
	Unmatched     int          `json:"unmatched"`
	Samples       []string     `json:"samples"`
}

// ProfileAuditResult is the verdict on exactly one configured Spotify ID.
type ProfileAuditResult struct {
	ArtistSlug       string                     `json:"artist_slug"`
	ArtistName       string                     `json:"artist_name"`
	ArtistEnabled    bool                       `json:"artist_enabled"`
	SpotifyID        string                     `json:"spotify_id"`
	URI              string                     `json:"uri"`
	Role             ProfileRole                `json:"role"`
	ReviewStatus     catalog.ProfileStatus      `json:"review_status"`
	ProfileName      string                     `json:"profile_name,omitempty"`
	Genres           []string                   `json:"genres"`
	ConfiguredGenres []string                   `json:"configured_genres"`
	SpotifyStatus    SourceStatus               `json:"spotify_status"`
	Releases         []ProfileRelease           `json:"releases"`
	ReleaseCount     int                        `json:"release_count"`
	TrackSamples     []string                   `json:"track_samples"`
	MusicBrainz      ProfileMusicBrainz         `json:"musicbrainz"`
	Evidence         []catalog.IdentityEvidence `json:"evidence"`
	MissingEvidence  []string                   `json:"missing_evidence"`
	Contradictions   []string                   `json:"contradictions"`
	Findings         []ProfileFinding           `json:"findings"`
	Classification   ProfileClassification      `json:"classification"`
	Severity         ProfileSeverity            `json:"severity"`
	Explanation      string                     `json:"explanation"`
	CheckedOn        string                     `json:"checked_on"`
}

type ProfileAuditSummary struct {
	Total                int `json:"total"`
	Verified             int `json:"verified"`
	Suspicious           int `json:"suspicious"`
	Ambiguous            int `json:"ambiguous"`
	Unavailable          int `json:"unavailable"`
	InsufficientEvidence int `json:"insufficient_evidence"`
}

// ProfileAuditReport is the single typed report shared by the CLI renderers
// and the local web UI.
type ProfileAuditReport struct {
	Version   int                  `json:"version"`
	CheckedOn string               `json:"checked_on"`
	Offline   bool                 `json:"offline"`
	Summary   ProfileAuditSummary  `json:"summary"`
	Profiles  []ProfileAuditResult `json:"profiles"`
	// RejectedProfiles is the decision history of profiles that were rejected
	// and are no longer linked, so a replaced or quarantined mapping stays visible.
	RejectedProfiles []RejectedProfileRecord `json:"rejected_profiles,omitempty"`
}

// RejectedProfileRecord is one rejected Spotify profile with its decision evidence.
type RejectedProfileRecord struct {
	ArtistSlug string                  `json:"artist_slug"`
	ArtistName string                  `json:"artist_name"`
	SpotifyID  string                  `json:"spotify_id"`
	URI        string                  `json:"uri"`
	ReviewedAt string                  `json:"reviewed_at,omitempty"`
	Reviewer   string                  `json:"reviewer,omitempty"`
	Evidence   []RejectedEvidenceEntry `json:"evidence,omitempty"`
}

type RejectedEvidenceEntry struct {
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

type AuditProfilesOptions struct {
	CatalogPath string
	ArtistSlug  string
	// Offline never calls MusicBrainz and expects the Spotify source to answer
	// from cache only (ErrCacheMiss means "not cached").
	Offline     bool
	Concurrency int
	Now         func() time.Time
	Progress    func(done, total int, result ProfileAuditResult)
}

type AuditProfiles struct {
	store       CatalogStore
	spotify     SpotifyProfileSource
	musicBrainz MusicBrainzReleaseGroupSearcher
}

func NewAuditProfiles(store CatalogStore, spotify SpotifyProfileSource, musicBrainz MusicBrainzReleaseGroupSearcher) AuditProfiles {
	return AuditProfiles{store: store, spotify: spotify, musicBrainz: musicBrainz}
}

type profileJob struct {
	artist catalog.Artist
	id     string
	role   ProfileRole
}

// Run audits every configured Spotify ID individually. It never writes the
// catalog and never refuses an invalid one, because duplicate IDs are
// something the audit reports.
func (a AuditProfiles) Run(ctx context.Context, options AuditProfilesOptions) (ProfileAuditReport, error) {
	c, err := a.store.Load(ctx, options.CatalogPath)
	if err != nil {
		return ProfileAuditReport{}, fmt.Errorf("load artist catalog: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	checked := now().UTC()

	jobs := profileJobs(c, options.ArtistSlug)
	rejected := rejectedProfiles(c, options.ArtistSlug)
	if options.ArtistSlug != "" && len(jobs) == 0 && len(rejected) == 0 {
		return ProfileAuditReport{}, fmt.Errorf("artist %q with Spotify IDs not found", options.ArtistSlug)
	}

	run := &profileAuditRun{
		ctx:       ctx,
		audit:     a,
		catalog:   c,
		options:   options,
		checked:   checked,
		owners:    profileOwners(c),
		verified:  verifiedProfileOwners(c),
		spotify:   map[string]*spotifyProfileEntry{},
		mb:        map[string]*musicBrainzEntry{},
		totalJobs: len(jobs),
	}

	results := make([]ProfileAuditResult, len(jobs))
	workers := options.Concurrency
	if workers <= 0 {
		workers = defaultProfileConcurrency
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, job := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, job profileJob) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = run.evaluate(job)
			run.reportProgress(results[i])
		}(i, job)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return ProfileAuditReport{}, err
	}

	report := ProfileAuditReport{
		Version:   profileAuditReportVersion,
		CheckedOn: checked.Format(time.DateOnly),
		Offline:   options.Offline,
		Profiles:  results,

		RejectedProfiles: rejected,
	}
	report.Summary = summarizeProfileAudit(results)
	return report, nil
}

// EvaluateProfile audits one Spotify ID for an artist as if it were linked,
// without reading or writing the store. It lets a reviewer see the same
// evidence for a candidate they are about to accept as for an attached ID.
func (a AuditProfiles) EvaluateProfile(ctx context.Context, c catalog.EditorialCatalog, slug, id string, role ProfileRole, options AuditProfilesOptions) (ProfileAuditResult, error) {
	index := c.FindArtist(slug)
	if index < 0 {
		return ProfileAuditResult{}, fmt.Errorf("artist %q not found", slug)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	owners := profileOwners(c)
	if !contains(owners[id], slug) {
		owners[id] = append(owners[id], slug)
	}
	run := &profileAuditRun{
		ctx:       ctx,
		audit:     a,
		catalog:   c,
		options:   options,
		checked:   now().UTC(),
		owners:    owners,
		verified:  verifiedProfileOwners(c),
		spotify:   map[string]*spotifyProfileEntry{},
		mb:        map[string]*musicBrainzEntry{},
		totalJobs: 1,
	}
	result := run.evaluate(profileJob{artist: c.Artists[index], id: id, role: role})
	if err := ctx.Err(); err != nil {
		return ProfileAuditResult{}, err
	}
	return result, nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// rejectedProfiles lists the rejected, unlinked profiles in editorial order.
func rejectedProfiles(c catalog.EditorialCatalog, slug string) []RejectedProfileRecord {
	var out []RejectedProfileRecord
	for _, artist := range c.Artists {
		if slug != "" && artist.Slug != slug {
			continue
		}
		for _, profile := range artist.SpotifyProfiles {
			if profile.Status != catalog.ProfileRejected || artist.HasSpotifyID(profile.ID) {
				continue
			}
			record := RejectedProfileRecord{
				ArtistSlug: artist.Slug,
				ArtistName: artist.Name,
				SpotifyID:  profile.ID,
				URI:        "spotify:artist:" + profile.ID,
				ReviewedAt: profile.ReviewedAt,
				Reviewer:   profile.Reviewer,
			}
			for _, e := range profile.Evidence {
				record.Evidence = append(record.Evidence, RejectedEvidenceEntry{Type: string(e.Type), URL: e.URL, Rationale: e.Rationale})
			}
			out = append(out, record)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ArtistSlug != out[j].ArtistSlug {
			return out[i].ArtistSlug < out[j].ArtistSlug
		}
		return out[i].SpotifyID < out[j].SpotifyID
	})

	return out
}

func profileJobs(c catalog.EditorialCatalog, slug string) []profileJob {
	artists := append([]catalog.Artist(nil), c.Artists...)
	sort.SliceStable(artists, func(i, j int) bool {
		if artists[i].EditorialOrder != artists[j].EditorialOrder {
			return artists[i].EditorialOrder < artists[j].EditorialOrder
		}
		return artists[i].Slug < artists[j].Slug
	})
	var jobs []profileJob
	for _, artist := range artists {
		if slug != "" && artist.Slug != slug {
			continue
		}
		for i, id := range artist.AllSpotifyIDs() {
			role := ProfileRoleSecondary
			if i == 0 {
				role = ProfileRolePrimary
			}
			jobs = append(jobs, profileJob{artist: artist, id: id, role: role})
		}
	}
	return jobs
}

func profileOwners(c catalog.EditorialCatalog) map[string][]string {
	owners := map[string][]string{}
	for _, artist := range c.Artists {
		for _, id := range artist.AllSpotifyIDs() {
			owners[id] = append(owners[id], artist.Slug)
		}
	}
	return owners
}

func verifiedProfileOwners(c catalog.EditorialCatalog) map[string]string {
	verified := map[string]string{}
	for _, artist := range c.Artists {
		for _, id := range artist.AllSpotifyIDs() {
			if artist.StatusOf(id) == catalog.ProfileVerified {
				verified[id] = artist.Slug
			}
		}
	}
	return verified
}

func summarizeProfileAudit(results []ProfileAuditResult) ProfileAuditSummary {
	summary := ProfileAuditSummary{Total: len(results)}
	for _, r := range results {
		switch r.Classification {
		case ClassVerified:
			summary.Verified++
		case ClassSuspicious:
			summary.Suspicious++
		case ClassAmbiguous:
			summary.Ambiguous++
		case ClassUnavailable:
			summary.Unavailable++
		default:
			summary.InsufficientEvidence++
		}
	}
	return summary
}

// spotifyProfileEntry memoises everything fetched for one Spotify ID, so a
// duplicate ID configured twice costs a single set of requests.
type spotifyProfileEntry struct {
	once        sync.Once
	profile     catalog.ArtistCandidate
	profileSt   SourceStatus
	albums      []catalog.Release
	albumsSt    SourceStatus
	trackSample []string
	trackCredit map[string]struct{}
}

type musicBrainzEntry struct {
	once   sync.Once
	groups []MusicBrainzReleaseGroup
	status SourceStatus
}

type profileAuditRun struct {
	ctx       context.Context
	audit     AuditProfiles
	catalog   catalog.EditorialCatalog
	options   AuditProfilesOptions
	checked   time.Time
	owners    map[string][]string
	verified  map[string]string
	totalJobs int

	mu      sync.Mutex
	spotify map[string]*spotifyProfileEntry
	mb      map[string]*musicBrainzEntry
	done    int

	rateLimited atomic.Bool
}

func (r *profileAuditRun) reportProgress(result ProfileAuditResult) {
	if r.options.Progress == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	r.options.Progress(r.done, r.totalJobs, result)
}

func sourceStatusOf(err error) SourceStatus {
	switch {
	case err == nil:
		return SourceOK
	case errors.Is(err, ErrSourceNotFound):
		return SourceNotFound
	case errors.Is(err, ErrSourceRateLimited):
		return SourceRateLimited
	case errors.Is(err, ErrCacheMiss):
		return SourceNotCached
	default:
		return SourceUnavailable
	}
}

func (r *profileAuditRun) spotifyEntry(id string) *spotifyProfileEntry {
	r.mu.Lock()
	entry, ok := r.spotify[id]
	if !ok {
		entry = &spotifyProfileEntry{}
		r.spotify[id] = entry
	}
	r.mu.Unlock()

	entry.once.Do(func() { r.fetchSpotify(id, entry) })
	return entry
}

func (r *profileAuditRun) fetchSpotify(id string, entry *spotifyProfileEntry) {
	if r.rateLimited.Load() {
		entry.profileSt, entry.albumsSt = SourceRateLimited, SourceRateLimited
		return
	}
	source := r.audit.spotify
	profile, err := source.GetArtist(r.ctx, id)
	entry.profile, entry.profileSt = profile, sourceStatusOf(err)
	switch entry.profileSt {
	case SourceRateLimited:
		r.rateLimited.Store(true)
		entry.albumsSt = SourceRateLimited
		return
	case SourceNotFound, SourceUnavailable:
		entry.albumsSt = entry.profileSt
		return
	}
	// A cache miss on the profile metadata still allows cached albums.

	if r.rateLimited.Load() {
		entry.albumsSt = SourceRateLimited
		return
	}
	albums, err := source.GetArtistAlbums(r.ctx, id, []string{"album"})
	entry.albums, entry.albumsSt = albums, sourceStatusOf(err)
	if entry.albumsSt == SourceRateLimited {
		r.rateLimited.Store(true)
		return
	}
	if entry.albumsSt != SourceOK {
		return
	}
	r.sampleTracks(id, entry)
}

func (r *profileAuditRun) sampleTracks(id string, entry *spotifyProfileEntry) {
	tracks, ok := r.audit.spotify.(spotifyTrackSource)
	if !ok || r.rateLimited.Load() {
		return
	}
	albums := auditedSpotifyAlbums(entry.albums, id)
	entry.trackCredit = map[string]struct{}{}
	for i, album := range albums {
		if i >= maxTrackSampleReleases {
			break
		}
		list, err := tracks.GetAlbumTracks(r.ctx, album.ID)
		if err != nil {
			if errors.Is(err, ErrSourceRateLimited) {
				r.rateLimited.Store(true)
			}
			continue
		}
		for j, track := range list {
			if j < maxTracksPerRelease {
				entry.trackSample = append(entry.trackSample, album.Title+" - "+track.Name)
			}
			for _, credit := range track.Artists {
				entry.trackCredit[credit.SpotifyID] = struct{}{}
			}
		}
	}
}

func (r *profileAuditRun) musicBrainzEntry(artist catalog.Artist) *musicBrainzEntry {
	r.mu.Lock()
	entry, ok := r.mb[artist.Slug]
	if !ok {
		entry = &musicBrainzEntry{}
		r.mb[artist.Slug] = entry
	}
	r.mu.Unlock()

	entry.once.Do(func() {
		if r.options.Offline {
			entry.status = SourceOffline
			return
		}
		groups, err := r.audit.musicBrainz.SearchArtistAlbumReleaseGroups(r.ctx, artist)
		if err != nil {
			entry.status = SourceUnavailable
			return
		}
		entry.groups, entry.status = groups, SourceOK
	})
	return entry
}

func auditedSpotifyAlbums(releases []catalog.Release, sourceID string) []AuditedAlbum {
	seen := map[string]struct{}{}
	albums := make([]AuditedAlbum, 0, len(releases))
	for _, release := range releases {
		if release.SpotifyID == "" {
			continue
		}
		if _, ok := seen[release.SpotifyID]; ok {
			continue
		}
		seen[release.SpotifyID] = struct{}{}
		albums = append(albums, AuditedAlbum{
			ID: release.SpotifyID, Title: release.Name, Date: release.ReleaseDate,
			Year: yearFromDate(release.ReleaseDate), URL: release.URL, SourceID: sourceID,
		})
	}
	sortAlbums(albums)
	return albums
}

type profileEvaluation struct {
	result   ProfileAuditResult
	positive bool
	negative bool
}

func (e *profileEvaluation) addEvidence(item catalog.IdentityEvidence) {
	e.result.Evidence = append(e.result.Evidence, item)
	if item.IsIndependentProof() {
		e.positive = true
	}
	if item.Polarity == catalog.EvidenceNegative && item.Strong {
		e.negative = true
		e.result.Contradictions = append(e.result.Contradictions, item.Detail)
	}
}

func (e *profileEvaluation) addFinding(code FindingCode, severity ProfileSeverity, detail string) {
	e.result.Findings = append(e.result.Findings, ProfileFinding{Code: code, Severity: severity, Detail: detail})
}

func (r *profileAuditRun) evaluate(job profileJob) ProfileAuditResult {
	artist := job.artist
	eval := &profileEvaluation{result: ProfileAuditResult{
		ArtistSlug:       artist.Slug,
		ArtistName:       artist.Name,
		ArtistEnabled:    artist.Enabled,
		SpotifyID:        job.id,
		URI:              "spotify:artist:" + job.id,
		Role:             job.role,
		ReviewStatus:     artist.StatusOf(job.id),
		Genres:           []string{},
		ConfiguredGenres: append([]string{}, artist.Genres...),
		Releases:         []ProfileRelease{},
		TrackSamples:     []string{},
		MusicBrainz:      ProfileMusicBrainz{Samples: []string{}},
		Evidence:         []catalog.IdentityEvidence{},
		MissingEvidence:  []string{},
		Contradictions:   []string{},
		Findings:         []ProfileFinding{},
		CheckedOn:        r.checked.Format(time.DateOnly),
	}}

	if owners := r.owners[job.id]; len(owners) > 1 {
		eval.addFinding(FindingDuplicateID, SeverityError, "Spotify ID is configured for several artists: "+strings.Join(owners, ", "))
	}

	spotify := r.spotifyEntry(job.id)
	eval.result.SpotifyStatus = spotifyStatusOf(spotify)
	eval.result.ProfileName = spotify.profile.Name
	if len(spotify.profile.Genres) > 0 {
		eval.result.Genres = append([]string{}, spotify.profile.Genres...)
	}
	eval.result.TrackSamples = append(eval.result.TrackSamples, spotify.trackSample...)

	if eval.result.SpotifyStatus != SourceOK {
		eval.result.MissingEvidence = append(eval.result.MissingEvidence, "Spotify profile and releases ("+string(eval.result.SpotifyStatus)+")")
		return finishProfile(eval, artist)
	}

	r.checkProfileName(eval, artist)
	r.checkGenres(eval, artist)
	r.checkReleases(eval, job, spotify)
	return finishProfile(eval, artist)
}

func spotifyStatusOf(entry *spotifyProfileEntry) SourceStatus {
	if entry.albumsSt != SourceOK {
		return entry.albumsSt
	}
	return SourceOK
}

func (r *profileAuditRun) checkProfileName(eval *profileEvaluation, artist catalog.Artist) {
	name := eval.result.ProfileName
	if name == "" {
		return
	}
	known := []string{artist.Name, artist.PublicName}
	known = append(known, artist.Aliases...)
	for _, candidate := range known {
		if candidate != "" && normalizeProfileName(candidate) == normalizeProfileName(name) {
			eval.result.Evidence = append(eval.result.Evidence, catalog.IdentityEvidence{
				Kind: catalog.EvidenceNameSimilarity, Polarity: catalog.EvidencePositive, Source: "spotify",
				Detail: "profile name matches the configured name; a name alone is not identity evidence",
			})
			return
		}
	}
	eval.addFinding(FindingProfileRenamed, SeverityWarning, fmt.Sprintf("Spotify profile is named %q, not %q or a known alias", name, artist.Name))
}

func (r *profileAuditRun) checkGenres(eval *profileEvaluation, artist catalog.Artist) {
	if len(eval.result.Genres) == 0 || len(artist.Genres) == 0 || genresOverlap(artist.Genres, eval.result.Genres) {
		return
	}
	detail := fmt.Sprintf("profile genres [%s] share nothing with configured genres [%s]; legitimate cross-genre collaborations exist, so this is a warning only",
		strings.Join(eval.result.Genres, ", "), strings.Join(artist.Genres, ", "))
	eval.addFinding(FindingGenreMismatch, SeverityWarning, detail)
	eval.result.Evidence = append(eval.result.Evidence, catalog.IdentityEvidence{
		Kind: catalog.EvidenceGenre, Polarity: catalog.EvidenceWarning, Source: "spotify", Detail: detail,
	})
}

func (r *profileAuditRun) checkReleases(eval *profileEvaluation, job profileJob, spotify *spotifyProfileEntry) {
	albums := auditedSpotifyAlbums(spotify.albums, job.id)
	eval.result.ReleaseCount = len(albums)
	for i, album := range albums {
		if i >= maxReleaseSamples {
			break
		}
		eval.result.Releases = append(eval.result.Releases, ProfileRelease{ID: album.ID, Name: album.Title, Date: album.Date, URL: album.URL})
	}

	r.checkReleaseAnomalies(eval, job, spotify.albums)
	r.checkTrackCredits(eval, job, spotify)

	if len(albums) == 0 {
		eval.result.MissingEvidence = append(eval.result.MissingEvidence, "Spotify releases (the profile has no albums)")
		return
	}

	mb := r.musicBrainzEntry(job.artist)
	eval.result.MusicBrainz.Status = mb.status
	if mb.status != SourceOK {
		eval.result.MissingEvidence = append(eval.result.MissingEvidence, "MusicBrainz release comparison ("+string(mb.status)+")")
		if mb.status == SourceUnavailable {
			eval.result.Evidence = append(eval.result.Evidence, catalog.IdentityEvidence{
				Kind: catalog.EvidenceMusicBrainzUnavailable, Polarity: catalog.EvidenceWarning, Source: "musicbrainz",
				Detail: "MusicBrainz could not be queried; this is missing evidence, not a contradiction",
			})
		}
		return
	}
	mbAlbums := auditedMusicBrainzAlbums(mb.groups)
	eval.result.MusicBrainz.ReleaseGroups = len(mbAlbums)
	for i, album := range mbAlbums {
		if i >= maxMusicBrainzSamples {
			break
		}
		eval.result.MusicBrainz.Samples = append(eval.result.MusicBrainz.Samples, album.URL)
	}
	if len(mbAlbums) == 0 {
		eval.result.MissingEvidence = append(eval.result.MissingEvidence, "MusicBrainz release groups (none found for this artist name)")
		return
	}

	matched, _, unmatched := compareAlbums(albums, mbAlbums)
	eval.result.MusicBrainz.Matched = len(matched)
	eval.result.MusicBrainz.Unmatched = len(unmatched)
	if len(matched) > 0 {
		first := matched[0]
		eval.addEvidence(catalog.IdentityEvidence{
			Kind: catalog.EvidenceMusicBrainzRelease, Polarity: catalog.EvidencePositive, Strong: true, Source: "musicbrainz",
			URL: first.MusicBrainz.URL, ReleaseName: first.MusicBrainz.Title, ReleaseYear: first.MusicBrainz.Year,
			Detail: fmt.Sprintf("%d of %d Spotify albums match MusicBrainz release groups", len(matched), len(albums)),
		})
		if len(unmatched) > len(matched) {
			eval.addFinding(FindingUnrelatedAlbums, SeverityWarning, fmt.Sprintf("%d of %d albums match no known release group", len(unmatched), len(albums)))
		}
		return
	}
	detail := fmt.Sprintf("none of the %d Spotify albums match the %d known MusicBrainz release groups", len(albums), len(mbAlbums))
	eval.addEvidence(catalog.IdentityEvidence{
		Kind: catalog.EvidenceReleaseMismatch, Polarity: catalog.EvidenceNegative, Strong: true, Source: "musicbrainz", Detail: detail,
	})
	eval.addFinding(FindingUnrelatedAlbums, SeverityError, detail)
}

func (r *profileAuditRun) checkReleaseAnomalies(eval *profileEvaluation, job profileJob, releases []catalog.Release) {
	var anomalies, conflicting []string
	for _, release := range releases {
		label := release.Name
		if label == "" {
			label = release.SpotifyID
		}
		switch {
		case strings.TrimSpace(release.Name) == "":
			anomalies = append(anomalies, release.SpotifyID+": empty title")
		case !validReleaseDate(release.ReleaseDate):
			anomalies = append(anomalies, fmt.Sprintf("%s: invalid release date %q", label, release.ReleaseDate))
		case releaseDateAfter(release.ReleaseDate, r.checked):
			anomalies = append(anomalies, fmt.Sprintf("%s: release date %s is in the future", label, release.ReleaseDate))
		}
		if len(release.Artists) > 0 && !creditsInclude(release.Artists, job.id) {
			conflicting = append(conflicting, label)
		}
	}
	sort.Strings(anomalies)
	sort.Strings(conflicting)
	if len(anomalies) > 0 {
		eval.addFinding(FindingReleaseAnomaly, SeverityWarning, strings.Join(anomalies, "; "))
	}
	if len(conflicting) > 0 {
		eval.addFinding(FindingConflictingCredits, SeverityWarning, "releases credited to other artists only: "+strings.Join(conflicting, ", "))
	}
}

// checkTrackCredits treats a credit next to a profile that is itself verified
// for a different artist as independent evidence. Names and unverified
// membership never count.
func (r *profileAuditRun) checkTrackCredits(eval *profileEvaluation, job profileJob, spotify *spotifyProfileEntry) {
	ids := make([]string, 0, len(spotify.trackCredit))
	for id := range spotify.trackCredit {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		owner, ok := r.verified[id]
		if !ok || owner == job.artist.Slug {
			continue
		}
		eval.addEvidence(catalog.IdentityEvidence{
			Kind: catalog.EvidenceTrackCredit, Polarity: catalog.EvidencePositive, Strong: true, Source: "spotify",
			URL: "https://open.spotify.com/artist/" + id, SubjectID: id,
			Detail: "credited on tracks next to the verified profile of " + owner,
		})
		return
	}
}

func finishProfile(eval *profileEvaluation, artist catalog.Artist) ProfileAuditResult {
	result := &eval.result
	if profile, ok := artist.ProfileFor(result.SpotifyID); ok && profile.Status == catalog.ProfileVerified && hasIndependentEvidence(profile.Evidence) {
		eval.positive = true
		result.Evidence = append(result.Evidence, catalog.IdentityEvidence{
			Kind: catalog.EvidenceExternalAuthority, Polarity: catalog.EvidencePositive, Strong: true, Source: "editorial_review",
			Detail: "manually verified by " + profile.Reviewer + " with independent evidence",
		})
	}

	renamed := hasFinding(result.Findings, FindingProfileRenamed)
	if eval.negative && renamed {
		result.Findings = upgradeFinding(result.Findings, FindingProfileRenamed, SeverityError, "; profile was renamed and its releases are unrelated, so it may have been taken over")
	}
	if eval.negative && !eval.positive && !renamed && nameMatchesArtist(result.ProfileName, artist) {
		eval.addFinding(FindingLikelyHomonym, SeverityWarning, "the profile has the same name as the artist but unrelated releases; an identical name is not identity evidence")
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].Code != result.Findings[j].Code {
			return result.Findings[i].Code < result.Findings[j].Code
		}
		return result.Findings[i].Detail < result.Findings[j].Detail
	})

	switch {
	case result.SpotifyStatus == SourceNotFound:
		result.Classification, result.Severity = ClassUnavailable, SeverityWarning
		result.Explanation = "Spotify no longer serves this profile (removed or never existed); it is not treated as wrong."
	case result.SpotifyStatus == SourceRateLimited || result.SpotifyStatus == SourceUnavailable:
		result.Classification, result.Severity = ClassUnavailable, SeverityWarning
		result.Explanation = "Spotify could not be queried (" + string(result.SpotifyStatus) + "); no verdict was reached."
	case hasFinding(result.Findings, FindingDuplicateID):
		result.Classification, result.Severity = ClassAmbiguous, SeverityError
		result.Explanation = "The same Spotify ID is configured for several artists, so ownership is ambiguous."
	case eval.positive && eval.negative:
		result.Classification, result.Severity = ClassAmbiguous, SeverityError
		result.Explanation = "Independent evidence supports this profile, but its releases also contradict it; review manually."
	case eval.negative:
		result.Classification, result.Severity = ClassSuspicious, SeverityError
		result.Explanation = "Releases contradict the artist: " + strings.Join(result.Contradictions, "; ") + "."
	case eval.positive:
		result.Classification, result.Severity = ClassVerified, SeverityInfo
		result.Explanation = "Independent evidence supports this profile."
		if len(result.Findings) > 0 {
			result.Severity = SeverityWarning
			result.Explanation += " Warnings are listed."
		}
	default:
		result.Classification, result.Severity = ClassInsufficientEvidence, SeverityInfo
		result.Explanation = "Not enough independent evidence to verify or contradict this profile."
		if len(result.MissingEvidence) > 0 {
			result.Severity = SeverityWarning
			result.Explanation += " Missing: " + strings.Join(result.MissingEvidence, "; ") + "."
		}
	}
	return *result
}

func hasIndependentEvidence(evidence []catalog.Evidence) bool {
	for _, item := range evidence {
		if item.Independent() {
			return true
		}
	}
	return false
}

func hasFinding(findings []ProfileFinding, code FindingCode) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func upgradeFinding(findings []ProfileFinding, code FindingCode, severity ProfileSeverity, suffix string) []ProfileFinding {
	for i := range findings {
		if findings[i].Code == code {
			findings[i].Severity = severity
			findings[i].Detail += suffix
		}
	}
	return findings
}

func nameMatchesArtist(name string, artist catalog.Artist) bool {
	if name == "" {
		return false
	}
	for _, known := range append([]string{artist.Name, artist.PublicName}, artist.Aliases...) {
		if known != "" && normalizeProfileName(known) == normalizeProfileName(name) {
			return true
		}
	}
	return false
}

func normalizeProfileName(name string) string {
	return strings.Join(strings.Fields(nonAlphaNumericPattern.ReplaceAllString(strings.ToLower(name), " ")), "")
}

func genresOverlap(configured, profile []string) bool {
	for _, a := range configured {
		a = strings.ToLower(strings.TrimSpace(a))
		for _, b := range profile {
			b = strings.ToLower(strings.TrimSpace(b))
			if a != "" && b != "" && (a == b || strings.Contains(a, b) || strings.Contains(b, a)) {
				return true
			}
		}
	}
	return false
}

func creditsInclude(credits []catalog.ArtistCandidate, id string) bool {
	for _, credit := range credits {
		if credit.SpotifyID == id {
			return true
		}
	}
	return false
}

func validReleaseDate(date string) bool {
	for _, layout := range []string{time.DateOnly, "2006-01", "2006"} {
		if _, err := time.Parse(layout, date); err == nil {
			return true
		}
	}
	return false
}

func releaseDateAfter(date string, now time.Time) bool {
	for _, layout := range []string{time.DateOnly, "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, date); err == nil {
			return parsed.After(now)
		}
	}
	return false
}
