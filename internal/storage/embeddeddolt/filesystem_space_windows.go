//go:build cgo && windows

package embeddeddolt

import "golang.org/x/sys/windows"

func filesystemHasAvailableSpace(path string) (bool, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	var availableBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &availableBytes, nil, nil); err != nil {
		return false, err
	}
	return availableBytes != 0, nil
}
