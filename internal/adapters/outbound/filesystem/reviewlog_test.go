package filesystem_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/filesystem"
	"github.com/javiyt/spotwufamily/internal/application/admin"
)

func TestReviewLogAppendsWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "artists.yaml.reviews.jsonl")
	log := filesystem.NewReviewLog(path)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

	require.NoError(t, log.Append(context.Background(), admin.ReviewLogEntry{At: at, Actor: "javi", Slug: "streetlife", SpotifyID: "0FCYBhXjpfQIQWNKiVEDg5", Action: "reject", To: "rejected", Note: "wrong act", IDsAfter: []string{"7CrVv4zLga71gdlMOMfjXL"}}))
	require.NoError(t, log.Append(context.Background(), admin.ReviewLogEntry{At: at.Add(time.Minute), Actor: "javi", Slug: "k-g-b", SpotifyID: "2gKwx0Lm0JoOrN2mY9Z0PG", Action: "quarantine"}))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var lines []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		lines = append(lines, m)
	}
	require.Len(t, lines, 2)
	require.Equal(t, "2026-10-20T09:00:00Z", lines[0]["at"])
	require.Equal(t, "streetlife", lines[0]["artist"])
	require.Equal(t, []any{}, lines[0]["ids_before"])
	require.Equal(t, "quarantine", lines[1]["action"])

	require.Error(t, log.Append(canceled(), admin.ReviewLogEntry{}))
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
