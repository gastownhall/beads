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

// createJunction makes link a directory junction (a mount-point reparse
// point) to target, which needs no privilege on native Windows.
func createJunction(link, target string) error {
	if err := os.Mkdir(link, 0o700); err != nil {
		return err
	}
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	// REPARSE_DATA_BUFFER, MountPointReparseBuffer variant.
	pathBuf := append(append([]uint16{}, substitute...), printName...)
	buf := make([]byte, 16+2*len(pathBuf))
	le := func(off int, v uint32, size int) {
		for i := 0; i < size; i++ {
			buf[off+i] = byte(v >> (8 * i))
		}
	}
	le(0, windows.IO_REPARSE_TAG_MOUNT_POINT, 4)
	le(4, uint32(8+2*len(pathBuf)), 2)       // ReparseDataLength
	le(8, 0, 2)                              // SubstituteNameOffset
	le(10, uint32(2*(len(substitute)-1)), 2) // SubstituteNameLength
	le(12, uint32(2*len(substitute)), 2)     // PrintNameOffset
	le(14, uint32(2*(len(printName)-1)), 2)  // PrintNameLength
	for i, c := range pathBuf {
		le(16+2*i, uint32(c), 2)
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var returned uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &returned, nil)
}

// TestTryRetirableLockRefusesReparsePoint pins that a retirable lease never
// lands on whatever a final symlink or junction points at: acquisition
// fails, and the link and its target are left as they were.
func TestTryRetirableLockRefusesReparsePoint(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		require.NoError(t, os.WriteFile(target, []byte("target"), 0o600))
		link := filepath.Join(dir, "life.lock")
		if err := os.Symlink(target, link); err != nil {
			if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
				t.Skipf("file symlinks unavailable on this host: %v", err)
			}
			t.Fatal(err)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			if underWine() {
				t.Skipf("Wine's CreateSymbolicLinkW reports success without creating a link: %v", err)
			}
			t.Fatalf("os.Symlink did not create a symlink: %v", err)
		}

		lease, err := TryRetirableLock(link)
		if err == nil {
			lease.Unlock()
			t.Fatal("retirable acquisition followed a final symlink")
		}
		require.ErrorIs(t, err, errReparsePoint)
		info, err := os.Lstat(link)
		require.NoError(t, err, "the link must survive the refusal")
		require.NotZero(t, info.Mode()&os.ModeSymlink, "the link must still be a link")
		got, err := os.Readlink(link)
		require.NoError(t, err)
		require.Equal(t, target, got, "the link must still point at its target")
		content, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, "target", string(content), "the target must be unchanged")
		free, err := TryLock(target)
		require.NoError(t, err, "the target must not be left locked")
		free.Unlock()
	})
	t.Run("junction", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		require.NoError(t, os.Mkdir(target, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(target, "keep"), []byte("keep"), 0o600))
		link := filepath.Join(dir, "life.lock")
		if err := createJunction(link, target); err != nil {
			if underWine() {
				t.Skipf("Wine does not support FSCTL_SET_REPARSE_POINT: %v", err)
			}
			require.NoError(t, err)
		}

		lease, err := TryRetirableLock(link)
		if err == nil {
			lease.Unlock()
			t.Fatal("retirable acquisition opened a final junction")
		}
		_, err = os.Lstat(link)
		require.NoError(t, err, "the junction must survive the refusal")
		got, err := os.ReadFile(filepath.Join(target, "keep"))
		require.NoError(t, err)
		require.Equal(t, "keep", string(got), "the target must be unchanged")
	})
}

// TestRetireRefusesReplacedPath pins that retirement only deletes the file
// the lease holds: once its pathname names a replacement, Retire fails and
// leaves the replacement alone.
func TestRetireRefusesReplacedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "life.lock")
	lease, err := TryRetirableLock(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if lease != nil {
			lease.Unlock()
		}
	})

	require.NoError(t, os.Rename(path, filepath.Join(dir, "moved.lock")), "delete sharing allows renaming the held file away")
	require.NoError(t, os.WriteFile(path, []byte("replacement"), 0o600))

	require.Error(t, lease.Retire(), "retiring a pathname that now names another file")
	got, err := os.ReadFile(path)
	require.NoError(t, err, "the replacement must survive")
	require.Equal(t, "replacement", string(got))

	lease.Unlock()
	lease = nil
	got, err = os.ReadFile(path)
	require.NoError(t, err, "the replacement must survive the lease's release")
	require.Equal(t, "replacement", string(got), "releasing the lease must not delete the replacement")
}

// TestTryLockPathRemoveStillFailsWhileHeld pins that ordinary leases did not
// gain delete sharing: a retirable contender still meets lock contention,
// and removing a held TryLock file still fails with a sharing violation.
// Once released, the file is still there and removes normally.
func TestTryLockPathRemoveStillFailsWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "life.lock")
	lease, err := TryLock(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if lease != nil {
			lease.Unlock()
		}
	})

	other, err := TryRetirableLock(path)
	if other != nil {
		other.Unlock()
	}
	require.True(t, lockfile.IsLocked(err), "existing lifecycle holder refusal = %v", err)
	require.ErrorIs(t, os.Remove(path), windows.ERROR_SHARING_VIOLATION, "removing a held ordinary lease")
	require.FileExists(t, path)

	lease.Unlock()
	lease = nil
	require.FileExists(t, path, "Unlock closes the handle without removing the file")
	require.NoError(t, os.Remove(path), "a released ordinary path removes normally")
}

// TestRetireWindowsRemovalError pins that a retirement the filesystem refuses
// is reported, not swallowed: a reader that does not share delete makes
// Retire fail with the sharing violation, and once that reader closes the
// still-held lease retires normally.
func TestRetireWindowsRemovalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	held, err := TryRetirableLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if err := held.Retire(); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("non-sharing reader retirement error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := held.Retire(); err != nil {
		t.Fatalf("retire after releasing reader, lifecycle lock still held: %v", err)
	}
}

// TestTryRetirableLockWindowsOpenError pins that an unusable lock target
// (a directory, or a path under a regular file) fails with a PathError and
// leaves the blocking file untouched.
func TestTryRetirableLockWindowsOpenError(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(blocker, "lock")} {
		held, err := TryRetirableLock(path)
		if held != nil {
			held.Unlock()
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("invalid lock target %q error = %v, want PathError", path, err)
		}
	}
	if content, err := os.ReadFile(blocker); err != nil || string(content) != "retain" {
		t.Fatalf("failed open changed blocker: %q, %v", content, err)
	}
}
