//go:build windows

package daemon

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
)

func killProcess(pid int) error {
	// Use taskkill on Windows (without /F for graceful)
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to kill PID %d: %w", pid, err)
	}
	return nil
}

func forceKillProcess(pid int) error {
	// Use taskkill with /F flag for force kill
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to force kill PID %d: %w", pid, err)
	}
	return nil
}

func isProcessAlive(pid int) bool {
	// Use tasklist to check if process exists
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	// Check if output contains the PID
	return contains(string(output), strconv.Itoa(pid))
}

func contains(s, substr string) bool {
	return findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	return len(s) >= len(substr) && bytes.Contains([]byte(s), []byte(substr))
}
