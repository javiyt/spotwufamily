package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

const relationshipsCatalog = `version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    spotify_id: ""
    category: core
    roles: [core, affiliate_group]
    aliases: []
    enabled: false
    notes: ""
  - slug: rza
    name: RZA
    spotify_id: ""
    category: core
    aliases: []
    enabled: false
    notes: ""
`

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Execute(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestArtistsRelationshipsLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artists.yaml")
	require.NoError(t, os.WriteFile(path, []byte(relationshipsCatalog), 0o644))
	base := []string{"artists", "relationships"}
	with := func(args ...string) []string {
		return append(append([]string{}, base...), append(args, "--catalog", path)...)
	}

	code, out, errOut := runCLI(t, with("add", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan", "--dry-run")...)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "dry-run, nothing saved")
	_, out, _ = runCLI(t, with("list")...)
	require.Contains(t, out, "relationships: 0")

	code, _, errOut = runCLI(t, with("add", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan")...)
	require.Equal(t, 0, code, errOut)

	code, _, errOut = runCLI(t, with("add", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan")...)
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "already exists")

	code, _, errOut = runCLI(t, with("add", "--type", "member_of", "--from", "wu-tang-clan", "--to", "rza")...)
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "requires a group target")

	_, out, _ = runCLI(t, with("members", "--group", "wu-tang-clan", "--established")...)
	require.Contains(t, out, "members of wu-tang-clan: 0", "proposed claims are not established")

	code, _, errOut = runCLI(t, with("review", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan", "--status", "verified")...)
	require.Equal(t, 2, code, "verified without evidence or reviewer must be blocked")
	require.Contains(t, errOut, "independent evidence")

	code, _, errOut = runCLI(t, with("review", "--type", "member_of", "--from", "rza", "--to", "wu-tang-clan", "--status", "verified",
		"--reviewer", "tester", "--confidence", "high", "--evidence-type", "wikipedia",
		"--evidence-url", "https://en.wikipedia.org/wiki/Wu-Tang_Clan", "--evidence-rationale", "Founding member.")...)
	require.Equal(t, 0, code, errOut)
	_, out, _ = runCLI(t, with("members", "--group", "wu-tang-clan", "--established")...)
	require.Contains(t, out, "rza (RZA)")
	_, out, _ = runCLI(t, with("groups", "--artist", "rza")...)
	require.Contains(t, out, "wu-tang-clan")

	code, out, errOut = runCLI(t, "artists", "rename", "--slug", "rza", "--to", "the-rza", "--catalog", path)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "relationships rewritten=1")
	_, out, _ = runCLI(t, with("list", "--slug", "the-rza")...)
	require.Contains(t, out, "member_of the-rza -> wu-tang-clan")

	code, _, _ = runCLI(t, with("remove", "--type", "member_of", "--from", "the-rza", "--to", "wu-tang-clan")...)
	require.Equal(t, 0, code)
	_, out, _ = runCLI(t, with("list")...)
	require.Contains(t, out, "relationships: 0")

	code, _, _ = runCLI(t, with("bogus")...)
	require.Equal(t, 2, code)
}
