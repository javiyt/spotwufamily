package filesystem_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
)

type exportTree struct {
	site, out, static string
}

func newExportTree(t *testing.T) exportTree {
	t.Helper()
	site := t.TempDir()
	tree := exportTree{site: site, out: filepath.Join(site, "data", "generated"), static: filepath.Join(site, "static")}
	write := func(path, data string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
	}
	write(filepath.Join(tree.out, "a.json"), "old-a")
	write(filepath.Join(tree.out, "obsolete.json"), "old-obsolete")
	write(filepath.Join(tree.static, "search-index.json"), "old-search")
	write(filepath.Join(tree.static, "logo.svg"), "keep me")
	return tree
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			rel, _ := filepath.Rel(root, path)
			files[rel] = string(data)
		}
		return nil
	}))
	return files
}

func TestStagedWriterDiscardLeavesPreviousArtifacts(t *testing.T) {
	tree := newExportTree(t)
	before := readTree(t, tree.site)

	staged, err := filesystem.NewStagedWriter(tree.out, tree.static)
	require.NoError(t, err)
	changed, err := staged.WriteFile(filepath.Join(tree.out, "a.json"), []byte("new-a"))
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = staged.WriteFile(filepath.Join(tree.out, "nested", "b.json"), []byte("new-b"))
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, staged.PruneDir(tree.out, map[string]struct{}{filepath.Join(tree.out, "a.json"): {}}))
	// Cancelled mid-export: the staged files are dropped.
	require.NoError(t, staged.Discard())

	require.Equal(t, before, readTree(t, tree.site), "previous artifacts must be byte-identical and nothing left behind")
}

func TestStagedWriterCommitPublishesAndPrunes(t *testing.T) {
	tree := newExportTree(t)
	staged, err := filesystem.NewStagedWriter(tree.out, tree.static)
	require.NoError(t, err)

	keepOut := map[string]struct{}{}
	for path, data := range map[string]string{
		filepath.Join(tree.out, "a.json"):           "old-a", // unchanged
		filepath.Join(tree.out, "nested", "b.json"): "new-b",
	} {
		keepOut[path] = struct{}{}
		changed, err := staged.WriteFile(path, []byte(data))
		require.NoError(t, err)
		require.Equal(t, data != "old-a", changed)
	}
	search := filepath.Join(tree.static, "search-index.json")
	_, err = staged.WriteFile(search, []byte("new-search"))
	require.NoError(t, err)
	require.NoError(t, staged.PruneDir(tree.out, keepOut))
	require.NoError(t, staged.PruneDir(tree.static, map[string]struct{}{search: {}}))

	_, err = staged.WriteFile(filepath.Join(tree.site, "outside.json"), []byte("x"))
	require.Error(t, err, "files outside the export roots are refused")

	require.NoError(t, staged.Commit())
	require.NoError(t, staged.Discard(), "discard after commit is a no-op")

	require.Equal(t, map[string]string{
		"data/generated/a.json":        "old-a",
		"data/generated/nested/b.json": "new-b",
		"static/search-index.json":     "new-search",
		"static/logo.svg":              "keep me",
	}, readTree(t, tree.site))
}

func TestStagedWriterRemovesStaleStagingDirs(t *testing.T) {
	tree := newExportTree(t)
	stale := filepath.Join(tree.site, "data", ".export-staging-generated-123")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	staged, err := filesystem.NewStagedWriter(tree.out, tree.static)
	require.NoError(t, err)
	require.NoError(t, staged.Discard())
	require.NoDirExists(t, stale)
}

func TestDiffWriterReportsChangesWithoutWriting(t *testing.T) {
	tree := newExportTree(t)
	before := readTree(t, tree.site)

	diff := filesystem.NewDiffWriter()
	changed, err := diff.WriteFile(filepath.Join(tree.out, "a.json"), []byte("old-a"))
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = diff.WriteFile(filepath.Join(tree.out, "new.json"), []byte("n"))
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, diff.PruneDir(tree.out, map[string]struct{}{
		filepath.Join(tree.out, "a.json"): {}, filepath.Join(tree.out, "new.json"): {},
	}))

	require.Equal(t, []string{filepath.Join(tree.out, "new.json"), filepath.Join(tree.out, "obsolete.json")}, diff.Changed())
	require.Equal(t, before, readTree(t, tree.site))
	for _, p := range diff.Changed() {
		require.False(t, strings.Contains(p, ".tmp"))
	}
}
