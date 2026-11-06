//go:build windows

package util

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// openSharingDelete opens path the way a retirable lease does, sharing
// read, write and delete with every other handle.
func openSharingDelete(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// underWine reports whether the test runs under Wine, whose wineserver (as of
// 9.0) still opens a file whose deletion is pending, where native Windows
// fails the open with ERROR_ACCESS_DENIED until the last handle closes.
func underWine() bool {
	return windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil
}

func requireNoLease(t *testing.T, what string, lease *Lock, err error) {
	t.Helper()
	if err == nil {
		lease.Unlock()
		t.Fatalf("%s succeeded on a retired, still-held lease", what)
	}
}

func TestRetirableLockRetireWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "life.lock")
	lease, err := TryRetirableLock(path)
	require.NoError(t, err)
	require.NoError(t, lease.Retire())

	ordinary, err := TryLock(path)
	requireNoLease(t, "TryLock", ordinary, err)
	retirable, err := TryRetirableLock(path)
	requireNoLease(t, "TryRetirableLock", retirable, err)
	t.Run("opens", func(t *testing.T) {
		if underWine() {
			t.Skip("Wine does not refuse opens of a delete-pending file; native Windows only")
		}
		f, err := os.Open(path)
		if err == nil {
			_ = f.Close()
			t.Fatal("os.Open succeeded on a retired, still-held lease")
		}
		require.Error(t, openSharingDelete(path), "a delete-sharing open of a retired, still-held lease")
	})

	lease.Unlock()
	_, err = os.Lstat(path)
	require.True(t, os.IsNotExist(err), "retired lock file must be gone after Unlock: %v", err)
	fresh, err := TryLock(path)
	require.NoError(t, err, "fresh acquisition after the retired lease is released")
	fresh.Unlock()
}
