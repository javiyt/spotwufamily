package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
	"github.com/stretchr/testify/require"
)

const candidatesCatalog = `version: 1
artists:
  - slug: gravediggaz
    name: Gravediggaz
    spotify_id: ""
    category: affiliate_group
    roles:
      - affiliate_group
    aliases: []
    enabled: false
    editorial_order: 1
    notes: ""
  - slug: ancients-coins
    name: Ancients Coins
    spotify_id: ""
    category: affiliate_group
    roles:
      - affiliate_group
    aliases: []
    enabled: false
    editorial_order: 2
    notes: ""
`

const candidatesInventory = "\xef\xbb\xbftipo,nombre,nivel_verificacion_referencia,funciones,relaciones_a_grupos,fuentes_ids,criterio\n" +
	"artista,Frukwan,alta,rapero_o_colaborador,group-gravediggaz,gravediggaz,x\n" +
	"artista,Prince Paul,alta,productor,group-gravediggaz,gravediggaz,x\n" +
	"grupo,Gravediggaz,alta,,,gravediggaz,x\n" +
	"grupo,Black Brigade,baja,,,directorio_fans,x\n"

type candidateEnv struct {
	dir, catalog, ledger, input string
}

func newCandidateEnv(t *testing.T) candidateEnv {
	t.Helper()
	dir := t.TempDir()
	env := candidateEnv{dir: dir, catalog: filepath.Join(dir, "artists.yaml"), ledger: filepath.Join(dir, "wu-candidates.yaml"), input: filepath.Join(dir, "in.csv")}
	require.NoError(t, os.WriteFile(env.catalog, []byte(candidatesCatalog), 0o644))
	require.NoError(t, os.WriteFile(env.input, []byte(candidatesInventory), 0o644))
	return env
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Execute(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestImportCandidatesDryRunThenApplyIsIdempotent(t *testing.T) {
	env := newCandidateEnv(t)

	code, out, errOut := run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "import-candidates dry-run: read=4")
	require.Contains(t, out, "pending=3 accepted=0 rejected=0 needs_more_evidence=0 alias_of_existing=1")
	_, err := os.Stat(env.ledger)
	require.True(t, os.IsNotExist(err), "dry run must not write the ledger")

	for i := 0; i < 2; i++ {
		code, out, errOut = run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger, "--apply")
		require.Equal(t, 0, code, errOut)
		require.Contains(t, out, "import-candidates applied")
	}
	first, err := os.ReadFile(env.ledger)
	require.NoError(t, err)
	code, _, errOut = run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger, "--apply")
	require.Equal(t, 0, code, errOut)
	second, err := os.ReadFile(env.ledger)
	require.NoError(t, err)
	require.Equal(t, first, second)

	catalogAfter, err := os.ReadFile(env.catalog)
	require.NoError(t, err)
	require.NotContains(t, string(catalogAfter), "frukwan", "pending candidates never reach the catalog")
}

func TestReviewAcceptStageRevertWorkflow(t *testing.T) {
	env := newCandidateEnv(t)
	code, _, errOut := run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger, "--apply")
	require.Equal(t, 0, code, errOut)

	// One independent source is not enough to accept.
	review := []string{"artists", "review-candidate", "--catalog", env.catalog, "--ledger", env.ledger, "--name", "Frukwan", "--reviewer", "javi",
		"--status", "accepted", "--category", "affiliate_artist", "--relation", "member", "--group", "gravediggaz", "--relation-claim", "Gravediggaz member",
		"--evidence", "musicbrainz|https://musicbrainz.org/artist/6c20758e-b910-4ea0-84da-b20ecc7c7d1e|MusicBrainz lists Frukwan as an original Gravediggaz member"}
	code, _, errOut = run(t, review...)
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "different source types")

	review = append(review, "--evidence", "wikipedia|https://en.wikipedia.org/wiki/Gravediggaz|Article lists Frukwan as The Gatekeeper")
	code, out, errOut := run(t, review...)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "status=accepted evidence=2")

	code, out, errOut = run(t, "artists", "import-candidates", "--catalog", env.catalog, "--ledger", env.ledger)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "dry-run")
	catalogBefore, _ := os.ReadFile(env.catalog)
	require.NotContains(t, string(catalogBefore), "slug: frukwan")

	report := filepath.Join(env.dir, "report.md")
	code, _, errOut = run(t, "artists", "import-candidates", "--catalog", env.catalog, "--ledger", env.ledger, "--report", report, "--apply")
	require.Equal(t, 0, code, errOut)
	staged, _ := os.ReadFile(env.catalog)
	require.Contains(t, string(staged), "slug: frukwan")
	require.Contains(t, string(staged), "enabled: false")
	require.Contains(t, string(staged), "spotify_id: \"\"")
	md, _ := os.ReadFile(report)
	require.Contains(t, string(md), "staged as `frukwan`")

	code, out, errOut = run(t, "artists", "import-candidates", "--catalog", env.catalog, "--ledger", env.ledger, "--revert", "--apply")
	require.Equal(t, 0, code, errOut)
	reverted, _ := os.ReadFile(env.catalog)
	require.NotContains(t, string(reverted), "slug: frukwan")
	require.Contains(t, out, "accepted=1")
}

