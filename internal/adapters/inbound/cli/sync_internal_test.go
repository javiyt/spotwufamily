package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/javiyt/spotwufamily/internal/application/catalogsync"
	"github.com/stretchr/testify/require"
)

func TestSyncErrorExitCodeReturnsSuccessForPartialSync(t *testing.T) {
	err := &catalogsync.PartialSyncError{Err: errors.New("quota exceeded")}

	require.Equal(t, 0, syncErrorExitCode(err, nil))
}

func TestSyncErrorExitCodeReturnsFailureForPartialSyncSnapshotError(t *testing.T) {
	err := &catalogsync.PartialSyncError{Err: errors.New("quota exceeded")}

	require.Equal(t, 1, syncErrorExitCode(err, errors.New("snapshot failed")))
}

func TestSyncErrorExitCodeReturnsFailureForRegularError(t *testing.T) {
	require.Equal(t, 1, syncErrorExitCode(errors.New("spotify down"), nil))
}

func TestSpotifyMaxRetryAfterParsesEnvironment(t *testing.T) {
	cases := map[string]time.Duration{
		"":       0, // the Spotify client then applies its 10 minute default
		"  ":     0,
		"90s":    90 * time.Second,
		"2m":     2 * time.Minute,
		"30":     30 * time.Second,
		" 15 ":   15 * time.Second,
		"bogus":  0,
		"1.5min": 0,
	}
	for value, want := range cases {
		t.Setenv("SPOTIFY_MAX_RETRY_AFTER", value)
		require.Equal(t, want, spotifyMaxRetryAfter(), "SPOTIFY_MAX_RETRY_AFTER=%q", value)
	}
}
