package fix

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIsWithinWorkspace_PathTraversal tests path traversal attempts
func TestIsWithinWorkspace_PathTraversal(t *testing.T) {
	root := t.TempDir()

	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{
			name:      "simple dotdot traversal",
			candidate: filepath.Join(root, "..", "etc"),
			want:      false,
		},
		{
			name:      "dotdot in middle of path",
			candidate: filepath.Join(root, "subdir", "..", "..", "etc"),
			want:      false,
		},
		{
			name:      "multiple dotdot",
			candidate: filepath.Join(root, "..", "..", ".."),
			want:      false,
		},
		{
			name:      "dotdot stays within workspace",
			candidate: filepath.Join(root, "a", "b", "..", "c"),
			want:      true,
		},
		{
			name:      "relative path with dotdot",
			candidate: filepath.Join(root, "subdir", "..", "file"),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWithinWorkspace(root, tt.candidate)
			if got != tt.want {
				t.Errorf("isWithinWorkspace(%q, %q) = %v, want %v", root, tt.candidate, got, tt.want)
			}
		})
	}
}

// TestValidateBeadsWorkspace_EdgeCases tests edge cases for workspace validation
func TestValidateBeadsWorkspace_EdgeCases(t *testing.T) {
	t.Run("nested .beads directories", func(t *testing.T) {
		// Create a workspace with nested .beads directories
		dir := setupTestWorkspace(t)
		nestedDir := filepath.Join(dir, "subdir")
		nestedBeadsDir := filepath.Join(nestedDir, ".beads")
		if err := os.MkdirAll(nestedBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create nested .beads: %v", err)
		}

		// Root workspace should be valid
		if err := validateBeadsWorkspace(dir); err != nil {
			t.Errorf("expected root workspace to be valid, got: %v", err)
		}

		// Nested workspace should also be valid
		if err := validateBeadsWorkspace(nestedDir); err != nil {
			t.Errorf("expected nested workspace to be valid, got: %v", err)
		}
	})

	t.Run(".beads as a file not directory", func(t *testing.T) {
		dir := t.TempDir()
		beadsFile := filepath.Join(dir, ".beads")
		// Create .beads as a file instead of directory
		if err := os.WriteFile(beadsFile, []byte("not a directory"), 0600); err != nil {
			t.Fatalf("failed to create .beads file: %v", err)
		}

		err := validateBeadsWorkspace(dir)
		// NOTE: Current implementation only checks if .beads exists via os.Stat,
		// but doesn't verify it's a directory. This test documents current behavior.
		// A future improvement could add IsDir() check.
		if err == nil {
			// Currently passes - implementation doesn't validate it's a directory
			t.Log(".beads exists as file - validation passes (edge case)")
		}
	})

	t.Run(".beads as symlink to directory", func(t *testing.T) {
		dir := t.TempDir()
		// Create actual .beads directory elsewhere
		actualBeadsDir := filepath.Join(t.TempDir(), "actual_beads")
		if err := os.MkdirAll(actualBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create actual beads dir: %v", err)
		}

		// Create symlink .beads -> actual_beads
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink(actualBeadsDir, symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		// Should be valid - symlink to directory is acceptable
		if err := validateBeadsWorkspace(dir); err != nil {
			t.Errorf("expected symlinked .beads directory to be valid, got: %v", err)
		}
	})

	t.Run(".beads as symlink to file", func(t *testing.T) {
		dir := t.TempDir()
		// Create a file
		actualFile := filepath.Join(t.TempDir(), "actual_file")
		if err := os.WriteFile(actualFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create actual file: %v", err)
		}

		// Create symlink .beads -> file
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink(actualFile, symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		err := validateBeadsWorkspace(dir)
		// NOTE: os.Stat follows symlinks, so if symlink points to a file,
		// it just sees the file exists and returns no error.
		// Current implementation doesn't verify it's a directory.
		if err == nil {
			t.Log(".beads symlink to file - validation passes (edge case)")
		}
	})

	t.Run(".beads as broken symlink", func(t *testing.T) {
		dir := t.TempDir()
		// Create symlink to non-existent target
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink("/nonexistent/target", symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		err := validateBeadsWorkspace(dir)
		if err == nil {
			t.Error("expected error when .beads is a broken symlink")
		}
	})

	t.Run("relative path resolution", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		// Test with relative path
		originalWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get working directory: %v", err)
		}
		defer func() {
			if err := os.Chdir(originalWd); err != nil {
				t.Logf("failed to restore working directory: %v", err)
			}
		}()

		if err := os.Chdir(filepath.Dir(dir)); err != nil {
			t.Fatalf("failed to change directory: %v", err)
		}

		relPath := filepath.Base(dir)
		if err := validateBeadsWorkspace(relPath); err != nil {
			t.Errorf("expected relative path to be valid, got: %v", err)
		}
	})
}

// TestGitHooks_EdgeCases tests GitHooks with edge cases
func TestGitHooks_EdgeCases(t *testing.T) {
	// Skip if running as test binary (can't execute bd subcommands)
	skipIfTestBinary(t)

	t.Run("hooks directory does not exist", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Verify .git/hooks doesn't exist or remove it
		hooksDir := filepath.Join(dir, ".git", "hooks")
		_ = os.RemoveAll(hooksDir)

		// GitHooks should create the directory via bd hooks install
		err := GitHooks(dir)
		if err != nil {
			t.Errorf("GitHooks should succeed when hooks directory doesn't exist, got: %v", err)
		}

		// Verify hooks directory was created
		if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
			t.Error("expected hooks directory to be created")
		}
	})

	t.Run("git worktree with .git file", func(t *testing.T) {
		// Create main repo
		mainDir := setupTestGitRepo(t)

		// Create a commit so we can create a worktree
		testFile := filepath.Join(mainDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, mainDir, "add", "test.txt")
		runGit(t, mainDir, "commit", "-m", "initial")

		// Create a worktree
		worktreeDir := t.TempDir()
		runGit(t, mainDir, "worktree", "add", worktreeDir, "-b", "feature")

		// Create .beads in worktree
		beadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads in worktree: %v", err)
		}

		// GitHooks should work with worktrees where .git is a file
		err := GitHooks(worktreeDir)
		if err != nil {
			t.Errorf("GitHooks should work with git worktrees, got: %v", err)
		}
	})
}

