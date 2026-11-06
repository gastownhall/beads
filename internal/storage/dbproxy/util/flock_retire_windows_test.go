//go:build windows

package util

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
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

// TestTryRetirableLockRetireWindowsPaths runs the retire-while-held lifecycle
// across the pathname forms a lifecycle lock can take: relative, Unicode,
// longer than MAX_PATH and already extended-length.
func TestTryRetirableLockRetireWindowsPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	long := filepath.Join(root, strings.Repeat("a", 120), strings.Repeat("b", 120), "lock")
	extended := `\\?\` + filepath.Join(root, "extended", "lock")
	if strings.HasPrefix(root, `\\`) {
		extended = `\\?\UNC\` + strings.TrimPrefix(filepath.Join(root, "extended", "lock"), `\\`)
	}
	for _, tc := range []struct{ name, path string }{
		{"ordinary", filepath.Join(root, "ordinary", "lock")},
		{"relative", filepath.Join("relative", "lock")},
		{"unicode", filepath.Join(root, "café-雪", "lock")},
		{"long", long},
		{"extended", extended},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := TryRetirableLock(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if held != nil {
					held.Unlock()
				}
			})
			for _, acquire := range []func(string) (*Lock, error){TryLock, TryRetirableLock} {
				other, err := acquire(tc.path)
				if other != nil {
					other.Unlock()
				}
				if !lockfile.IsLocked(err) {
					t.Fatalf("competing acquisition = %v, want lock contention", err)
				}
			}
			if err := held.Retire(); err != nil {
				t.Fatalf("retire while holding lifecycle lock: %v", err)
			}
			for _, acquire := range []func(string) (*Lock, error){TryLock, TryRetirableLock} {
				other, err := acquire(tc.path)
				if other != nil {
					other.Unlock()
				}
				if err == nil {
					t.Fatal("acquired a lock while original handle was delete-pending")
				}
			}
			held.Unlock()
			held = nil
			if _, err := os.Stat(tc.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retired lock after Unlock: %v", err)
			}
			fresh, err := TryRetirableLock(tc.path)
			if err != nil {
				t.Fatalf("fresh acquisition after retirement: %v", err)
			}
			fresh.Unlock()
			if _, err := os.Stat(tc.path); err != nil {
				t.Fatalf("Unlock without retirement must retain the file: %v", err)
			}
		})
	}
}
