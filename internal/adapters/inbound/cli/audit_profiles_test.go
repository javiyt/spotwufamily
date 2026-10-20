package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

const auditProfilesCatalog = `
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    spotify_ids:
      - 0H8YCcvC3MPLKnbDRasGiG
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
  - slug: k-g-b
    name: K.G.B.
    spotify_id: 2gKwx0Lm0JoOrN2mY9Z0PG
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 2
    notes: ""
`

func auditProfilesSetup(t *testing.T) (catalogPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	catalogPath = filepath.Join(dir, "artists.yaml")
	dbPath = filepath.Join(dir, "catalog.db")
	require.NoError(t, os.WriteFile(catalogPath, []byte(auditProfilesCatalog), 0o644))
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Execute([]string{"db", "migrate", "--db", dbPath, "--snapshot", filepath.Join(dir, "s.sql.gz")}, &stdout, &stderr), stderr.String())
	return catalogPath, dbPath
}

func TestArtistsAuditProfilesOfflineListsEveryIDWithoutNetworkOrSecrets(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")
	catalogPath, dbPath := auditProfilesSetup(t)
	before, err := os.ReadFile(catalogPath)
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "audit-profiles", "--offline", "--catalog", catalogPath, "--db", dbPath, "--report", "-"}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	out := stdout.String()
	for _, id := range []string{"34EP7KEpOjXcM2TCat1ISk", "0H8YCcvC3MPLKnbDRasGiG", "2gKwx0Lm0JoOrN2mY9Z0PG"} {
		require.Contains(t, out, "`"+id+"`")
	}
	require.Contains(t, out, "offline (cache only)")
	after, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "audit mode never writes YAML")
}

func TestArtistsAuditProfilesJSONReportToFileAndArtistFilter(t *testing.T) {
	catalogPath, dbPath := auditProfilesSetup(t)
	reportPath := filepath.Join(t.TempDir(), "profiles.json")

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "audit-profiles", "--offline", "--artist", "wu-tang-clan", "--format", "json", "--catalog", catalogPath, "--db", dbPath, "--report", reportPath}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var report struct {
		Profiles []struct {
			SpotifyID string `json:"spotify_id"`
			Role      string `json:"role"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(data, &report))
	require.Len(t, report.Profiles, 2)
	require.Equal(t, "primary", report.Profiles[0].Role)
	require.Equal(t, "secondary", report.Profiles[1].Role)
}

func TestArtistsAuditProfilesRejectsBadOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--format", "yaml"},
		{"--bogus"},
		{"--apply"},
		{"--concurrency", "zero"},
	} {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(append([]string{"artists", "audit-profiles", "--offline"}, args...), &stdout, &stderr)
		require.Equal(t, 2, code, args)
	}
}

func TestArtistsAuditProfilesOnlineRequiresSpotifyCredentials(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")
	catalogPath, dbPath := auditProfilesSetup(t)

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{"artists", "audit-profiles", "--catalog", catalogPath, "--db", dbPath}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "SPOTIFY_CLIENT_ID")
}
