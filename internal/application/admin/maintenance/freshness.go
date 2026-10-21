package maintenance

import "context"

// FreshnessSource supplies the digests Freshness compares. Every method that
// can be unknown returns ok=false so "absent" is explicit, never an empty
// digest standing for a missing thing.
type FreshnessSource interface {
	// CatalogDigest is the digest of the editorial catalog as it is now.
	CatalogDigest() string
	// SeededDigest is the catalog digest the database was last seeded or
	// synced from; ok=false when the database was never seeded.
	SeededDigest() (string, bool)
	// SnapshotDigest is the digest of the database content the snapshot file
	// reflects; ok=false when there is no (readable) snapshot.
	SnapshotDigest() (string, bool)
	// DatabaseState tells a missing database file (Absent) from one that
	// exists but cannot be read (Unreadable); only Absent means offline.
	DatabaseState() DatabaseState
	// DatabaseDigest is the digest of the current database content; ok=false
	// when there is no database.
	DatabaseDigest() (string, bool)
	// ExportDigest is the digest of the database content the generated export
	// reflects; it equals DatabaseDigest exactly when a fresh export would
	// change nothing. ok=false when the export is missing or unknown.
	ExportDigest() (string, bool)
	// PinnedContentSHA is the content_sha256 pinned in the published catalog
	// manifest; LocalContentSHA is the local database's content hash.
	PinnedContentSHA() (string, bool)
	LocalContentSHA() (string, bool)
}

// FreshnessSession is optionally implemented by a FreshnessSource whose reads
// are costly (the release preflight hashes the whole database). Freshness
// calls Session once per call and reads every digest from the returned
// source, so anything it caches lives for that one call only.
type FreshnessSession interface {
	Session() FreshnessSource
}

// DatabaseState is the presence of the database file.
type DatabaseState int

const (
	DatabaseAbsent DatabaseState = iota
	DatabaseReadable
	DatabaseUnreadable
)

// Freshness reports which derived data is out of date. Reasons are fixed
// strings in a fixed order and never contain paths.
type Freshness struct {
	SeedNeeded           bool
	SnapshotNeeded       bool
	ExportNeeded         bool
	RebuildNeeded        bool
	DiffersFromPublished bool
	// Offline means there is no database; that is not staleness.
	Offline bool
	// Unreadable means a database file exists but could not be read; no
	// database comparison is made and nothing is reported as fresh.
	Unreadable bool
	Reasons    []string
}

// Option configures a Service.
type Option func(*Service)

// WithFreshness gives the Service the source Freshness reads.
func WithFreshness(src FreshnessSource) Option {
	return func(s *Service) { s.freshness = src }
}

// Freshness compares the digests of the source. Rules, in reason order:
//
//   - No database (DatabaseDigest ok=false): Offline, no seed/snapshot/export
//     comparison is made (offline is not stale). RebuildNeeded is set only if
//     a snapshot exists, since the database can then be rebuilt from it.
//   - SeedNeeded: the database was never seeded or the catalog digest differs
//     from the seeded digest.
//   - SnapshotNeeded: no snapshot, or its digest differs from the database's.
//   - ExportNeeded: no export, or its digest differs from the database's.
//   - DiffersFromPublished: pinned and local content hashes are both known
//     and differ. A missing side is reported but is not drift.
func (s *Service) Freshness(ctx context.Context) (Freshness, error) {
	if s.freshness == nil {
		return Freshness{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Freshness{}, err
	}
	src := s.freshness
	if sess, ok := src.(FreshnessSession); ok {
		src = sess.Session()
	}
	var f Freshness
	add := func(r string) { f.Reasons = append(f.Reasons, r) }

	state := src.DatabaseState()
	db, haveDB := src.DatabaseDigest()
	snap, haveSnap := src.SnapshotDigest()
	switch {
	case state == DatabaseUnreadable || (state == DatabaseReadable && !haveDB):
		f.Unreadable = true
		add("Database could not be read")
	case state == DatabaseAbsent || !haveDB:
		f.Offline = true
		add("No database found")
		if haveSnap {
			f.RebuildNeeded = true
			add("Database can be rebuilt from the snapshot")
		}
	default:
		if seeded, ok := src.SeededDigest(); !ok {
			f.SeedNeeded = true
			add("Database has not been seeded from the catalog")
		} else if seeded != src.CatalogDigest() {
			f.SeedNeeded = true
			add("Catalog changed since last database seed/sync")
		}
		if !haveSnap {
			f.SnapshotNeeded = true
			add("No snapshot found")
		} else if snap != db {
			f.SnapshotNeeded = true
			add("Snapshot is older than the database")
		}
		if exp, ok := src.ExportDigest(); !ok {
			f.ExportNeeded = true
			add("No generated export found")
		} else if exp != db {
			f.ExportNeeded = true
			add("Generated export is out of date with the database")
		}
	}

	pinned, havePinned := src.PinnedContentSHA()
	local, haveLocal := src.LocalContentSHA()
	switch {
	case !havePinned:
		add("No published catalog manifest to compare against")
	case !haveLocal:
		add("Local content hash is unavailable")
	case pinned != local:
		f.DiffersFromPublished = true
		add("Local data differs from the published catalog")
	}
	return f, nil
}
