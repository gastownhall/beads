//go:build unix || linux || darwin

package lockfile

import (
	"syscall"
)

// isProcessRunning checks if a process with the given PID is running
func isProcessRunning(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
