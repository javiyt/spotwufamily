package cli

import (
	"errors"
	"testing"

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
