package sqlite

import (
	"os"
	"strings"
	"testing"
)

// TestIsWSL2WindowsPath tests WAL mode detection on WSL2 paths
func TestIsWSL2WindowsPath(t *testing.T) {
	// Check if we're actually running in WSL2 (the tests are most meaningful there)
	isActuallyWSL2 := isActualWSL2()

	tests := []struct {
		name     string
		path     string
		wantTrue bool // True if we expect isWSL2WindowsPath to return true
		desc     string
	}{
		{
			name:     "Windows C: drive via /mnt/c/",
			path:     "/mnt/c/Users/test/project/.beads/beads.db",
			wantTrue: true,
			desc:     "Windows filesystem mount (GH#920)",
		},
		{
			name:     "Windows D: drive via /mnt/d/",
			path:     "/mnt/d/work/repo/.beads/beads.db",
			wantTrue: true,
			desc:     "Windows filesystem mount (GH#920)",
		},
		{
			name:     "Docker Desktop bind mount via /mnt/wsl/",
			path:     "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/8751927bbe6399e9c8ce8ce00205a4c514767d2aed43570b4264ab4083ce0ef0/.beads/beads.db",
			wantTrue: true,
			desc:     "Docker Desktop bind mount (GH#1224)",
		},
		{
			name:     "WSL2 root filesystem /home/",
			path:     "/home/user/project/.beads/beads.db",
			wantTrue: false,
			desc:     "Native WSL2 ext4 filesystem (WAL works)",
		},
		{
			name:     "WSL2 /tmp/",
			path:     "/tmp/beads/.beads/beads.db",
			wantTrue: false,
			desc:     "Native WSL2 tmpfs (WAL works)",
		},
		{
			name:     "Non-WSL Linux path",
			path:     "/home/user/project/.beads/beads.db",
			wantTrue: false,
			desc:     "Regular Linux path (not WSL2)",
		},
		{
			name:     "Non-WSL /mnt/ path",
			path:     "/mnt/nfs/shared/.beads/beads.db",
			wantTrue: false,
			desc:     "Non-Windows /mnt path (different context)",
		},
		{
			name:     "Edge case: /mnt/wsl/ root",
			path:     "/mnt/wsl/",
			wantTrue: true,
			desc:     "Should match /mnt/wsl/ prefix",
		},
		{
			name:     "Edge case: /mnt/wsls/ (with 's' suffix)",
			path:     "/mnt/wsls/some/path",
			wantTrue: false,
			desc:     "Should NOT match /mnt/wsls (exact prefix match)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWSL2WindowsPath(tt.path)

			if !isActuallyWSL2 {
				// When not actually in WSL2, the function should return false
				// (because /proc/version check fails)
				if got != false {
					t.Errorf("isWSL2WindowsPath(%q) = %v, want false (not in WSL2 environment)\n%s", tt.path, got, tt.desc)
				}
				return
			}

			// When actually in WSL2, verify the path detection logic
			if got != tt.wantTrue {
				t.Errorf("isWSL2WindowsPath(%q) = %v, want %v\n%s", tt.path, got, tt.wantTrue, tt.desc)
			}
		})
	}
}

// isActualWSL2 checks if the test is actually running in WSL2
func isActualWSL2() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	version := strings.ToLower(string(data))
	return strings.Contains(version, "microsoft") || strings.Contains(version, "wsl")
}

// TestDockerBindMountDetection verifies that Docker Desktop bind mounts are properly detected
// This test is most meaningful when run on actual WSL2 with Docker Desktop installed
func TestDockerBindMountDetection(t *testing.T) {
	if !isActualWSL2() {
		t.Skip("Skipping: not running in WSL2")
	}

	// These paths represent actual Docker Desktop bind mount patterns
	dockerBindMountPaths := []string{
		"/mnt/wsl/docker-desktop-bind-mounts/",
		"/mnt/wsl/docker-desktop/",
		"/mnt/wsl/shared/",
	}

	for _, pathPrefix := range dockerBindMountPaths {
		fullPath := pathPrefix + "some/db/.beads/beads.db"
		if !isWSL2WindowsPath(fullPath) {
			t.Errorf("Docker bind mount path %q should be detected as problematic for WAL mode", fullPath)
		}
	}
}

// TestJournalModeSelection verifies that the correct journal mode is selected
func TestJournalModeSelection(t *testing.T) {
	if !isActualWSL2() {
		t.Skip("Skipping: not running in WSL2")
	}

	tests := []struct {
		path         string
		expectedMode string
	}{
		{
			path:         "/home/user/project/.beads/beads.db",
			expectedMode: "WAL",
		},
		{
			path:         "/mnt/c/Users/test/project/.beads/beads.db",
			expectedMode: "DELETE",
		},
		{
			path:         "/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/123/.beads/beads.db",
			expectedMode: "DELETE",
		},
	}

	for _, tt := range tests {
		shouldDisableWAL := isWSL2WindowsPath(tt.path)
		expectedMode := "WAL"
		if shouldDisableWAL {
			expectedMode = "DELETE"
		}

		if expectedMode != tt.expectedMode {
			t.Errorf("Path %q: expected mode %s, got logic would select %s", tt.path, tt.expectedMode, expectedMode)
		}
	}
}
