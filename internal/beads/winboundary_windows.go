//go:build windows

package beads

import (
	"errors"
	"os"
	"os/user"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hostWindowsFacts gathers the live host inputs to the Windows boundary.
func hostWindowsFacts() windowsFacts {
	f := windowsFacts{Getenv: os.Getenv}
	if dir, err := windows.GetSystemWindowsDirectory(); err == nil {
		f.WindowsDir = dir
	}
	if dir, err := getProfilesDirectory(); err == nil {
		f.ProfilesDir = dir
	}
	// The account database is not affected by USERPROFILE manipulation;
	// fall back to it only when the lookup fails, as the POSIX check does.
	if u, err := user.Current(); err == nil {
		f.Home = u.HomeDir
	}
	if f.Home == "" {
		f.Home, _ = os.UserHomeDir()
	}
	return f
}

// procGetProfilesDirectoryW is userenv!GetProfilesDirectoryW, which the
// pinned x/sys/windows does not export.
var procGetProfilesDirectoryW = windows.NewLazySystemDLL("userenv.dll").NewProc("GetProfilesDirectoryW")

// getProfilesDirectory returns the parent directory of user profiles.
// The size argument is in characters, including the terminating NUL; on
// ERROR_INSUFFICIENT_BUFFER the API writes back the size it needs.
func getProfilesDirectory() (string, error) {
	if err := procGetProfilesDirectoryW.Find(); err != nil {
		return "", err
	}
	n := uint32(windows.MAX_PATH)
	for {
		buf := make([]uint16, n)
		size := n
		//nolint:gosec // G103: buf holds n UTF-16 units and size is the DWORD count, the exact GetProfilesDirectoryW contract.
		r, _, err := procGetProfilesDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
		if r != 0 {
			return windows.UTF16ToString(buf), nil
		}
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) && size > n {
			n = size
			continue
		}
		return "", err
	}
}
