package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const genericFailure = "operation failed"

// ErrConflict is returned when a requested resource is held by another job.
var ErrConflict = errors.New("resource is in use by another job")

// ErrNotFound is returned when a job does not exist.
var ErrNotFound = errors.New("job not found")

// EditHolder is the HolderID of a ConflictError raised because a
// synchronous edit (see Manager.Acquire) holds the resource.
const EditHolder = "catalog-edit"

// ConflictError identifies the job holding a contested resource.
type ConflictError struct{ HolderID string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: job %s", ErrConflict, e.HolderID)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// Option customises a Manager.
type Option func(*Manager)

// WithSanitizer overrides how job errors are turned into user-visible text.
func WithSanitizer(f func(error) string) Option {
	return func(m *Manager) {
		if f != nil {
			m.sanitize = f
		}
	}
}

// WithOnFinish registers a callback invoked once when a job reaches a terminal
// status after running.
func WithOnFinish(f func(Job)) Option {
	return func(m *Manager) { m.onFinish = f }
}

// Manager runs jobs in the background, one active job per resource.
type Manager struct {
	store    Store
	now      func() time.Time
	newID    func() string
	sanitize func(error) string
	onFinish func(Job)

	mu      sync.Mutex
	jobs    map[string]*Job
	order   []string
	locks   map[string]string
	edits   map[string]int // resources held by Acquire, with their holder count
	cancels map[string]context.CancelFunc
	done    map[string]chan struct{}
}

// NewManager builds a Manager persisting through store.
func NewManager(store Store, now func() time.Time, newID func() string, opts ...Option) *Manager {
	m := &Manager{
		store:    store,
		now:      now,
		newID:    newID,
		sanitize: defaultSanitize,
		jobs:     map[string]*Job{},
		locks:    map[string]string{},
		edits:    map[string]int{},
		cancels:  map[string]context.CancelFunc{},
		done:     map[string]chan struct{}{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func defaultSanitize(err error) string {
	var s interface{ Safe() string }
	if errors.As(err, &s) {
		return s.Safe()
	}
	return genericFailure
}

// Submit starts a job, or returns a *ConflictError if a resource is held.
func (m *Manager) Submit(ctx context.Context, s Spec) (Job, error) {
	if s.Run == nil {
		return Job{}, errors.New("job has no function to run")
	}
	resources := dedupe(s.Resources)

	m.mu.Lock()
	for _, r := range resources {
		if holder, held := m.locks[r]; held {
			m.mu.Unlock()
			return Job{}, &ConflictError{HolderID: holder}
		}
		if m.edits[r] > 0 {
			m.mu.Unlock()
			return Job{}, &ConflictError{HolderID: EditHolder}
		}
	}
	job := &Job{
		ID:        m.newID(),
		Kind:      s.Kind,
		Status:    StatusQueued,
		Counts:    map[string]int{},
		Resources: resources,
		ResumeOf:  s.ResumeOf,
		CreatedAt: m.now(),
	}
	if err := m.store.Save(ctx, snapshot(job)); err != nil {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("persist job: %w", err)
	}
	for _, r := range resources {
		m.locks[r] = job.ID
	}
	m.jobs[job.ID] = job
	m.order = append(m.order, job.ID)
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancels[job.ID] = cancel
	done := make(chan struct{})
	m.done[job.ID] = done
	snap := snapshot(job)
	m.mu.Unlock()

	go m.run(runCtx, job.ID, s.Run, done)
	return snap, nil
}

// Acquire holds resources for a synchronous edit made outside any job (for
// example a catalog save from the editor) until the returned release is
// called. It fails with a *ConflictError when a job holds one of them; while
// held, Submit refuses jobs needing them. Edits share resources with each
// other (they serialise on their own file lock). Release is idempotent.
func (m *Manager) Acquire(resources ...string) (release func(), err error) {
	resources = dedupe(resources)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range resources {
		if holder, held := m.locks[r]; held {
			return nil, &ConflictError{HolderID: holder}
		}
	}
	for _, r := range resources {
		m.edits[r]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			for _, r := range resources {
				if m.edits[r]--; m.edits[r] <= 0 {
					delete(m.edits, r)
				}
			}
		})
	}, nil
}

func (m *Manager) run(ctx context.Context, id string, fn Func, done chan struct{}) {
	m.mu.Lock()
	job := m.jobs[id]
	if job.Status == StatusQueued {
		job.Status = StatusRunning
	}
	job.StartedAt = m.now()
	m.persistLocked(job)
	m.mu.Unlock()

	outcome, err := m.invoke(ctx, fn, &reporter{m: m, id: id})

	m.mu.Lock()
	var finalErr *FinalError
	// A cancel request only wins when the job stopped with an error: work
	// that finished anyway (an outcome with a nil error) is recorded as it
	// ended, and so is a FinalError from work a cancel could not stop.
	cancelled := job.Status == StatusCancelling && err != nil && !errors.As(err, &finalErr)
	switch {
	case cancelled:
		job.Status = StatusCancelled
		job.Resumable = outcome.Resumable
	case err != nil:
		job.Status = StatusFailed
		job.Message = m.sanitize(err)
	case outcome.Status == StatusSucceeded || outcome.Status == StatusPartial:
		job.Status = outcome.Status
		res := outcome.Result
		job.Result = &res
		job.Resumable = outcome.Resumable
	default:
		job.Status = StatusFailed
		job.Message = genericFailure
	}
	job.FinishedAt = m.now()
	for _, r := range job.Resources {
		delete(m.locks, r)
	}
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
	}
	delete(m.cancels, id)
	m.persistLocked(job)
	final := snapshot(job)
	m.mu.Unlock()

	if m.onFinish != nil {
		m.onFinish(final)
	}
	close(done)
}

func (m *Manager) invoke(ctx context.Context, fn Func, r Reporter) (out Outcome, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out, err = Outcome{}, errors.New(genericFailure)
		}
	}()
	return fn(ctx, r)
}

