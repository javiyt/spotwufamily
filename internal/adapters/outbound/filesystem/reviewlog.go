package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/admin"
)

// ReviewLog appends profile review decisions to a JSON Lines file. The file is
// append-only: nothing here rewrites or truncates earlier entries.
type ReviewLog struct {
	path string
	mu   sync.Mutex
}

func NewReviewLog(path string) *ReviewLog { return &ReviewLog{path: path} }

type reviewLogLine struct {
	At             string   `json:"at"`
	Actor          string   `json:"actor"`
	Artist         string   `json:"artist"`
	SpotifyID      string   `json:"spotify_id"`
	Action         string   `json:"action"`
	From           string   `json:"from,omitempty"`
	To             string   `json:"to,omitempty"`
	Note           string   `json:"note,omitempty"`
	OverrideReason string   `json:"override_reason,omitempty"`
	EvidenceURLs   []string `json:"evidence_urls,omitempty"`
	IDsBefore      []string `json:"ids_before"`
	IDsAfter       []string `json:"ids_after"`
}

func (l *ReviewLog) Append(ctx context.Context, e admin.ReviewLogEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	line, err := json.Marshal(reviewLogLine{
		At: e.At.UTC().Format(time.RFC3339), Actor: e.Actor, Artist: e.Slug, SpotifyID: e.SpotifyID, Action: e.Action,
		From: e.From, To: e.To, Note: e.Note, OverrideReason: e.OverrideReason, EvidenceURLs: e.EvidenceURLs,
		IDsBefore: nonNil(e.IDsBefore), IDsAfter: nonNil(e.IDsAfter),
	})
	if err != nil {
		return fmt.Errorf("encode review log entry: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create review log directory: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open review log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write review log: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync review log: %w", err)
	}
	return f.Close()
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}
