//go:build windows

package workspacegate

import (
	"errors"

	"golang.org/x/sys/windows"
)

func canonicalParent(path string) (resolved string, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, windows.CloseHandle(h))
	}()

	// A volume GUID gives drive-letter and directory-mounted aliases the same
	// identity. EvalSymlinks cannot walk through a volume-mounted ancestor.
	const volumeNameGUID = 0x1
	flags := uint32(volumeNameGUID)
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
		if errors.Is(err, windows.ERROR_PATH_NOT_FOUND) && flags == volumeNameGUID {
			// Network shares have no volume GUID; their DOS form is a UNC path.
			flags = 0
			continue
		}
		if err != nil {
			return "", err
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		return windows.UTF16ToString(buf[:n]), nil
	}
}
