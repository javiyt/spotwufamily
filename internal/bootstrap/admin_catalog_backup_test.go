package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

func TestDiscoverImportBacksUpTheCatalogBeforeWriting(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	wiki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"parse":{"sections":[
			{"toclevel":1,"line":"Groups","anchor":"Groups"},
			{"toclevel":2,"line":"Killarmy","anchor":"Killarmy"}
		]}}`)
	}))
	t.Cleanup(wiki.Close)
	s := startStack(t, e2eConfig(root, envMap(nil), readyReleaser(), WithWikipedia(wiki.URL, "")))
	catalogPath := filepath.Join(root, "data", "artists.yaml")
	before, err := os.ReadFile(catalogPath)
	require.NoError(t, err)

	pv, tok, err := s.a.PreviewAction(context.Background(), "discover-import", maintenance.Params{})
	require.NoError(t, err)
	id := s.startedJob(s.confirmWith("discover-import", maintenance.Params{}, tok, pv.RequiresTyped, nil))
	j := s.wait(id)
	require.Equal(t, jobs.StatusSucceeded, j.Status, j.Message)

	after, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.NotEqual(t, string(before), string(after), "the import wrote the catalog")

	dir := filepath.Join(root, "build", "admin", "backups")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var copies []string
	for _, e := range entries {
		if regexp.MustCompile(`^artists-\d{8}T\d{6}Z(-\d+)?\.yaml$`).MatchString(e.Name()) {
			copies = append(copies, e.Name())
		}
	}
	require.Len(t, copies, 1, "one timestamped catalog copy: %v", entries)
	saved, err := os.ReadFile(filepath.Join(dir, copies[0]))
	require.NoError(t, err)
	require.Equal(t, string(before), string(saved), "the copy holds the catalog as it was before the import")
	info, err := os.Stat(filepath.Join(dir, copies[0]))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
