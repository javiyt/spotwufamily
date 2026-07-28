package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/stretchr/testify/require"
)

func TestWriterWriteFileAndPrune(t *testing.T) {
	dir := t.TempDir()
	writer := filesystem.NewWriter()
	path := filepath.Join(dir, "generated", "artists", "index.json")

	changed, err := writer.WriteFile(path, []byte("{}\n"))
	require.NoError(t, err)
	require.True(t, changed)

	changed, err = writer.WriteFile(path, []byte("{}\n"))
	require.NoError(t, err)
	require.False(t, changed)

	obsolete := filepath.Join(dir, "generated", "old.json")
	require.NoError(t, os.WriteFile(obsolete, []byte("{}\n"), 0o644))
	require.NoError(t, writer.PruneDir(filepath.Join(dir, "generated"), map[string]struct{}{path: {}}))
	require.NoFileExists(t, obsolete)
	require.FileExists(t, path)
}
