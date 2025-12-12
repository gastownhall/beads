//go:build integration
// +build integration

package main

import (
	"path/filepath"
	"testing"
)

// TestDaemonExitsWhenParentDies verifies that the daemon exits when its parent process dies
func TestDaemonExitsWhenParentDies(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Manual test - requires daemon to be run externally")
	
	// This is a manual test scenario:
	// 1. Start a shell process that spawns the daemon
	// 2. Verify daemon tracks parent PID
	// 3. Kill the shell process
	// 4. Verify daemon exits within 10-15 seconds
	//
	// To test manually:
	//   $ sh -c 'bd daemon --interval 5s & sleep 100' &
	//   $ SHELL_PID=$!
	//   $ # Check daemon.lock has parent_pid set to SHELL_PID
	//   $ kill $SHELL_PID
	//   $ # Daemon should exit within 10-15 seconds
}

func mustAbs(t *testing.T, path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Failed to get absolute path: %v", err)
	}
	return abs
}

// runGitCmd is defined in git_sync_test.go