func TestReviewCandidateRejectedIsPersistedAndNotReopenedByImport(t *testing.T) {
	env := newCandidateEnv(t)
	run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger, "--apply")

	code, _, errOut := run(t, "artists", "review-candidate", "--catalog", env.catalog, "--ledger", env.ledger, "--name", "black brigade", "--reviewer", "javi", "--status", "rejected", "--reason", "No independent documentation")
	require.Equal(t, 0, code, errOut)
	code, out, errOut := run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger, "--apply")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "pending=2 accepted=0 rejected=1")

	// Leaving rejected needs new independent evidence.
	code, _, errOut = run(t, "artists", "review-candidate", "--catalog", env.catalog, "--ledger", env.ledger, "--name", "Black Brigade", "--reviewer", "javi", "--status", "needs_more_evidence", "--reason", "retry")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "new independent evidence")
}

func TestReviewCandidateValidatesFlags(t *testing.T) {
	env := newCandidateEnv(t)
	code, _, errOut := run(t, "artists", "review-candidate", "--catalog", env.catalog, "--ledger", env.ledger)
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "required")
	code, _, errOut = run(t, "artists", "review-candidate", "--name", "X", "--status", "accepted", "--reviewer", "j", "--evidence", "bad")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "type|url|rationale")
	code, _, _ = run(t, "artists", "import-candidates", "--bogus")
	require.Equal(t, 2, code)
	code, _, _ = run(t, "artists", "import-candidates", "--revert", "--input", "x.csv")
	require.Equal(t, 2, code)
}

func TestDiscoverWuDoesNotReproposeLedgerCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"parse": {"sections": [
			{"toclevel": 1, "line": "Groups", "anchor": "Groups"},
			{"toclevel": 2, "line": "Black Brigade", "anchor": "Black_Brigade"},
			{"toclevel": 1, "line": "Singers", "anchor": "Singers"},
			{"toclevel": 2, "line": "Tekitha", "anchor": "Tekitha"}]}}`)
	}))
	defer server.Close()
	env := newCandidateEnv(t)
	run(t, "artists", "import-candidates", "--input", env.input, "--catalog", env.catalog, "--ledger", env.ledger, "--apply")
	run(t, "artists", "review-candidate", "--catalog", env.catalog, "--ledger", env.ledger, "--name", "Black Brigade", "--reviewer", "javi", "--status", "rejected", "--reason", "unsupported")
	ledgerBefore, _ := os.ReadFile(env.ledger)

	for i := 0; i < 2; i++ {
		code, out, errOut := run(t, "artists", "discover-wu", "--catalog", env.catalog, "--ledger", env.ledger, "--report", filepath.Join(env.dir, "d.md"), "--wikipedia-api-url", server.URL, "--apply", "--quiet")
		require.Equal(t, 0, code, errOut)
		if i == 0 {
			require.Contains(t, out, "found=2 existing=0 new=1 added=1 suppressed=1")
		} else {
			require.Contains(t, out, "found=2 existing=1 new=0 added=0 suppressed=1")
		}
	}
	updated, _ := os.ReadFile(env.catalog)
	require.NotContains(t, string(updated), "black-brigade")
	require.Contains(t, string(updated), "slug: tekitha")
	ledgerAfter, _ := os.ReadFile(env.ledger)
	require.Equal(t, ledgerBefore, ledgerAfter, "discovery never changes the ledger")
}
