package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindGitRoot verifies that findGitRoot correctly resolves the git
// repository root from a path inside a repo. This is critical for the
// backup redirect fix — when .beads/redirect points to another project,
// findGitRoot must resolve the correct repo from the backup directory.
func TestFindGitRoot(t *testing.T) {
	// Create a temp git repo
	repoDir := t.TempDir()
	if err := runCommandInDir(repoDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	// Configure git user for commits
	_ = runCommandInDir(repoDir, "git", "config", "user.email", "test@test.com")
	_ = runCommandInDir(repoDir, "git", "config", "user.name", "test")

	// Create a subdirectory
	subDir := filepath.Join(repoDir, "sub", "deep")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// findGitRoot from subdirectory should return repo root
	root, err := findGitRoot(subDir)
	if err != nil {
		t.Fatalf("findGitRoot failed: %v", err)
	}

	// Resolve symlinks for comparison (macOS /private/var vs /var)
	expectedRoot, _ := filepath.EvalSymlinks(repoDir)
	actualRoot, _ := filepath.EvalSymlinks(root)

	if actualRoot != expectedRoot {
		t.Errorf("findGitRoot(%q) = %q, want %q", subDir, root, repoDir)
	}
}

// TestFindGitRoot_NotARepo verifies findGitRoot returns an error when
// the path is not inside a git repository.
func TestFindGitRoot_NotARepo(t *testing.T) {
	tmpDir := t.TempDir() // not a git repo

	_, err := findGitRoot(tmpDir)
	if err == nil {
		t.Fatal("expected error for non-git directory, got nil")
	}
}

// TestFindGitRoot_File verifies findGitRoot works when given a file path
// (not a directory) — it should use the parent directory.
func TestFindGitRoot_File(t *testing.T) {
	repoDir := t.TempDir()
	if err := runCommandInDir(repoDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// Create a file
	filePath := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	root, err := findGitRoot(filePath)
	if err != nil {
		t.Fatalf("findGitRoot failed for file path: %v", err)
	}

	expectedRoot, _ := filepath.EvalSymlinks(repoDir)
	actualRoot, _ := filepath.EvalSymlinks(root)

	if actualRoot != expectedRoot {
		t.Errorf("findGitRoot(%q) = %q, want %q", filePath, root, repoDir)
	}
}
