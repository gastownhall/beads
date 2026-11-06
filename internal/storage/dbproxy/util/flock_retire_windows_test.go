//go:build windows

package util

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// TestRetirableLockRetireWhileHeld covers the retirement contract that makes
// the reverse migration safe: once a lease retires its path, the name stays
// unavailable to openers until the lease unlocks, and is gone for good
// afterwards (#6884). Windows refuses every open of a delete-pending name;
// wine (used for cross-platform evidence) does not model that refusal, so
// the refusal clause runs only where the platform enforces it and skips
// with a reason elsewhere.
func TestRetirableLockRetireWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.lock")

	lock, err := TryRetirableLock(path)
	require.NoError(t, err)
	require.NoError(t, lock.Retire())

	t.Run("name unavailable to openers until unlock", func(t *testing.T) {
		probe, probeErr := os.OpenFile(path, os.O_RDWR, 0o600)
		if probeErr == nil {
			_ = probe.Close()
			t.Skip("platform does not refuse opens of a delete-pending name (wine); native Windows refuses them")
		}
		_, err := openForRetire(path)
		require.Error(t, err, "delete-sharing opener must not reach a retired name while the lease is held")
	})

	lock.Unlock()

	assert.NoFileExists(t, path)
	fresh, err := TryRetirableLock(path)
	require.NoError(t, err, "fresh acquisition must succeed after unlock")
	fresh.Unlock()
}

// openForRetire is the deletion-handle open Retire uses: a competing
// retirement attempt must fail while the name is retired.
func openForRetire(path string) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		path16,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}
