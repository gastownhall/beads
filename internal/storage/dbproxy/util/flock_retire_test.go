package util

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRetirableLockRetire checks basic retirement off Windows: retirement
// removes the path like a plain removal, is idempotent, and leaves the path
// acquirable again once the lease is released.
func TestRetirableLockRetire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	lock, err := TryRetirableLock(path)
	require.NoError(t, err)
	require.NoError(t, lock.Retire())
	require.NoError(t, lock.Retire(), "retirement is idempotent")
	lock.Unlock()

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "retired lock file must be gone once the lease is released")

	fresh, err := TryRetirableLock(path)
	require.NoError(t, err, "a retired path must be acquirable again")
	fresh.Unlock()
	_ = os.Remove(path)
}

// TestRetirableLockRetireToleratesMissingPath checks that retiring a path
// someone else already removed is not an error.
func TestRetirableLockRetireToleratesMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.lock")

	lock, err := TryRetirableLock(path)
	require.NoError(t, err)
	defer lock.Unlock()

	require.NoError(t, os.Remove(path))
	require.NoError(t, lock.Retire())
}

// TestRetirableLockRefusesOrdinaryAndReleasedLeases checks the lease-state
// refusals: ordinary TryLock leases and already-released leases are never
// retirable.
func TestRetirableLockRefusesOrdinaryAndReleasedLeases(t *testing.T) {
	dir := t.TempDir()

	ordinary, err := TryLock(filepath.Join(dir, "ordinary.lock"))
	require.NoError(t, err)
	defer ordinary.Unlock()
	require.Error(t, ordinary.Retire(), "ordinary leases are not retirable")
	_, statErr := os.Stat(filepath.Join(dir, "ordinary.lock"))
	require.NoError(t, statErr, "a refused retirement must not remove the file")

	released, err := TryRetirableLock(filepath.Join(dir, "released.lock"))
	require.NoError(t, err)
	released.Unlock()
	require.Error(t, released.Retire(), "released leases are not retirable")
}
