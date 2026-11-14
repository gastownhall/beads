//go:build windows

package workspacegate

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// GetFinalPathNameByHandle flags; x/sys exports the function but not
// these constants.
const (
	fileNameNormalized = 0x0
	volumeNameDOS      = 0x0
	volumeNameGUID     = 0x1
)

// maxFinalPathLen bounds the final-path buffer (UTF-16 units): the
// longest path Windows can name.
const maxFinalPathLen = 32768

// canonicalParent resolves an existing gate parent directory to one
// spelling shared by every path that reaches it.
//
// filepath.EvalSymlinks cannot do this on Windows: since Go 1.23
// (winsymlink=1) it refuses to walk through a mount-point reparse point,
// so every parent beneath a directory-mounted volume (a Dev Drive at
// C:\Code) or a junction fails with "The system cannot find the path
// specified". Instead the parent is opened (following reparse points)
// and its final path asked of the handle: the DOS spelling first (a
// drive-letter path, or \\srv\share\... on a network share, so a mapped
// or subst drive letter gives way to the share or the backing
// directory), volume-GUID spelling only for a volume with no DOS name,
// and EvalSymlinks last, for filesystems that do not answer the query.
// The spelling is chosen per volume by the mount manager, so
// drive-letter, folder-mount, junction, case-variant, 8.3 and \\?\
// spellings of one directory converge; for a plain local path (a drive
// letter that is neither mapped nor subst, and no junction or mount
// point on the way) the result equals what EvalSymlinks returned before.
//
// A missing parent returns the open's fs.ErrNotExist error, and nothing
// is ever created (OPEN_EXISTING).
func canonicalParent(p string) (string, error) {
	h, openErr := openDirForQuery(p)
	if openErr != nil && errors.Is(openErr, fs.ErrNotExist) {
		return "", openErr
	}
	if openErr == nil {
		defer func() { _ = windows.CloseHandle(h) }() // query-only handle: nothing to flush
	}
	query := func(flags uint32) func() (string, error) {
		return func() (string, error) {
			if openErr != nil {
				return "", openErr
			}
			s, err := finalPathName(h, flags)
			if err != nil {
				return "", err
			}
			return finalPathToGateParent(s)
		}
	}
	return resolveParent(
		query(fileNameNormalized|volumeNameDOS),
		query(fileNameNormalized|volumeNameGUID),
		func() (string, error) { return filepath.EvalSymlinks(p) },
	)
}

// openDirForQuery opens an existing directory for a metadata query only:
// no access rights, full sharing, reparse points followed.
func openDirForQuery(p string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(longPathForOpen(p))
	if err != nil {
		return windows.InvalidHandle, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	h, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return windows.InvalidHandle, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	return h, nil
}

// longPathForOpen adds the \\?\ (or \\?\UNC\) prefix to an absolute path
// long enough to need it, at the threshold Go's own fixLongPath uses
// (248, CreateDirectory's limit, below MAX_PATH). Shorter, relative,
// and already-prefixed paths are returned unchanged.
func longPathForOpen(p string) string {
	if len(p) < 248 || strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return p
	}
	p = filepath.Clean(p)
	switch {
	case strings.HasPrefix(p, `\\`):
		return `\\?\UNC\` + p[2:]
	case isDriveAbs(p):
		return win32FilePrefix + p
	}
	return p
}

// finalPathName asks the handle for its final path with the given flags,
// growing the buffer to the size Windows reports.
func finalPathName(h windows.Handle, flags uint32) (string, error) {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		// Too small: n is the required size including the terminator.
		size := int(n)
		if size <= len(buf) {
			size = len(buf) + 1
		}
		if size > maxFinalPathLen {
			return "", fmt.Errorf("final path needs %d UTF-16 units, over the %d limit", n, maxFinalPathLen)
		}
		buf = make([]uint16, size)
	}
}
