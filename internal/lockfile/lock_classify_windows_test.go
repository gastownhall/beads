//go:build windows

package lockfile

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// Over \\wsl$ / \\wsl.localhost the 9P redirector answers LockFileEx with
// "Incorrect function"; that, and the explicit not-supported codes, must be
// told apart from a lock held by another process.
func TestClassifyLockErr(t *testing.T) {
	for _, tc := range []struct {
		name        string
		errno       windows.Errno
		unsupported bool
	}{
		{"ERROR_INVALID_FUNCTION", windows.ERROR_INVALID_FUNCTION, true},
		{"ERROR_NOT_SUPPORTED", windows.ERROR_NOT_SUPPORTED, true},
		{"ERROR_CALL_NOT_IMPLEMENTED", windows.ERROR_CALL_NOT_IMPLEMENTED, true},
		{"ERROR_INVALID_HANDLE", windows.ERROR_INVALID_HANDLE, false},
		{"ERROR_ACCESS_DENIED", windows.ERROR_ACCESS_DENIED, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLockErr(tc.errno)
			if !errors.Is(got, tc.errno) {
				t.Errorf("classifyLockErr(%v) = %v, lost the original errno", tc.errno, got)
			}
			if IsLocked(got) || errors.Is(got, ErrLockBusy) {
				t.Errorf("classifyLockErr(%v) = %v, reads as busy", tc.errno, got)
			}
			if !tc.unsupported {
				if got != error(tc.errno) {
					t.Errorf("classifyLockErr(%v) = %#v, want the errno unchanged", tc.errno, got)
				}
				return
			}
			if !errors.Is(got, ErrUnsupported) {
				t.Errorf("classifyLockErr(%v) = %v, want a match for ErrUnsupported", tc.errno, got)
			}
		})
	}
	if got := classifyLockErr(nil); got != nil {
		t.Errorf("classifyLockErr(nil) = %v, want nil", got)
	}
}
