//go:build unix

package yamlcatalog

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

var processMu sync.Mutex

// lockCatalog serialises writers of one catalog, across goroutines and across
// processes (for example the admin and a CLI run). The lock lives in a sibling
// file so replacing the catalog by rename never invalidates it.
func lockCatalog(path string) (func(), error) {
	processMu.Lock()
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		processMu.Unlock()
		return nil, fmt.Errorf("open catalog lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		processMu.Unlock()
		return nil, fmt.Errorf("lock catalog: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		processMu.Unlock()
	}, nil
}