// persistLocked saves a snapshot; persistence failures must not kill the job.
func (m *Manager) persistLocked(j *Job) {
	_ = m.store.Save(context.Background(), snapshot(j))
}

// Get returns a snapshot of a job.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return snapshot(j), true
}

// List returns all known jobs, newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, snapshot(m.jobs[m.order[i]]))
	}
	return out
}

// Cancel requests cancellation of an active job.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.Status.Terminal() {
		return fmt.Errorf("job %s already finished", id)
	}
	if j.Status != StatusCancelling {
		j.Status = StatusCancelling
		m.persistLocked(j)
	}
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
	}
	return nil
}

// Recover loads persisted jobs and marks any that were active as interrupted.
func (m *Manager) Recover(ctx context.Context) error {
	loaded, err := m.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("load jobs: %w", err)
	}
	sort.SliceStable(loaded, func(a, b int) bool { return loaded[a].CreatedAt.Before(loaded[b].CreatedAt) })

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range loaded {
		if _, exists := m.jobs[l.ID]; exists {
			continue
		}
		j := l
		if !j.Status.Terminal() {
			j.Status = StatusInterrupted
			j.Message = "interrupted by server restart"
			j.FinishedAt = m.now()
			if err := m.store.Save(ctx, snapshot(&j)); err != nil {
				return fmt.Errorf("persist interrupted job: %w", err)
			}
		}
		if j.Counts == nil {
			j.Counts = map[string]int{}
		}
		m.jobs[j.ID] = &j
		m.order = append(m.order, j.ID)
	}
	return nil
}

// Wait blocks until the job finishes. Intended for tests.
func (m *Manager) Wait(id string) {
	m.mu.Lock()
	done := m.done[id]
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

type reporter struct {
	m  *Manager
	id string
}

func (r *reporter) update(f func(*Job)) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	f(r.m.jobs[r.id])
}

func (r *reporter) Step(name string) { r.update(func(j *Job) { j.Step = name }) }
func (r *reporter) Progress(done, total int) {
	r.update(func(j *Job) { j.Done, j.Total = done, total })
}
func (r *reporter) Count(key string, delta int) {
	r.update(func(j *Job) { j.Counts[key] += delta })
}

func snapshot(j *Job) Job {
	c := *j
	c.Counts = make(map[string]int, len(j.Counts))
	for k, v := range j.Counts {
		c.Counts[k] = v
	}
	c.Resources = append([]string(nil), j.Resources...)
	if j.Result != nil {
		res := *j.Result
		c.Result = &res
	}
	return c
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
