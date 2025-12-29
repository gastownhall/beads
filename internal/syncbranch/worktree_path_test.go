package syncbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetBeadsWorktreePath tests the worktree path calculation for various repo structures.
// This is the regression test for GH#639.
func TestGetBeadsWorktreePath(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	t.Run("regular repo returns .git/beads-worktrees path", func(t *testing.T) {
		// Create a regular git repository
		tmpDir := t.TempDir()
		runGitCmd(t, tmpDir, "init")
		runGitCmd(t, tmpDir, "config", "user.email", "test@test.com")
		runGitCmd(t, tmpDir, "config", "user.name", "Test User")

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}
		runGitCmd(t, tmpDir, "add", ".")
		runGitCmd(t, tmpDir, "commit", "-m", "initial")

		// Test getBeadsWorktreePath
		path := getBeadsWorktreePath(ctx, tmpDir, "beads-sync")

		// Should be under .git/beads-worktrees
		expectedSuffix := filepath.Join(".git", "beads-worktrees", "beads-sync")
		if !strings.HasSuffix(path, expectedSuffix) {
			t.Errorf("Expected path to end with %q, got %q", expectedSuffix, path)
		}

		// Path should be absolute
		if !filepath.IsAbs(path) {
			t.Errorf("Expected absolute path, got %q", path)
		}
	})

	t.Run("bare repo returns correct worktree path", func(t *testing.T) {
		// Create a bare repository
		tmpDir := t.TempDir()
		bareRepoPath := filepath.Join(tmpDir, "bare.git")
		runGitCmd(t, tmpDir, "init", "--bare", bareRepoPath)

		// Test getBeadsWorktreePath from bare repo
		path := getBeadsWorktreePath(ctx, bareRepoPath, "beads-sync")

		// For bare repos, git-common-dir returns the bare repo itself
		// So the path should be <bare-repo>/beads-worktrees/beads-sync
		expectedPath := filepath.Join(bareRepoPath, "beads-worktrees", "beads-sync")
		if path != expectedPath {
			t.Errorf("Expected path %q, got %q", expectedPath, path)
		}

		// Path should be absolute
		if !filepath.IsAbs(path) {
			t.Errorf("Expected absolute path, got %q", path)
		}

		// Verify it's NOT trying to create .git/beads-worktrees inside the bare repo
		// (which would fail since bare repos don't have a .git subdirectory)
		badPath := filepath.Join(bareRepoPath, ".git", "beads-worktrees", "beads-sync")
		if path == badPath {
			t.Errorf("Bare repo should not use .git subdirectory path: %q", path)
		}
	})

	t.Run("worktree of regular repo uses common git dir", func(t *testing.T) {
		// Create a regular repository
		tmpDir := t.TempDir()
		mainRepoPath := filepath.Join(tmpDir, "main-repo")
		if err := os.MkdirAll(mainRepoPath, 0750); err != nil {
			t.Fatalf("Failed to create main repo dir: %v", err)
		}

		runGitCmd(t, mainRepoPath, "init")
		runGitCmd(t, mainRepoPath, "config", "user.email", "test@test.com")
		runGitCmd(t, mainRepoPath, "config", "user.name", "Test User")

		// Create initial commit
		testFile := filepath.Join(mainRepoPath, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}
		runGitCmd(t, mainRepoPath, "add", ".")
		runGitCmd(t, mainRepoPath, "commit", "-m", "initial")

		// Create a worktree
		worktreePath := filepath.Join(tmpDir, "feature-worktree")
		runGitCmd(t, mainRepoPath, "worktree", "add", worktreePath, "-b", "feature")

		// Test getBeadsWorktreePath from the worktree
		path := getBeadsWorktreePath(ctx, worktreePath, "beads-sync")

		// Should point to the main repo's .git/beads-worktrees, not the worktree's
		mainGitDir := filepath.Join(mainRepoPath, ".git")
		expectedPath := filepath.Join(mainGitDir, "beads-worktrees", "beads-sync")

		// Resolve symlinks for comparison (on macOS, /var -> /private/var)
		resolvedExpected, err := filepath.EvalSymlinks(mainRepoPath)
		if err == nil {
			expectedPath = filepath.Join(resolvedExpected, ".git", "beads-worktrees", "beads-sync")
		}

		if path != expectedPath {
			t.Errorf("Expected path %q, got %q", expectedPath, path)
		}
	})

	t.Run("fallback works when git command fails", func(t *testing.T) {
		// Test with a non-git directory (should fallback to legacy behavior)
		tmpDir := t.TempDir()

		path := getBeadsWorktreePath(ctx, tmpDir, "beads-sync")

		// Should fallback to legacy .git/beads-worktrees path
		expectedPath := filepath.Join(tmpDir, ".git", "beads-worktrees", "beads-sync")
		if path != expectedPath {
			t.Errorf("Expected fallback path %q, got %q", expectedPath, path)
		}
	})
}

// TestGetBeadsWorktreePathRelativePath tests that relative paths from git are handled correctly
func TestGetBeadsWorktreePathRelativePath(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create a regular git repository
	tmpDir := t.TempDir()
	runGitCmd(t, tmpDir, "init")
	runGitCmd(t, tmpDir, "config", "user.email", "test@test.com")
	runGitCmd(t, tmpDir, "config", "user.name", "Test User")

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	runGitCmd(t, tmpDir, "add", ".")
	runGitCmd(t, tmpDir, "commit", "-m", "initial")

	// Test from a subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0750); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	// getBeadsWorktreePath should still return an absolute path
	path := getBeadsWorktreePath(ctx, subDir, "beads-sync")

	if !filepath.IsAbs(path) {
		t.Errorf("Expected absolute path, got %q", path)
	}
}

// runGitCmd is a helper to run git commands
func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

// TestNormalizeBeadsRelPath tests path normalization for bare repo worktrees.
// This is the regression test for GH#785.
func TestNormalizeBeadsRelPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "normal repo path unchanged",
			input:    ".beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "bare repo worktree strips leading component",
			input:    "main/.beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "bare repo worktree with deeper path",
			input:    "worktrees/feature-branch/.beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "metadata file also works",
			input:    "main/.beads/metadata.json",
			expected: ".beads/metadata.json",
		},
		{
			name:     "path with no .beads unchanged",
			input:    "some/other/path.txt",
			expected: "some/other/path.txt",
		},
		{
			name:     ".beads at start unchanged",
			input:    ".beads/subdir/file.jsonl",
			expected: ".beads/subdir/file.jsonl",
		},
		{
			name:     "similar prefix like .beads-backup not matched",
			input:    "foo/.beads-backup/.beads/issues.jsonl",
			expected: ".beads/issues.jsonl",
		},
		{
			name:     "only .beads-backup no real .beads unchanged",
			input:    "foo/.beads-backup/file.txt",
			expected: "foo/.beads-backup/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Normalize to forward slashes for comparison
			result := normalizeBeadsRelPath(tt.input)
			// Convert expected to platform path for comparison
			expectedPlatform := filepath.FromSlash(tt.expected)
			if result != expectedPlatform {
				t.Errorf("normalizeBeadsRelPath(%q) = %q, want %q", tt.input, result, expectedPlatform)
			}
		})
	}
}
