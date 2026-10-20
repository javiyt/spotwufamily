package filesystem

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

// maxRecentEntries caps how many entries Recent returns.
const maxRecentEntries = 200

// OperationLog is an append-only JSON Lines operations log.
type OperationLog struct {
	path string
	mu   sync.Mutex
}

var _ maintenance.OperationLog = (*OperationLog)(nil)

func NewOperationLog(path string) *OperationLog { return &OperationLog{path: path} }

func (l *OperationLog) Append(ctx context.Context, e maintenance.OperationEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode operation entry: %w", err)
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	return appendJSONLine(l.path, line, "operation log")
}

// Recent returns up to n entries (at most 200), newest first. Lines that do
// not parse are skipped wherever they are.
func (l *OperationLog) Recent(ctx context.Context, n int) ([]maintenance.OperationEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n > maxRecentEntries {
		n = maxRecentEntries
	}
	if n <= 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open operation log: %w", err)
	}
	defer f.Close()

	var all []maintenance.OperationEntry
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e maintenance.OperationEntry
			if json.Unmarshal(line, &e) == nil && e.Kind != "" {
				all = append(all, e)
			}
		}
		if err != nil {
			break
		}
	}
	out := make([]maintenance.OperationEntry, 0, min(n, len(all)))
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out, nil
}
