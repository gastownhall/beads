//go:build windows

package beads

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// windowsIsPathInSafeBoundary is the Windows arm of isPathInSafeBoundary
// (SEC-003). input is the caller-supplied path and abs its filepath.Abs form;
// both keep their original spelling so the policy can reject names the OS
// would silently normalize away.
func windowsIsPathInSafeBoundary(input, abs string) bool {
	return newWindowsBoundary(collectWindowsBoundaryInput()).inSafeBoundary(input, abs)
}

// fixedWindowsSystemRoots are denied even when the environment disagrees: a
// machine with a relocated Windows directory still denies the default
// locations, the way the POSIX prefixes are unconditional literals.
var fixedWindowsSystemRoots = []string{
	`C:\Windows`,
	`C:\Program Files`,
	`C:\Program Files (x86)`,
}

// windowsSystemRootEnvNames lists environment values that may report system
// roots; some installations relocate these directories to other drives.
// Values are only ever added to the deny set; malformed values are discarded
// by newWindowsBoundary.
var windowsSystemRootEnvNames = []string{
	"SystemRoot", "windir",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramFiles(Arm)",
}

// collectWindowsBoundaryInput gathers the per-call host facts. It performs no
// caching: facts are read once per boundary check, so an environment change
// mid-process cannot observe a stale policy.
func collectWindowsBoundaryInput() windowsBoundaryInput {
	values := append([]string(nil), fixedWindowsSystemRoots...)
	// The OS-reported Windows directory covers profiles whose SystemRoot is
	// not C:\Windows; errors leave the fixed and env-derived roots in force.
	if sysDir, err := windows.GetSystemWindowsDirectory(); err == nil && sysDir != "" {
		values = append(values, sysDir)
	}
	for _, name := range windowsSystemRootEnvNames {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			values = append(values, v)
		}
	}
	return windowsBoundaryInput{
		systemRootValues: values,
		resolve:          resolveWindowsFinalPath,
	}
}

// resolveWindowsFinalPath physically resolves path the way the kernel sees
// it: the object is opened with FILE_FLAG_BACKUP_SEMANTICS and followed
// through reparse points, and its final name is read back, so junction and
// symlink aliases are compared by where they land rather than by spelling.
// A not-yet-existing tail is allowed: the longest existing ancestor is
// resolved and the missing components re-appended, mirroring
// resolveLongestExistingAncestor on the POSIX side. Entries that exist but
// cannot be resolved (dangling reparse points, access errors), a
// non-directory ancestor, and unavailable volumes stay unresolved — those
// failures are never erased by walking above them.
func resolveWindowsFinalPath(path string) (string, bool) {
	canon, ok := winCleanPath(path)
	if !ok {
		return "", false
	}
	var tail []string
	cur := canon
	for {
		p, err := windows.UTF16PtrFromString(winExtendedPath(cur))
		if err != nil {
			return "", false
		}
		// Access 0 with full sharing: presence and identity only, no
		// reading or writing. OPEN_REPARSE_POINT is deliberately omitted so
		// the OS follows links.
		h, err := windows.CreateFile(p, 0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err == nil {
			final, fok := finalPathFromHandle(h, len(tail) > 0)
			// The handle is only a query vehicle; a close failure cannot
			// change the answer already read from it.
			_ = windows.CloseHandle(h)
			if !fok {
				return "", false
			}
			for i := 0; i < len(tail); i++ {
				final += `\` + tail[i]
			}
			return final, true
		}
		// Climb only past entries proven absent. A followed not-found open
		// can still be a dangling reparse point; os.Lstat (which opens with
		// FILE_FLAG_OPEN_REPARSE_POINT) distinguishes them, and a dangling
		// link must stay unresolved rather than be walked above.
		if !winErrAbsent(err) || winPathExists(winExtendedPath(cur)) {
			return "", false
		}
		parent, base := splitWinPath(cur)
		if base == "" {
			return "", false // volume root: nothing above to resolve
		}
		tail = append([]string{base}, tail...)
		cur = parent
	}
}

// finalPathFromHandle reads the final path of an open handle. FILE_NAME_
// NORMALIZED | VOLUME_NAME_DOS (both zero) keeps the ordinary Win32 spelling.
// needDir rejects a file: only the leaf of a resolution may be a
// non-directory; an ancestor that is a file cannot hold the missing tail.
func finalPathFromHandle(h windows.Handle, needDir bool) (string, bool) {
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return "", false
	}
	if needDir && fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", false
	}
	n := uint32(512)
	for {
		buf := make([]uint16, n)
		ret, err := windows.GetFinalPathNameByHandle(h, &buf[0], n, 0)
		if err != nil || ret == 0 {
			return "", false
		}
		if ret < n {
			return windows.UTF16ToString(buf[:ret]), true
		}
		if n >= 1<<16 {
			return "", false // refuse to grow unboundedly on a broken result
		}
		if ret > n {
			n = ret // required size including the terminator
		} else {
			n *= 2
		}
	}
}

// winErrAbsent reports whether err says the entry does not exist.
func winErrAbsent(err error) bool {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == windows.ERROR_FILE_NOT_FOUND || errno == windows.ERROR_PATH_NOT_FOUND
}

// winPathExists reports whether the entry itself is present, without
// following a final reparse point. Anything that cannot be proven absent
// counts as present, so unopenable-but-real entries stay unresolved.
func winPathExists(extended string) bool {
	_, err := os.Lstat(extended)
	return err == nil || !os.IsNotExist(err)
}

// winExtendedPath spells a canonical path in the extended form safe to pass
// to CreateFile regardless of length.
func winExtendedPath(canon string) string {
	if strings.HasPrefix(canon, `\\`) {
		return `\\?\UNC\` + canon[2:]
	}
	return `\\?\` + canon
}

// splitWinPath splits a canonical path into its parent and final component.
// Volume roots are their own parent, which stops the ascent above a volume.
func splitWinPath(p string) (parent, base string) {
	if winIsVolumeRoot(p) {
		return p, ""
	}
	i := strings.LastIndexByte(p, '\\')
	if i < 0 {
		return p, p
	}
	parent = p[:i]
	if len(parent) == 2 && isWinDrivePath(parent) {
		parent += `\`
	}
	return parent, p[i+1:]
}
