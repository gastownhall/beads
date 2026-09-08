//go:build windows

package linear

import (
	"golang.org/x/sys/windows"
)

func isProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}
