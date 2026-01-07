package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSocketPathEnvOverride verifies that BD_SOCKET env var overrides default socket path.
func TestSocketPathEnvOverride(t *testing.T) {
	// Create isolated temp directory
	tmpDir := t.TempDir()
	customSocket := filepath.Join(tmpDir, "custom.sock")

	// Set environment for isolation
	t.Setenv("BD_SOCKET", customSocket)

	// Verify getSocketPath returns custom path
	got := getSocketPath()
	if got != customSocket {
		t.Errorf("getSocketPath() = %q, want %q", got, customSocket)
	}
}

// TestSocketPathForPIDEnvOverride verifies that BD_SOCKET env var overrides PID-derived path.
func TestSocketPathForPIDEnvOverride(t *testing.T) {
	// Create isolated temp directory
	tmpDir := t.TempDir()
	customSocket := filepath.Join(tmpDir, "custom.sock")

	// Set environment for isolation
	t.Setenv("BD_SOCKET", customSocket)

	// Verify getSocketPathForPID returns custom path (ignoring pidFile)
	pidFile := "/some/other/path/daemon.pid"
	got := getSocketPathForPID(pidFile)
	if got != customSocket {
		t.Errorf("getSocketPathForPID(%q) = %q, want %q", pidFile, got, customSocket)
	}
}

// TestSocketPathDefaultBehavior verifies default behavior when BD_SOCKET is not set.
func TestSocketPathDefaultBehavior(t *testing.T) {
	// Ensure BD_SOCKET is not set (t.Setenv restores after test)
	t.Setenv("BD_SOCKET", "")

	// Verify getSocketPathForPID derives from PID file path
	pidFile := "/path/to/.beads/daemon.pid"
	got := getSocketPathForPID(pidFile)
	want := "/path/to/.beads/bd.sock"
	if got != want {
		t.Errorf("getSocketPathForPID(%q) = %q, want %q", pidFile, got, want)
	}
}

// TestDaemonSocketIsolation demonstrates that two test instances can use different sockets.
// This is the key pattern for parallel test isolation.
func TestDaemonSocketIsolation(t *testing.T) {
	// Simulate two parallel tests with different socket paths
	tests := []struct {
		name       string
		sockSuffix string
	}{
		{"instance_a", "a.sock"},
		{"instance_b", "b.sock"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each sub-test gets isolated socket path in its own temp dir
			socketPath := filepath.Join(t.TempDir(), tt.sockSuffix)
			t.Setenv("BD_SOCKET", socketPath)

			got := getSocketPath()
			if got != socketPath {
				t.Errorf("getSocketPath() = %q, want %q", got, socketPath)
			}

			// Verify paths are unique per instance
			if !strings.Contains(got, tt.sockSuffix) {
				t.Errorf("getSocketPath() = %q, want it to contain %q", got, tt.sockSuffix)
			}
		})
	}
}
