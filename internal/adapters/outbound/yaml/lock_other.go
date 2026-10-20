//go:build !unix

package yamlcatalog

import "sync"

var processMu sync.Mutex

// lockCatalog only serialises writers inside this process on platforms without
// flock.
func lockCatalog(string) (func(), error) {
	processMu.Lock()
	return processMu.Unlock, nil
}
