//go:build !unix

package filesystem

import (
	"path/filepath"
	"sync"
)

var (
	heldLocksMu sync.Mutex
	heldLocks   = map[string]bool{}
)

// tryLockFile only excludes holders inside this process on platforms without
// flock.
func tryLockFile(path string) (func(), error) {
	key := path
	if abs, err := filepath.Abs(path); err == nil {
		key = abs
	}
	heldLocksMu.Lock()
	defer heldLocksMu.Unlock()
	if heldLocks[key] {
		return nil, ErrLocked
	}
	heldLocks[key] = true

	return func() {
		heldLocksMu.Lock()
		delete(heldLocks, key)
		heldLocksMu.Unlock()
	}, nil
}
