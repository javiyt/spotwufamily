package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrLocked reports that another process or handle holds the resource.
var ErrLocked = errors.New("resource is locked by another operation")

// ResourceLock is a non-blocking exclusive lock on a lock file, shared by the
// admin and CLI processes so they never write the same resource at once.
type ResourceLock struct {
	path string
	mu   sync.Mutex
	held bool
}

func NewResourceLock(path string) *ResourceLock {
	return &ResourceLock{path: path}
}

// TryLock takes the lock without waiting. It returns ErrLocked when another
// process, another ResourceLock or this same lock already holds it. The
// returned release func is safe to call more than once.
func (l *ResourceLock) TryLock() (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return nil, ErrLocked
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	unlock, err := tryLockFile(l.path)
	if err != nil {
		return nil, err
	}
	l.held = true

	var once sync.Once
	return func() {
		once.Do(func() {
			unlock()
			l.mu.Lock()
			l.held = false
			l.mu.Unlock()
		})
	}, nil
}
