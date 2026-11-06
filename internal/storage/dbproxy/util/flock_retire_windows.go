//go:build windows

package util

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openRetirableLockFile opens lockPath like openLockFile but also shares
// delete access, so the holder can later retire the file without closing it.
// The lease itself does not request DELETE access: ordinary openers, which do
// not share delete, stay compatible with it and still meet byte-range
// contention rather than an open-time sharing violation. The handle is not
// inheritable and an existing file is not truncated.
func openRetirableLockFile(lockPath string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: lockPath, Err: err}
	}
	h, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: lockPath, Err: err}
	}
	return os.NewFile(uintptr(h), lockPath), nil
}

// retireLockFile marks the held lock file for deletion through a short-lived
// second handle, using classic disposition: the name disappears when the
// last handle (the lease) closes, and new opens fail until then.
func retireLockFile(f *os.File) error {
	name, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return &os.PathError{Op: "retire", Path: f.Name(), Err: err}
	}
	h, err := windows.CreateFile(
		name,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "retire", Path: f.Name(), Err: err}
	}
	disposition := struct{ DeleteFile bool }{DeleteFile: true}
	//nolint:gosec // G103: FILE_DISPOSITION_INFO is passed by pointer, as SetFileInformationByHandle requires.
	err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition)))
	if closeErr := windows.CloseHandle(h); err == nil {
		err = closeErr
	}
	if err != nil {
		return &os.PathError{Op: "retire", Path: f.Name(), Err: err}
	}
	return nil
}
