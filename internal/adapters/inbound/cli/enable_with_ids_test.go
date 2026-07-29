package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteArtistsEnableWithIDs(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: "34EP7KEpOjXcM2TCat1ISk"
    category: core
    roles: [core]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
  - slug: achozen
    name: Achozen
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 2
    notes: ""
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: "5NIhgQ08PBzMux08ndi8Ae"
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: true
    editorial_order: 3
    notes: ""
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "enable-with-ids", "--catalog", catalogPath}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "enabled=1 already_enabled=1 without_spotify_ids=1")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "slug: wu-tang-clan\n    name: Wu-Tang Clan\n    spotify_id: 34EP7KEpOjXcM2TCat1ISk\n    category: core\n    roles:\n      - core\n    aliases: []\n    enabled: true")
	require.Contains(t, string(updated), "slug: achozen\n    name: Achozen\n    spotify_id: \"\"\n    category: affiliate_group\n    roles:\n      - affiliate_group\n    aliases: []\n    enabled: false")
}

func TestExecuteArtistsEnableWithIDsCanDryRun(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	original := []byte(`
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: "34EP7KEpOjXcM2TCat1ISk"
    category: core
    roles: [core]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`)
	require.NoError(t, os.WriteFile(catalogPath, original, 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "enable-with-ids", "--catalog", catalogPath, "--dry-run"}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "artists enable-with-ids dry-run: enabled=1")
	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Equal(t, string(original), string(updated))
}
