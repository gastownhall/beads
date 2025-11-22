//go:build unix

package main

import (
	"os"
	"syscall"
)

// isSandboxed detects if we're running in a sandboxed environment where process signaling is restricted.
//
// Detection strategy:
// 1. Check if we can send signal 0 (existence check) to our own process
// 2. If we get EPERM (operation not permitted), we're likely sandboxed
//
// This works because:
// - Normal environments: processes can signal themselves
// - Sandboxed environments (Codex, containers): signal operations restricted by MAC/seccomp
//
// False positives are rare because:
// - Normal users can always signal their own processes
// - EPERM only occurs when OS-level security policies block the syscall
//
// Implements bd-u3t: Phase 2 auto-detection for GH #353
func isSandboxed() bool {
	// Try to send signal 0 (existence check) to our own process
	// Signal 0 doesn't actually send a signal, just checks permissions
	pid := os.Getpid()
	err := syscall.Kill(pid, 0)

	if err == syscall.EPERM {
		// EPERM = Operation not permitted
		// We can't signal our own process, likely sandboxed
		return true
	}

	// No error or different error = not sandboxed
	// Different errors (ESRCH = no such process) shouldn't happen for our own PID
	return false
}
