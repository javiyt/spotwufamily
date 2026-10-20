package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

const reviewCatalog = `version: 1
artists:
  - slug: rza
    name: RZA
    spotify_id: 34EP7KEpOjXcM2TCat1ISk
    category: core
    roles: [core]
    aliases: []
    enabled: true
    editorial_order: 1
    notes: ""
`

func TestExecuteArtistsReviewProfileVerifiesAndRejectsAutomation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	require.NoError(t, os.WriteFile(path, []byte(reviewCatalog), 0o644))

	var stdout, stderr bytes.Buffer
	code := cli.Execute([]string{
		"artists", "review-profile", "--catalog", path, "--slug", "rza", "--spotify-id", "34EP7KEpOjXcM2TCat1ISk",
		"--status", "verified", "--reviewer", "javi", "--evidence-type", "musicbrainz",
		"--evidence-url", "https://musicbrainz.org/artist/1", "--rationale", "MusicBrainz lists this profile",
	}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.Contains(t, stdout.String(), "unreviewed -> verified")

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(saved), "status: verified")
	require.Contains(t, string(saved), "reviewer: javi")

	stdout.Reset()
	stderr.Reset()
	code = cli.Execute([]string{
		"artists", "review-profile", "--catalog", path, "--slug", "rza", "--spotify-id", "34EP7KEpOjXcM2TCat1ISk",
		"--status", "verified", "--evidence-type", "official_site", "--evidence-url", "http://example.com", "--rationale", "x",
	}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "https")
}
