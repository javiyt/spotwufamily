package artists

import "context"

// Publication stages whose last successful run is checkpointed.
const (
	StageReconcile = "reconcile"
	StageSync      = "sync"
	StageSnapshot  = "snapshot"
	StageExport    = "export"
	StageRelease   = "release"
)

// ArtistFingerprint summarises the editorial inputs of one artist. Metadata is
// a hash of the non-Spotify editorial fields.
type ArtistFingerprint struct {
	Slug          string   `json:"slug"`
	SpotifyIDs    []string `json:"spotify_ids"`
	Verified      bool     `json:"verified"`
	Enabled       bool     `json:"enabled"`
	Relationships []string `json:"relationships"`
	Metadata      string   `json:"metadata"`
}

// AppliedCatalog is the catalog state last applied to the database.
type AppliedCatalog struct {
	Digest    string
	Artists   []ArtistFingerprint
	AppliedAt string
}

// StageCheckpoint records the digests a stage last completed against.
type StageCheckpoint struct {
	Stage         string
	CatalogDigest string
	DBDigest      string
	At            string
}

// ProvenanceStore persists what the database was built from.
type ProvenanceStore interface {
	AppliedCatalog(ctx context.Context) (AppliedCatalog, bool, error)
	RecordApplied(ctx context.Context, applied AppliedCatalog) error
	Checkpoint(ctx context.Context, stage string) (StageCheckpoint, bool, error)
	RecordCheckpoint(ctx context.Context, checkpoint StageCheckpoint) error
}
