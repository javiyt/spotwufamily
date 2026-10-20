package filesystem

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceLockExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db.lock")
	first := NewResourceLock(path)
	release, err := first.TryLock()
	require.NoError(t, err)
	defer release()

	// A separate handle on the same file must conflict, as another process would.
	_, err = NewResourceLock(path).TryLock()
	require.True(t, errors.Is(err, ErrLocked), "got %v", err)
	// The same handle cannot be taken twice either.
	_, err = first.TryLock()
	require.True(t, errors.Is(err, ErrLocked), "got %v", err)
}

func TestResourceLockReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db.lock")
	release, err := NewResourceLock(path).TryLock()
	require.NoError(t, err)
	release()
	release() // idempotent

	again, err := NewResourceLock(path).TryLock()
	require.NoError(t, err)
	again()
}

func TestResourceLockCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "catalog.db.lock")
	release, err := NewResourceLock(path).TryLock()
	require.NoError(t, err)
	release()
	require.FileExists(t, path)
}
