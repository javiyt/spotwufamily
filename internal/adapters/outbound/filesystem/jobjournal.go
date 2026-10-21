package filesystem

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// JobJournal persists job snapshots as append-only JSON Lines. The last record
// for a job ID wins when loading.
type JobJournal struct {
	path string
	mu   sync.Mutex
}

var _ jobs.Store = (*JobJournal)(nil)

func NewJobJournal(path string) *JobJournal { return &JobJournal{path: path} }

func (j *JobJournal) Save(ctx context.Context, job jobs.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	line, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	line = append(line, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()
	return appendJSONLine(j.path, line, "job journal")
}

// Load returns the latest snapshot per job ID in first-seen order. Lines that
// do not parse (for example a truncated trailing write) are skipped.
func (j *JobJournal) Load(ctx context.Context) ([]jobs.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.Open(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open job journal: %w", err)
	}
	defer f.Close()

	var order []string
	latest := map[string]jobs.Job{}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var job jobs.Job
			if json.Unmarshal(line, &job) == nil && job.ID != "" {
				if _, seen := latest[job.ID]; !seen {
					order = append(order, job.ID)
				}
				latest[job.ID] = job
			}
		}
		if err != nil {
			break
		}
	}
	out := make([]jobs.Job, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out, nil
}
