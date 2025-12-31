//go:build unix || linux || darwin

package lockfile

import (
	"syscall"
)

// isProcessRunning checks if a process with the given PID is running
func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false // Invalid PID (0 would signal our process group, not a specific process)
	}
	return syscall.Kill(pid, 0) == nil
}
