package util

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetirableLockRetire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "life.lock")
	lease, err := TryRetirableLock(path)
	require.NoError(t, err)

	require.NoError(t, lease.Retire())
	require.NoError(t, lease.Retire(), "retiring twice is a no-op")
	lease.Unlock()
	require.NoFileExists(t, path)

	fresh, err := TryRetirableLock(path)
	require.NoError(t, err, "a retired pathname is free for a fresh lease")
	fresh.Unlock()
	require.FileExists(t, path, "Unlock alone never removes the file")
}
