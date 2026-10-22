package httpadmin_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The published site is read-only and static. These tests fail if admin code,
// session material or credentials ever reach the Hugo sources or its output.
func TestPublishedSiteContainsNoAdminSurface(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "site")
	forbidden := []string{
		"SPOTIFY_CLIENT_SECRET", "SPOTIFY_CLIENT_ID", "SPOTWUFAMILY_ADMIN", "X-CSRF", "csrf_token",
		"spotwufamily_admin", "admin.js", "admin.css", "/api/artists", "/jobs/", "/operations",
	}
	forbiddenFiles := []string{"admin.js", "admin.css"}

	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		for _, name := range forbiddenFiles {
			require.NotEqual(t, name, d.Name(), "admin asset published: %s", path)
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		if !isText(data) {
			return nil
		}
		scanned++
		for _, marker := range forbidden {
			require.False(t, strings.Contains(string(data), marker), "%s contains admin/credential marker %q", path, marker)
		}
		return nil
	})
	require.NoError(t, err)
	require.Positive(t, scanned, "site sources must be scanned")
}

func isText(data []byte) bool {
	for _, b := range data[:min(len(data), 512)] {
		if b == 0 {
			return false
		}
	}
	return true
}
