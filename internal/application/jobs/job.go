// Package jobs runs long-lived maintenance operations in the background with
// per-resource locking, progress reporting and persisted status.
package jobs

import (
	"context"
	"time"
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusCancelling  Status = "cancelling"
	StatusSucceeded   Status = "succeeded"
	StatusPartial     Status = "partial"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

// Terminal reports whether the status is final.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusPartial, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	}
	return false
}

// Report is a tabular result attached to a finished job.
type Report struct {
	Name    string
	Columns []string
	Rows    [][]string
}

// Result is the user-facing outcome of a job.
type Result struct {
	Summary string
	Report  *Report
}

// Job is a snapshot of a background operation.
type Job struct {
	ID         string
	Kind       string
	Status     Status
	Step       string
	Done       int
	Total      int
	Counts     map[string]int
	Message    string
	Resources  []string
	ResumeOf   string
	Resumable  bool
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Result     *Result
}

// Reporter lets a running Func publish progress.
type Reporter interface {
	Step(name string)
	Progress(done, total int)
	Count(key string, delta int)
}

// Func is the work performed by a job. Status in the Outcome must be
// StatusSucceeded or StatusPartial.
type Func func(ctx context.Context, r Reporter) (Outcome, error)

// Outcome is what a Func returns on completion.
type Outcome struct {
	Status    Status
	Result    Result
	Resumable bool
	// Final documents that the job started work a cancel could not stop.
	// Any outcome returned with a nil error is recorded as returned even if
	// a cancel was requested while the job ran; Final is kept for callers
	// that state it explicitly (release publish).
	Final bool
}

// FinalError is a failure recorded as StatusFailed, with its message, even
// if a cancel was requested while the job ran (see Outcome.Final).
type FinalError struct{ Err error }

func (e *FinalError) Error() string { return e.Err.Error() }
func (e *FinalError) Unwrap() error { return e.Err }

// Spec describes a job to submit.
type Spec struct {
	Kind      string
	Resources []string
	ResumeOf  string
	Run       Func
}

// Store persists job snapshots.
type Store interface {
	Save(ctx context.Context, j Job) error
	Load(ctx context.Context) ([]Job, error)
}
