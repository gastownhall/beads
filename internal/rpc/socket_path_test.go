//go:build !windows

package rpc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortSocketPath_ShortPath(t *testing.T) {
	// Short paths should use the natural .beads/bd.sock location
	workspacePath := "/tmp/myrepo"
	socketPath := ShortSocketPath(workspacePath)

	expected := filepath.Join(workspacePath, ".beads", "bd.sock")
	if socketPath != expected {
		t.Errorf("ShortSocketPath(%q) = %q, want %q", workspacePath, socketPath, expected)
	}
}

func TestShortSocketPath_LongPath(t *testing.T) {
	// Long paths should use /tmp/beads-{hash}/bd.sock
	// Create a path that's definitely over 103 chars when .beads/bd.sock is added
	longPath := "/Volumes/External Drive/Dropbox/Projects/Clients/Company/product-name-with-extra-long-name"
	socketPath := ShortSocketPath(longPath)

	// Should be relocated to /tmp
	if !strings.HasPrefix(socketPath, "/tmp/beads-") {
		t.Errorf("ShortSocketPath(%q) = %q, want path starting with /tmp/beads-", longPath, socketPath)
	}

	// Should end with bd.sock
	if !strings.HasSuffix(socketPath, "/bd.sock") {
		t.Errorf("ShortSocketPath(%q) = %q, want path ending with /bd.sock", longPath, socketPath)
	}

	// Path should be short enough
	if len(socketPath) > MaxUnixSocketPath {
		t.Errorf("ShortSocketPath(%q) = %q (len=%d), want len <= %d", longPath, socketPath, len(socketPath), MaxUnixSocketPath)
	}
}

func TestShortSocketPath_Deterministic(t *testing.T) {
	// Same workspace should always produce same socket path
	workspacePath := "/Volumes/External Drive/Some/Long/Path/To/A/Repository"
	path1 := ShortSocketPath(workspacePath)
	path2 := ShortSocketPath(workspacePath)

	if path1 != path2 {
		t.Errorf("ShortSocketPath is not deterministic: %q != %q", path1, path2)
	}
}

func TestShortSocketPath_DifferentWorkspaces(t *testing.T) {
	// Different workspaces should produce different socket paths
	workspace1 := "/Volumes/External/Project1/With/Long/Path/Here"
	workspace2 := "/Volumes/External/Project2/With/Long/Path/Here"

	path1 := ShortSocketPath(workspace1)
	path2 := ShortSocketPath(workspace2)

	if path1 == path2 {
		t.Errorf("Different workspaces should produce different socket paths: both got %q", path1)
	}
}

func TestNeedsShortPath(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		want      bool
	}{
		{
			name:      "short path",
			workspace: "/tmp/myrepo",
			want:      false,
		},
		{
			name:      "medium path",
			workspace: "/Users/john/projects/myrepo",
			want:      false,
		},
		{
			name:      "long path exceeding limit",
			workspace: "/Volumes/External Drive/Dropbox/Projects/Clients/Company/product-name-with-extra-characters-to-exceed-limit",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsShortPath(tt.workspace)
			if got != tt.want {
				naturalPath := filepath.Join(tt.workspace, ".beads", "bd.sock")
				t.Errorf("NeedsShortPath(%q) = %v, want %v (natural path len=%d, limit=%d)",
					tt.workspace, got, tt.want, len(naturalPath), MaxUnixSocketPath)
			}
		})
	}
}

func TestEnsureSocketDir(t *testing.T) {
	// Test creating a /tmp/beads-* directory
	// Manually simulate the condition where we need to create the directory
	// by using a path format that matches our pattern
	testSocketPath := filepath.Join("/tmp", "beads-testxyz", "bd.sock")

	result, err := EnsureSocketDir(testSocketPath)
	if err != nil {
		t.Fatalf("EnsureSocketDir failed: %v", err)
	}

	if result != testSocketPath {
		t.Errorf("EnsureSocketDir returned %q, want %q", result, testSocketPath)
	}

	// Clean up
	_ = os.RemoveAll(filepath.Dir(testSocketPath))
}

func TestCleanupSocketDir(t *testing.T) {
	// Create a test directory in /tmp
	testDir := filepath.Join("/tmp", "beads-cleanup-test")
	if err := os.MkdirAll(testDir, 0700); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	socketPath := filepath.Join(testDir, "bd.sock")
	if err := os.WriteFile(socketPath, []byte("test"), 0600); err != nil {
		t.Fatalf("Failed to create test socket file: %v", err)
	}

	// Clean up
	if err := CleanupSocketDir(socketPath); err != nil {
		t.Errorf("CleanupSocketDir failed: %v", err)
	}

	// Directory should be removed
	if _, err := os.Stat(testDir); !os.IsNotExist(err) {
		t.Errorf("Directory %s should have been removed", testDir)
		_ = os.RemoveAll(testDir) // Clean up for next run
	}
}

func TestShortSocketPath_EdgeCase_ExactLimit(t *testing.T) {
	// Test a path that's exactly at the limit
	// .beads/bd.sock adds 15 characters
	// So a workspace path of 88 chars + 15 = 103 (exactly at limit)
	workspace := strings.Repeat("x", 88)
	socketPath := ShortSocketPath(workspace)

	// Should use natural path since it's exactly at the limit
	expected := filepath.Join(workspace, ".beads", "bd.sock")
	if socketPath != expected {
		t.Errorf("Path at exact limit should use natural path.\nGot: %q\nWant: %q\nLen: %d", socketPath, expected, len(expected))
	}
}