// TestUntrackedJSONL_EdgeCases — removed: UntrackedJSONL function removed (bd-9ni.2)
func TestUntrackedJSONL_EdgeCases(t *testing.T) {
	t.Skip("UntrackedJSONL removed as part of JSONL removal (bd-9ni.2)")
}

// TestPermissions_EdgeCases tests Permissions with edge cases
func TestPermissions_EdgeCases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping Unix permission/symlink test on Windows")
	}
	t.Run("symbolic link to .beads directory", func(t *testing.T) {
		dir := t.TempDir()

		// Create actual .beads directory elsewhere
		actualBeadsDir := filepath.Join(t.TempDir(), "actual-beads")
		if err := os.MkdirAll(actualBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create actual .beads: %v", err)
		}

		// Create symlink to it
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink(actualBeadsDir, symlinkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}

		// Permissions should skip symlinked directories
		err := Permissions(dir)
		if err != nil {
			t.Errorf("expected no error for symlinked .beads, got: %v", err)
		}

		// Verify target directory permissions were not changed
		info, err := os.Stat(actualBeadsDir)
		if err != nil {
			t.Fatalf("failed to stat actual .beads: %v", err)
		}

		// Should still have 0755, not 0700
		if info.Mode().Perm() == 0700 {
			t.Error("symlinked directory permissions should not be changed to 0700")
		}
	})

	t.Run("symbolic link to database file", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		// Create actual database file elsewhere
		actualDbPath := filepath.Join(t.TempDir(), "actual-beads.db")
		if err := os.WriteFile(actualDbPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create actual db: %v", err)
		}

		// Create symlink to it
		dbSymlinkPath := filepath.Join(dir, ".beads", "beads.db")
		if err := os.Symlink(actualDbPath, dbSymlinkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}

		// Permissions should skip symlinked files
		err := Permissions(dir)
		if err != nil {
			t.Errorf("expected no error for symlinked db, got: %v", err)
		}

		// Verify target file permissions were not changed
		info, err := os.Stat(actualDbPath)
		if err != nil {
			t.Fatalf("failed to stat actual db: %v", err)
		}

		// Should still have 0644, not 0600
		if info.Mode().Perm() == 0600 {
			t.Error("symlinked database permissions should not be changed to 0600")
		}
	})

	t.Run("fixes incorrect .beads directory permissions", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		beadsDir := filepath.Join(dir, ".beads")

		// Set incorrect permissions (too permissive)
		if err := os.Chmod(beadsDir, 0755); err != nil {
			t.Fatalf("failed to set permissions: %v", err)
		}

		err := Permissions(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify permissions were fixed to 0700
		info, err := os.Stat(beadsDir)
		if err != nil {
			t.Fatalf("failed to stat .beads: %v", err)
		}

		if info.Mode().Perm() != 0700 {
			t.Errorf("expected permissions 0700, got %o", info.Mode().Perm())
		}
	})

	t.Run("fixes incorrect database file permissions", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		dbPath := filepath.Join(dir, ".beads", "beads.db")
		if err := os.WriteFile(dbPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create db: %v", err)
		}

		err := Permissions(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify permissions were fixed to 0600
		info, err := os.Stat(dbPath)
		if err != nil {
			t.Fatalf("failed to stat db: %v", err)
		}

		if info.Mode().Perm() != 0600 {
			t.Errorf("expected permissions 0600, got %o", info.Mode().Perm())
		}
	})

	t.Run("handles missing database file gracefully", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		// No database file exists
		err := Permissions(dir)
		if err != nil {
			t.Errorf("expected no error when database doesn't exist, got: %v", err)
		}
	})
}
