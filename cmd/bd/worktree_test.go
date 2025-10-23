package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsGitWorktree(t *testing.T) {
	// Save current directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Create a temp directory for our test repo
	tmpDir := t.TempDir()
	
	// Initialize a git repo
	mainRepo := filepath.Join(tmpDir, "main")
	if err := os.Mkdir(mainRepo, 0755); err != nil {
		t.Fatal(err)
	}
	
	// Initialize main git repo
	if err := os.Chdir(mainRepo); err != nil {
		t.Fatal(err)
	}
	
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skip("git not available")
	}
	
	if err := exec.Command("git", "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "config", "user.name", "Test User").Run(); err != nil {
		t.Fatal(err)
	}
	
	// Create a commit
	readmeFile := filepath.Join(mainRepo, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "add", "README.md").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "commit", "-m", "Initial commit").Run(); err != nil {
		t.Fatal(err)
	}
	
	// Test 1: Main repo should NOT be a worktree
	if isGitWorktree() {
		t.Error("Main repository should not be detected as a worktree")
	}
	
	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	if err := exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature").Run(); err != nil {
		t.Skip("git worktree not available")
	}
	
	// Change to worktree directory
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	
	// Test 2: Worktree should be detected
	if !isGitWorktree() {
		t.Error("Worktree should be detected as a worktree")
	}
	
	// Test 3: Verify git-dir != git-common-dir in worktree
	wtGitDir := gitRevParse("--git-dir")
	wtCommonDir := gitRevParse("--git-common-dir")
	if wtGitDir == "" || wtCommonDir == "" {
		t.Error("git rev-parse should return valid paths in worktree")
	}
	if wtGitDir == wtCommonDir {
		t.Errorf("In worktree, git-dir (%s) should differ from git-common-dir (%s)", wtGitDir, wtCommonDir)
	}
	
	// Clean up worktree
	if err := os.Chdir(mainRepo); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "worktree", "remove", worktreeDir).Run(); err != nil {
		t.Logf("Warning: failed to clean up worktree: %v", err)
	}
}

func TestTruncateForBox(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		maxLen int
		want   string
	}{
		{"short path", "/home/user", 20, "/home/user"},
		{"exact length", "/home/user/test", 15, "/home/user/test"},
		{"long path", "/very/long/path/to/database/file.db", 20, ".../database/file.db"},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForBox(tt.path, tt.maxLen)
			if len(got) > tt.maxLen {
				t.Errorf("truncateForBox() result too long: got %d chars, want <= %d", len(got), tt.maxLen)
			}
			if len(tt.path) <= tt.maxLen && got != tt.path {
				t.Errorf("truncateForBox() shouldn't truncate short paths: got %q, want %q", got, tt.path)
			}
		})
	}
}
