package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
)

func TestJobErrorSanitizerLogsTheCauseRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := func(k string) string {
		if k == "SPOTIFY_CLIENT_SECRET" {
			return "spotify-s3cr3t"
		}
		return ""
	}
	sanitize := jobErrorSanitizer(logger, env)

	msg := sanitize(maintenance.Safe(errors.New("open /tmp/x/catalog.db: denied (spotify-s3cr3t)")))
	require.Equal(t, "An internal error occurred.", msg, "the user message stays generic")
	out := buf.String()
	require.Contains(t, out, "level=ERROR")
	require.Contains(t, out, "/tmp/x/catalog.db: denied", "the cause is logged server-side")
	require.NotContains(t, out, "spotify-s3cr3t", "secret values are redacted")

	buf.Reset()
	msg = sanitize(maintenance.Safe(fmt.Errorf("sync: %w", context.Canceled)))
	require.NotContains(t, msg, "/")
	out = buf.String()
	require.Contains(t, out, "level=INFO", "cancellation is not an error")
	require.NotContains(t, out, "level=ERROR")
	require.True(t, strings.Contains(out, "context canceled"), out)
}

func TestOperationLogErrorHandlerLevels(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := operationLogErrorHandler(logger)

	h(fmt.Errorf("read operation log: %w", context.Canceled))
	require.NotContains(t, buf.String(), "level=ERROR", "cancellation is not an error")
	require.NotContains(t, buf.String(), "write failed")

	buf.Reset()
	h(errors.New("read operation log: permission denied"))
	require.Contains(t, buf.String(), "level=ERROR")
	require.NotContains(t, buf.String(), "write failed", "a read failure is not labeled a write failure")
}
