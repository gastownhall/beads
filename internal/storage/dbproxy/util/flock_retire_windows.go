//go:build windows

package util

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openLockHandle opens a dbproxy lock file for one of the retirement-safe
// accesses: read/write for the lease itself, or delete access for the
// short-lived retirement handle. Both share read, write and delete, because
// Windows denies DELETE access to a new opener while any open handle lacks
// FILE_SHARE_DELETE, and neither follows a final reparse point.
func openLockHandle(lockPath string, access uint32, disposition uint32) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(
		path16,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, // no SECURITY_ATTRIBUTES: the handle is not inheritable
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return 0, err
	}
	return h, nil
}

// openRetirable opens the lock file for TryRetirableLock. The lease handle
// never deletes through itself (no DELETE access); it only shares delete so
// that Retire's short-lived handle can, and OPEN_ALWAYS matches TryLock's
// create-if-missing behavior.
func openRetirable(lockPath string) (*os.File, error) {
	h, err := openLockHandle(lockPath, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.OPEN_ALWAYS)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), lockPath), nil
}

// fileDispositionInfo is FILE_DISPOSITION_INFO: a TRUE deleteFile requests
// the classic disposition, where the file is deleted once the last handle
// to it closes.
type fileDispositionInfo struct{ deleteFile uint8 }

// retireFile removes the lease's pathname through a short-lived deletion
// handle: the name becomes unavailable to new openers immediately, while
// the file object lives until the lease's own handle closes in Unlock. The
// deletion handle is closed before the lease is released.
func (l *Lock) retireFile() error {
	h, err := openLockHandle(l.f.Name(), windows.DELETE|windows.FILE_READ_ATTRIBUTES, windows.OPEN_EXISTING)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil // the path is already gone; the lease is retired in effect
		}
		return fmt.Errorf("util: opening lock file for retirement: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	disp := fileDispositionInfo{deleteFile: 1}
	// nolint:gosec // G103: unsafe.Pointer required for the SetFileInformationByHandle buffer
	if err := windows.SetFileInformationByHandle(
		h,
		windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&disp)),
		uint32(unsafe.Sizeof(disp)),
	); err != nil {
		return fmt.Errorf("util: marking lock file for deletion: %w", err)
	}
	return nil
}
