package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/cli"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAdminServeRefusesNonLoopbackHost(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "192.168.1.10", ""} {
		var stdout, stderr bytes.Buffer
		code := cli.ExecuteWithContext(context.Background(), []string{"admin", "serve", "--host", host, "--port", "0"}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 2, code, host)
		require.Contains(t, stderr.String(), "admin serve:")
	}
}

func TestAdminServeRemoteNeedsOptInAndToken(t *testing.T) {
	t.Setenv("SPOTWUFAMILY_ADMIN_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := cli.ExecuteWithContext(context.Background(), []string{"admin", "serve", "--host", "192.168.1.10", "--allow-remote", "--port", "0"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "token")
}

func TestAdminServeRejectsUnknownOption(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.ExecuteWithContext(context.Background(), []string{"admin", "serve", "--nope"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 2, code)
}

func TestAdminServeEndToEndOfflineAndGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "artists.yaml")
	require.NoError(t, os.WriteFile(catalogPath, []byte(`
version: 1
artists:
  - slug: wu-tang-clan
    name: Wu-Tang Clan
    category: core
    roles: [core]
    enabled: false
    editorial_order: 1
`), 0o644))
	dbPath := filepath.Join(dir, "missing.db")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- cli.ExecuteWithContext(ctx, []string{"admin", "serve", "--port", "0", "--catalog", catalogPath, "--db", dbPath}, strings.NewReader(""), stdout, stderr)
	}()

	launch := regexp.MustCompile(`http://127\.0\.0\.1:\d+/auth\?token=[A-Za-z0-9_-]+`)
	var url string
	require.Eventually(t, func() bool {
		url = launch.FindString(stdout.String())
		return url != ""
	}, 5*time.Second, 20*time.Millisecond, "stderr: %s", stderr.String())

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	resp, err := client.Get(url)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Offline mode")
	require.Contains(t, string(body), "Artists")
	require.NoFileExists(t, dbPath)

	cancel()
	select {
	case code := <-done:
		require.Equal(t, 0, code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("admin serve did not stop after cancellation")
	}
}
