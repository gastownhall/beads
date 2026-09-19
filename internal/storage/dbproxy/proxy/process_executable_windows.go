//go:build windows

package proxy

import (
	"errors"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func processExecutableBasename(pid int) (basename string, gone bool, err error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", true, nil
		}
		return "", false, err
	}
	defer func() { _ = windows.CloseHandle(process) }()

	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", false, err
	}
	return filepath.Base(syscall.UTF16ToString(buffer[:size])), false, nil
}
