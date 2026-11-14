//go:build windows

package workspacegate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// GetFinalPathNameByHandle volume-name and file-name flags. x/sys
// v0.49.0 exports the call but not these constants; the values are from
// fileapi.h.
const (
	fileNameNormalized = 0x0 // FILE_NAME_NORMALIZED
	volumeNameDOS      = 0x0 // VOLUME_NAME_DOS
	volumeNameGUID     = 0x1 // VOLUME_NAME_GUID
)

// maxFinalPathUTF16 bounds the grow-to-reported-size retry loop around
// GetFinalPathNameByHandle: the longest path Win32 reports fits 32768
// UTF-16 units.
const maxFinalPathUTF16 = 32768

// maxPathLen is the classic MAX_PATH: a path at least this long needs the
// extended-length prefix before CreateFile will open it. Go's own file
// operations apply the same fix when needed, so the gate path itself can
// keep its plain spelling.
const maxPathLen = 260

// canonicalParent resolves p, the parent of a guarded directory, to the
// one canonical spelling every alias of that directory shares.
//
// filepath.EvalSymlinks cannot do this on Windows: since Go 1.23
// (GODEBUG winsymlink=1, the default) it refuses to traverse the
// IO_REPARSE_TAG_MOUNT_POINT entries that a directory-mounted volume (a
// Dev Drive mounted at C:\Code) and a plain junction both are, so every
// workspace beneath one failed with "gate parent ... must exist" even
// though the directory exists. When the mount point is the LAST component
// it is worse: EvalSymlinks returns it unevaluated, so the junction and
// target spellings of one directory produced two different gate keys.
//
// Instead the parent is opened as a directory handle — CreateFile follows
// reparse points unless asked not to — and the handle's final path is
// queried. The volume name is chosen per volume by the mount manager, so
// the drive-letter, folder-mount, junction and case-variant spellings of
// one directory all yield one string. resolveParent picks the spelling
// (DOS first, volume GUID only for a volume with no DOS name,
// EvalSymlinks as the never-regress fallback) and finalPathToGateParent
// converts it.
//
// A handle that cannot be open hands its error to the caller unchanged:
// a missing parent stays fs.ErrNotExist so forDir's "must exist"
// contract holds exactly as it does on Unix.
func canonicalParent(p string) (string, error) {
	h, err := openParentHandle(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	query := func(volumeFlag uint32) func() (string, error) {
		return func() (string, error) {
			s, err := finalPathName(h, fileNameNormalized|volumeFlag)
			if err != nil {
				return "", err
			}
			return finalPathToGateParent(s)
		}
	}
	return resolveParent(
		query(volumeNameDOS),
		query(volumeNameGUID),
		func() (string, error) { return filepath.EvalSymlinks(p) },
	)
}

// openParentHandle opens p — an absolute, cleaned directory path —
// following reparse points: no FILE_FLAG_OPEN_REPARSE_POINT, so a mount
// point or junction in the ancestry resolves to the physical directory,
// and FILE_FLAG_BACKUP_SEMANTICS, which opening a directory requires.
// OPEN_EXISTING means nothing is ever created (the gate parent must
// already exist), access 0 suffices because only the name is queried,
// and full sharing keeps the probe out of other handles' way. Paths at
// least MAX_PATH long get the extended-length prefix, as Go's own file
// operations do.
func openParentHandle(p string) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(extendedLengthPath(p))
	if err != nil {
		return 0, fmt.Errorf("workspacegate: canonicalizing %s: %w", p, err)
	}
	h, err := windows.CreateFile(
		path,
		0, // access: the name query needs none
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		// Returned unchanged: ERROR_PATH_NOT_FOUND and
		// ERROR_FILE_NOT_FOUND both satisfy errors.Is(err,
		// fs.ErrNotExist) on this Go, so forDir's wrap and its callers
		// see the same missing-parent signal they see on Unix.
		return 0, err
	}
	return h, nil
}

// finalPathName queries the final path of h under the given volume-name
// flag, growing the buffer to the reported size (once is normally
// enough; the bound stops a pathological loop).
func finalPathName(h windows.Handle, flags uint32) (string, error) {
	buf := make([]uint16, 300)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", errors.New("workspacegate: GetFinalPathNameByHandle returned an empty path")
		}
		if n < uint32(len(buf)) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		if uint32(len(buf)) >= maxFinalPathUTF16 {
			return "", fmt.Errorf("workspacegate: final path needs %d UTF-16 units, over the %d bound", n, maxFinalPathUTF16)
		}
		buf = make([]uint16, n+1)
	}
}

// extendedLengthPath prefixes p with \\?\ (or \\?\UNC\ for UNC paths)
// when it is at least MAX_PATH long, so CreateFile can open it; shorter
// paths keep their plain spelling, and an already-prefixed path is
// returned as-is.
func extendedLengthPath(p string) string {
	if len(p) < maxPathLen || strings.HasPrefix(p, `\\?\`) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[1:]
	}
	return `\\?\` + p
}
