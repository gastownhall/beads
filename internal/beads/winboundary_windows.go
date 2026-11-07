//go:build windows

package beads

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hostWindowsFacts gathers the live host inputs to the Windows boundary.
func hostWindowsFacts() windowsFacts {
	f := windowsFacts{Getenv: os.Getenv, TempDir: os.TempDir(), Resolve: resolveWindowsFinalPath}
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

// GetFinalPathNameByHandle flags, not exported by the pinned x/sys/windows.
const (
	winFileNameNormalized = 0x0
	winVolumeNameDOS      = 0x0
)

// winExtendedSpelling returns the \\?\ form of a cleaned absolute path, which
// the OS opens without re-normalizing it: \\?\C:\x or \\?\UNC\server\share\x.
func winExtendedSpelling(path string) string {
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

// resolveWindowsFinalPath is the host winResolver: it opens path (a cleaned
// absolute path) following links and asks the OS for the handle's normalized
// DOS path, which resolves junctions, symlinks, subst drives and short
// names alike.
func resolveWindowsFinalPath(path string) (string, bool, error) {
	ext := winExtendedSpelling(path)
	name, err := windows.UTF16PtrFromString(ext)
	if err != nil {
		return "", false, err
	}
	// Access 0 needs no rights on the object; BACKUP_SEMANTICS opens
	// directories; omitting OPEN_REPARSE_POINT makes the OS follow links.
	h, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			_, lerr := os.Lstat(ext)
			return "", false, winOpenError(err, lerr)
		}
		return "", false, err
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", false, err
	}
	isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0

	n := uint32(windows.MAX_PATH)
	for {
		buf := make([]uint16, n)
		got, err := windows.GetFinalPathNameByHandle(h, &buf[0], n, winFileNameNormalized|winVolumeNameDOS)
		if err != nil {
			return "", false, err
		}
		// On success got excludes the NUL; when buf is too small it is the
		// size needed, including the NUL.
		if got < n {
			return windows.UTF16ToString(buf[:got]), isDir, nil
		}
		n = got + 1
	}
}
