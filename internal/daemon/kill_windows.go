//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
)

func killProcess(pid int) error {
	// On Windows, there's no SIGTERM equivalent for console processes.
	// The graceful RPC shutdown is already attempted before this function is called.
	// Use os.Process.Kill() which calls TerminateProcess - the reliable Windows API.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", pid, err)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("failed to kill PID %d: %w", pid, err)
	}
	return nil
}

func forceKillProcess(pid int) error {
	// On Windows, Kill() already uses TerminateProcess which is forceful
	return killProcess(pid)
}

func isProcessAlive(pid int) bool {
	// On Windows, FindProcess always succeeds, but we can check if the process
	// is actually running by trying to get its exit code via Wait with WNOHANG.
	// A simpler approach: try to open the process and check for errors.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows, we need to actually check if the process exists.
	// Signal(nil) on Windows returns an error if process doesn't exist.
	// However, os.Process.Signal is not implemented on Windows.
	// Use a different approach: try to kill with signal 0 equivalent.
	// Actually, on Windows we can check via process handle opening.
	// The simplest reliable way is to use tasklist.
	//
	// Note: os.FindProcess on Windows always succeeds regardless of whether
	// the process exists. We need to actually try to interact with it.
	// Using Release() and checking the error doesn't work either.
	//
	// Fall back to tasklist for reliability.
	return isProcessAliveTasklist(pid, proc)
}

func isProcessAliveTasklist(pid int, _ *os.Process) bool {
	// Use Windows API via tasklist to check if process exists
	// This is the most reliable method on Windows
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	// Check if output contains the PID (tasklist returns "INFO: No tasks..." if not found)
	return containsSubstring(string(output), fmt.Sprintf("%d", pid))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
