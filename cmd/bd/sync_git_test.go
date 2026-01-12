package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseGitStatusForBeadsChanges(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected bool
	}{
		// No changes
		{
			name:     "empty status",
			status:   "",
			expected: false,
		},
		{
			name:     "whitespace only",
			status:   "   \n",
			expected: false,
		},

		// Modified (should return true)
		{
			name:     "staged modified",
			status:   "M  .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "unstaged modified",
			status:   " M .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "staged and unstaged modified",
			status:   "MM .beads/issues.jsonl",
			expected: true,
		},

		// Added (should return true)
		{
			name:     "staged added",
			status:   "A  .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "added then modified",
			status:   "AM .beads/issues.jsonl",
			expected: true,
		},

		// Untracked (should return false)
		{
			name:     "untracked file",
			status:   "?? .beads/issues.jsonl",
			expected: false,
		},

		// Deleted (should return false)
		{
			name:     "staged deleted",
			status:   "D  .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "unstaged deleted",
			status:   " D .beads/issues.jsonl",
			expected: false,
		},

		// Edge cases
		{
			name:     "renamed file",
			status:   "R  old.jsonl -> .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "copied file",
			status:   "C  source.jsonl -> .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "status too short",
			status:   "M",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseGitStatusForBeadsChanges(tt.status)
			if result != tt.expected {
				t.Errorf("parseGitStatusForBeadsChanges(%q) = %v, want %v",
					tt.status, result, tt.expected)
			}
		})
	}
}

// TestGitBranchHasUpstream tests the gitBranchHasUpstream function
// which checks if a specific branch (not current HEAD) has upstream configured.
// This is critical for jj/jujutsu compatibility where HEAD is always detached
// but the sync-branch may have proper upstream tracking.
func TestGitBranchHasUpstream(t *testing.T) {
	// Create temp directory for test repos
	tmpDir, err := os.MkdirTemp("", "beads-upstream-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a bare "remote" repo
	remoteDir := filepath.Join(tmpDir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatalf("Failed to create bare repo: %v", err)
	}

	// Create local repo
	localDir := filepath.Join(tmpDir, "local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("Failed to create local dir: %v", err)
	}

	// Initialize and configure local repo
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "remote", "add", "origin", remoteDir},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Create initial commit on main
	testFile := filepath.Join(localDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	cmds = [][]string{
		{"git", "add", "test.txt"},
		{"git", "commit", "-m", "initial"},
		{"git", "push", "-u", "origin", "main"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Create beads-sync branch with upstream
	cmds = [][]string{
		{"git", "checkout", "-b", "beads-sync"},
		{"git", "push", "-u", "origin", "beads-sync"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to run %v: %v\n%s", args, err, out)
		}
	}

	// Save current dir and change to local repo
	origDir, _ := os.Getwd()
	if err := os.Chdir(localDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	// Test 1: beads-sync branch should have upstream
	t.Run("branch with upstream returns true", func(t *testing.T) {
		if !gitBranchHasUpstream("beads-sync") {
			t.Error("gitBranchHasUpstream('beads-sync') = false, want true")
		}
	})

	// Test 2: non-existent branch should return false
	t.Run("non-existent branch returns false", func(t *testing.T) {
		if gitBranchHasUpstream("no-such-branch") {
			t.Error("gitBranchHasUpstream('no-such-branch') = true, want false")
		}
	})

	// Test 3: Simulate jj detached HEAD - beads-sync should still work
	t.Run("works with detached HEAD (jj scenario)", func(t *testing.T) {
		// Detach HEAD (simulating jj's behavior)
		cmd := exec.Command("git", "checkout", "--detach", "HEAD")
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to detach HEAD: %v\n%s", err, out)
		}

		// gitHasUpstream() should fail (detached HEAD)
		if gitHasUpstream() {
			t.Error("gitHasUpstream() = true with detached HEAD, want false")
		}

		// But gitBranchHasUpstream("beads-sync") should still work
		if !gitBranchHasUpstream("beads-sync") {
			t.Error("gitBranchHasUpstream('beads-sync') = false with detached HEAD, want true")
		}
	})

	// Test 4: branch without upstream should return false
	t.Run("branch without upstream returns false", func(t *testing.T) {
		// Create a local-only branch (no upstream)
		cmd := exec.Command("git", "checkout", "-b", "local-only")
		cmd.Dir = localDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to create local branch: %v\n%s", err, out)
		}

		if gitBranchHasUpstream("local-only") {
			t.Error("gitBranchHasUpstream('local-only') = true, want false (no upstream)")
		}
	})
}
