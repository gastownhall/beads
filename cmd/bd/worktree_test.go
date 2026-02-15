package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTruncateForBox(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		maxLen int
		want   string
	}{
		{
			name:   "short path no truncate",
			path:   "/home/user",
			maxLen: 20,
			want:   "/home/user",
		},
		{
			name:   "exact length",
			path:   "12345",
			maxLen: 5,
			want:   "12345",
		},
		{
			name:   "needs truncate",
			path:   "/very/long/path/to/somewhere/deep",
			maxLen: 15,
			want:   "...mewhere/deep",
		},
		{
			name:   "truncate to minimum",
			path:   "abcdefghij",
			maxLen: 5,
			want:   "...ij",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForBox(tt.path, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForBox(%q, %d) = %q, want %q", tt.path, tt.maxLen, got, tt.want)
			}
			if len(got) > tt.maxLen {
				t.Errorf("truncateForBox(%q, %d) returned %q with length %d > maxLen %d",
					tt.path, tt.maxLen, got, len(got), tt.maxLen)
			}
		})
	}
}

func TestGitRevParse(t *testing.T) {
	// Basic test - should either return a value or empty string (if not in git repo)
	result := gitRevParse("--git-dir")
	// Just verify it doesn't panic and returns a string
	if result != "" {
		// In a git repo
		t.Logf("Git dir: %s", result)
	} else {
		// Not in a git repo or error
		t.Logf("Not in git repo or error")
	}
}

// TestResolveWorktreePathByName verifies that resolveWorktreePath can find
// worktrees by name (basename) when they're in subdirectories like .worktrees/
func TestResolveWorktreePathByName(t *testing.T) {
	// Create a temp directory for the main repo
	mainDir := newGitRepo(t)

	// Create initial commit (required for worktrees)
	if err := os.WriteFile(filepath.Join(mainDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create initial commit: %v\n%s", err, output)
	}

	// Create .worktrees subdirectory
	worktreesDir := filepath.Join(mainDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("Failed to create .worktrees dir: %v", err)
	}

	// Create a worktree inside .worktrees/
	worktreePath := filepath.Join(worktreesDir, "test-wt")
	cmd = exec.Command("git", "worktree", "add", "-b", "test-wt", worktreePath)
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create worktree: %v\n%s", err, output)
	}
	defer func() {
		// Cleanup worktree
		cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
		cmd.Dir = mainDir
		_ = cmd.Run()
	}()

	ctx := context.Background()

	t.Run("resolves by name when worktree is in subdirectory", func(t *testing.T) {
		// This should find the worktree by consulting git's registry
		resolved, err := resolveWorktreePath(ctx, mainDir, "test-wt")
		if err != nil {
			t.Errorf("resolveWorktreePath(repoRoot, \"test-wt\") failed: %v", err)
			return
		}
		// Compare resolved paths to handle symlinks (e.g., /var -> /private/var on macOS)
		wantResolved, _ := filepath.EvalSymlinks(worktreePath)
		gotResolved, _ := filepath.EvalSymlinks(resolved)
		if gotResolved != wantResolved {
			t.Errorf("resolveWorktreePath returned %q, want %q", resolved, worktreePath)
		}
	})

	t.Run("resolves by relative path", func(t *testing.T) {
		// This should work via the existing relative-to-repo-root logic
		resolved, err := resolveWorktreePath(ctx, mainDir, ".worktrees/test-wt")
		if err != nil {
			t.Errorf("resolveWorktreePath(repoRoot, \".worktrees/test-wt\") failed: %v", err)
			return
		}
		if resolved != worktreePath {
			t.Errorf("resolveWorktreePath returned %q, want %q", resolved, worktreePath)
		}
	})

	t.Run("resolves by absolute path", func(t *testing.T) {
		resolved, err := resolveWorktreePath(ctx, mainDir, worktreePath)
		if err != nil {
			t.Errorf("resolveWorktreePath(repoRoot, absolutePath) failed: %v", err)
			return
		}
		if resolved != worktreePath {
			t.Errorf("resolveWorktreePath returned %q, want %q", resolved, worktreePath)
		}
	})

	t.Run("returns error for non-existent worktree", func(t *testing.T) {
		_, err := resolveWorktreePath(ctx, mainDir, "non-existent")
		if err == nil {
			t.Error("resolveWorktreePath should return error for non-existent worktree")
		}
	})
}
