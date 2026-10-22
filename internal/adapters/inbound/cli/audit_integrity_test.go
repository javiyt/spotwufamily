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

func runIntegrity(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")
	var out, errOut bytes.Buffer
	code = cli.Execute(append([]string{"artists", "audit-integrity", "--offline"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestArtistsAuditIntegrityFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		strict  int // exit code with --strict
		code    int // exit code by default
		finding []string
	}{
		{"correct-homonym.yaml", 0, 0, []string{"info [blocked_sync_identity] method-man"}},
		{"incorrect-homonym.yaml", 1, 1, []string{"error [schema]", "name matches and credits are not enough"}},
		{"alias-only.yaml", 1, 1, []string{"error [schema]", "name matches and credits are not enough"}},
		{"no-discography.yaml", 0, 0, nil},
		{"two-profiles-same-name.yaml", 1, 0, []string{"warning [pending_profile] shared-name"}},
		{"multi-id.yaml", 0, 0, nil},
		{"historical-membership.yaml", 0, 0, nil},
		{"release-credit-vs-official.yaml", 0, 0, nil},
		{"credit-as-member.yaml", 1, 1, []string{"error [schema] relationships[0].evidence"}},
		{"member-of-non-group.yaml", 1, 1, []string{"error [schema] relationships[0].to"}},
		{"rejected-enabled.yaml", 1, 1, []string{"error [rejected_enabled_mapping] thief"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			path := filepath.Join("testdata", "integrity", tt.fixture)
			code, stdout, stderr := runIntegrity(t, "--catalog", path)
			require.Equal(t, tt.code, code, stdout+stderr)
			for _, want := range tt.finding {
				require.Contains(t, stdout, want)
			}
			strictCode, _, _ := runIntegrity(t, "--strict", "--catalog", path)
			require.Equal(t, tt.strict, strictCode)
		})
	}
}

func TestArtistsAuditIntegrityOutputIsDeterministicAndReadOnly(t *testing.T) {
	path := filepath.Join("testdata", "integrity", "rejected-enabled.yaml")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, first, _ := runIntegrity(t, "--format", "json", "--catalog", path)
	_, second, _ := runIntegrity(t, "--format", "json", "--catalog", path)
	require.Equal(t, first, second)

	var report struct {
		Summary  struct{ Errors, Warnings, Infos int }
		Findings []struct{ Severity, Code string }
	}
	require.NoError(t, json.Unmarshal([]byte(first), &report))
	require.Equal(t, 2, report.Summary.Errors)
	require.Equal(t, "error", report.Findings[0].Severity)
	require.Equal(t, "rejected_enabled_mapping", report.Findings[0].Code, "errors sort first, then by code")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestArtistsAuditIntegrityRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{{"--format", "xml"}, {"--bogus"}, {"--catalog"}} {
		code, _, stderr := runIntegrity(t, args...)
		require.Equal(t, 2, code, stderr)
	}
	code, _, stderr := runIntegrity(t, "--catalog", filepath.Join(t.TempDir(), "missing.yaml"))
	require.Equal(t, 1, code, stderr)
}

func TestArtistsAuditIntegrityPassesForShippedCatalog(t *testing.T) {
	code, stdout, stderr := runIntegrity(t, "--catalog", filepath.Join("..", "..", "..", "..", "data", "artists.yaml"))
	require.Equal(t, 0, code, stdout+stderr)
}
