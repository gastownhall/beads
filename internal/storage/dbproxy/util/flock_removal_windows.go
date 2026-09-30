//go:build windows

package util

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/steveyegge/beads/internal/lockfile"
	"golang.org/x/sys/windows"
)

var reOpenLockFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// TryLockForRemoval is TryLock with Windows delete sharing enabled. Use it only
// for a control file that the lease holder must retire with RemoveWhileHeld.
// Unlock alone does not remove the file.
func TryLockForRemoval(lockPath string) (*Lock, error) {
	if err := reOpenLockFile.Find(); err != nil {
		return nil, fmt.Errorf("util: resolving ReOpenFile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("util: creating lock directory: %w", err)
	}
	// Go 1.26 forwards this flag to CreateFile while retaining native path handling.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0600) //nolint:gosec // caller-derived control path
	if err != nil {
		return nil, fmt.Errorf("util: opening lock file: %w", err)
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info)
	if err == nil && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		err = fmt.Errorf("lock file is a reparse point: %w", os.ErrInvalid)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open lock for removal", Path: lockPath, Err: errors.Join(err, f.Close())}
	}
	// Let os.OpenFile handle creation and native path forms, then reopen the same
	// object with delete sharing before acquiring a lock on the replacement handle.
	raw, _, reopenErr := reOpenLockFile.Call(f.Fd(), windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, 0)
	closeErr := f.Close()
	handle := windows.Handle(raw)
	if handle == windows.InvalidHandle {
		return nil, fmt.Errorf("util: reopening lock file: %w", &os.PathError{Op: "reopen", Path: lockPath, Err: reopenErr})
	}
	if closeErr != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("util: closing initial lock handle: %w", closeErr)
	}
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("util: disabling lock handle inheritance: %w", err)
	}
	f = os.NewFile(raw, lockPath)
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// RemoveWhileHeld marks the lock file for deletion after the held handle closes.
// Unlike os.Remove, it preserves Windows pathname exclusion until Unlock.
// The lock must have been acquired with TryLockForRemoval.
func (l *Lock) RemoveWhileHeld() error {
	defer runtime.KeepAlive(l)
	if err := reOpenLockFile.Find(); err != nil {
		return fmt.Errorf("util: resolving ReOpenFile: %w", err)
	}
	raw, _, reopenErr := reOpenLockFile.Call(l.f.Fd(), windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, 0)
	handle := windows.Handle(raw)
	if handle == windows.InvalidHandle {
		return &os.PathError{Op: "remove held lock", Path: l.f.Name(), Err: reopenErr}
	}
	err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0)
	if err == nil {
		// Classic disposition keeps the name delete-pending until the lease closes.
		deleteFile := byte(1) // FILE_DISPOSITION_INFO contains one BOOLEAN.
		err = windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteFile, 1)
	}
	if err = errors.Join(err, windows.CloseHandle(handle)); err != nil {
		return &os.PathError{Op: "remove held lock", Path: l.f.Name(), Err: err}
	}
	return nil
}
