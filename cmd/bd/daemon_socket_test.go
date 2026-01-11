package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
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

// TestSocketPathForPIDLongPath verifies that long workspace paths use shortened socket paths.
// This fixes GH#1001 where pytest temp directories exceeded macOS's 104-byte socket path limit.
func TestSocketPathForPIDLongPath(t *testing.T) {
	t.Setenv("BD_SOCKET", "")

	// Create a path that would exceed the 103-byte limit when .beads/bd.sock is appended
	// /long/path/.beads/daemon.pid -> workspace is /long/path
	// socket would be /long/path/.beads/bd.sock
	longWorkspace := "/" + strings.Repeat("a", 90) // 91 bytes
	pidFile := filepath.Join(longWorkspace, ".beads", "daemon.pid")

	got := getSocketPathForPID(pidFile)

	// Should NOT be the natural path (which would be too long)
	naturalPath := filepath.Join(longWorkspace, ".beads", "bd.sock")
	if got == naturalPath {
		t.Errorf("getSocketPathForPID should use short path for long workspaces, got natural path %q (%d bytes)",
			got, len(got))
	}

	// Should be in /tmp/beads-{hash}/
	if !strings.HasPrefix(got, "/tmp/beads-") {
		t.Errorf("getSocketPathForPID(%q) = %q, want path starting with /tmp/beads-", pidFile, got)
	}

	// Should end with bd.sock
	if !strings.HasSuffix(got, "/bd.sock") {
		t.Errorf("getSocketPathForPID(%q) = %q, want path ending with /bd.sock", pidFile, got)
	}

	// Should be under the limit
	if len(got) > 103 {
		t.Errorf("getSocketPathForPID returned path of %d bytes, want <= 103", len(got))
	}
}

// TestSocketPathForPIDClientDaemonAgreement verifies that getSocketPathForPID
// returns the same path as rpc.ShortSocketPath for the same workspace.
// This is critical - if they disagree, the daemon listens on one path while
// the client tries to connect to another, causing connection failures.
// This test caught the GH#1001 bug where daemon.go used filepath.Join directly
// instead of rpc.ShortSocketPath.
func TestSocketPathForPIDClientDaemonAgreement(t *testing.T) {
	t.Setenv("BD_SOCKET", "")

	tests := []struct {
		name          string
		workspacePath string
	}{
		{"short_path", "/home/user/project"},
		{"medium_path", "/Users/testuser/Documents/projects/myapp"},
		{"long_path", "/" + strings.Repeat("a", 90)}, // Forces short socket path
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// What getSocketPathForPID returns (used by daemon operations)
			pidFile := filepath.Join(tt.workspacePath, ".beads", "daemon.pid")
			fromPID := getSocketPathForPID(pidFile)

			// What rpc.ShortSocketPath returns (used by client via getSocketPath)
			fromRPC := rpc.ShortSocketPath(tt.workspacePath)

			if fromPID != fromRPC {
				t.Errorf("socket path mismatch for workspace %q:\n  getSocketPathForPID: %q\n  rpc.ShortSocketPath: %q",
					tt.workspacePath, fromPID, fromRPC)
			}
		})
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
