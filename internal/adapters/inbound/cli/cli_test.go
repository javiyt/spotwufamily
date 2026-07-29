package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

func TestExecuteVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{"version"}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "spotwufamily v2-dev")
	require.Empty(t, stderr.String())
}

func TestExecuteArtistsResolveNonInteractive(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")
	reportPath := filepath.Join(dir, "report.md")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0"}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{
		"artists",
		"resolve",
		"--non-interactive",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
		"--report",
		reportPath,
	}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "wrote artist resolution report")

	report, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Contains(t, string(report), "Gravediggaz")
	require.Contains(t, string(report), "`0CH4f9m2L3TRaA5oErU2p0`")
}

func TestExecuteArtistsResolveApply(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")
	reportPath := filepath.Join(dir, "report.md")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0"}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{
		"artists",
		"resolve",
		"--non-interactive",
		"--apply",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
		"--report",
		reportPath,
	}, &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "applied resolved Spotify IDs: 1")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 0CH4f9m2L3TRaA5oErU2p0")
	require.Contains(t, string(updated), "enabled: false")
}

func TestExecuteArtistsResolveInteractiveAppliesSelectedCandidate(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0", "popularity": 45, "followers": 1000}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("1\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "select candidate number")
	require.Contains(t, stdout.String(), "https://open.spotify.com/artist/0CH4f9m2L3TRaA5oErU2p0")
	require.Contains(t, stdout.String(), "interactive resolve: applied=1 skipped=0")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 0CH4f9m2L3TRaA5oErU2p0")
	require.Contains(t, string(updated), "enabled: false")
}

func TestExecuteArtistsResolveInteractiveReviewAllCanReplaceExistingID(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: "1111111111111111111111"
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "gravediggaz": [
    {"name": "Gravediggaz", "spotify_id": "0CH4f9m2L3TRaA5oErU2p0", "popularity": 45, "followers": 1000}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--review-all",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("1\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "current Spotify IDs:")
	require.Contains(t, stdout.String(), "1111111111111111111111")
	require.Contains(t, stdout.String(), "k=keep current")
	require.Contains(t, stdout.String(), "interactive resolve: applied=1 skipped=0 kept=0 cleared=0")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 0CH4f9m2L3TRaA5oErU2p0")
	require.NotContains(t, string(updated), "spotify_id: \"1111111111111111111111\"")
}

func TestExecuteArtistsResolveInteractiveReviewAllCanAddMultipleAdditionalIDs(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

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
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "wu-tang-clan": [
    {"name": "Wu-Tang Clan", "spotify_id": "0H8YCcvC3MPLKnbDRasGiG", "popularity": 40, "followers": 500},
    {"name": "Wu-Tang Clan Legacy", "spotify_id": "1WuLegacySpotifyArtist", "popularity": 20, "followers": 200}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--review-all",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("a1,a2\nk\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "aN or aN,aM=add extra IDs")
	require.Contains(t, stdout.String(), "added extra Spotify ID: 0H8YCcvC3MPLKnbDRasGiG")
	require.Contains(t, stdout.String(), "added extra Spotify ID: 1WuLegacySpotifyArtist")

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Contains(t, string(updated), "spotify_id: 34EP7KEpOjXcM2TCat1ISk")
	require.Contains(t, string(updated), "spotify_ids:")
	require.Contains(t, string(updated), "- 0H8YCcvC3MPLKnbDRasGiG")
	require.Contains(t, string(updated), "- 1WuLegacySpotifyArtist")
}

func TestExecuteArtistsResolveInteractiveReviewAllHidesCurrentSpotifyIDsFromCandidates(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: wu-tang-killa-beez
    name: Wu-Tang Killa Beez
    spotify_id: "6UEytD2shU7Z8fMKj08puK"
    category: affiliate_group
    roles: [affiliate_group]
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "wu-tang-killa-beez": [
    {"name": "Wu Tang Killa Beez", "spotify_id": "6UEytD2shU7Z8fMKj08puK", "popularity": 24, "followers": 59749},
    {"name": "Eternal of Wu Tang Killa Beez", "spotify_id": "5txM0FfDbF5hcAR5wRCt1Y", "popularity": 0, "followers": 0}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--review-all",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("k\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), "current Spotify IDs:")
	require.Contains(t, stdout.String(), "- 6UEytD2shU7Z8fMKj08puK | https://open.spotify.com/artist/6UEytD2shU7Z8fMKj08puK | primary")
	require.NotContains(t, stdout.String(), "1. Wu Tang Killa Beez | 6UEytD2shU7Z8fMKj08puK")
	require.Contains(t, stdout.String(), "1. Eternal of Wu Tang Killa Beez | 5txM0FfDbF5hcAR5wRCt1Y")
}

func TestExecuteArtistsResolveInteractiveRejectsMixedCommaSelection(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	candidatesPath := filepath.Join(dir, "candidates.json")

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
`), 0o644))
	require.NoError(t, os.WriteFile(candidatesPath, []byte(`{
  "wu-tang-clan": [
    {"name": "Wu-Tang Clan", "spotify_id": "0H8YCcvC3MPLKnbDRasGiG", "popularity": 40, "followers": 500},
    {"name": "Wu-Tang Clan Legacy", "spotify_id": "1WuLegacySpotifyArtist", "popularity": 20, "followers": 200}
  ]
}`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.ExecuteWithInput([]string{
		"artists",
		"resolve",
		"--interactive",
		"--review-all",
		"--catalog",
		catalogPath,
		"--candidates",
		candidatesPath,
	}, strings.NewReader("a1,2\nk\n"), &stdout, &stderr)

	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())
	require.Contains(t, stdout.String(), `invalid selection "a1,2"`)

	updated, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.NotContains(t, string(updated), "spotify_ids:")
}

func TestExecuteUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute([]string{"unknown"}, &stdout, &stderr)

	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "unknown command")
	require.Empty(t, stdout.String())
}
