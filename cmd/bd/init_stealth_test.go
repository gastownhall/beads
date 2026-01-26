package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupGitExclude_Worktree verifies that setupGitExclude writes to the main
// repo's .git/info/exclude, not the worktree's .git/worktrees/<name>/info/exclude.
// This is the fix for GH#1053.
func TestSetupGitExclude_Worktree(t *testing.T) {
	// Create main repo
	mainDir := t.TempDir()
	cmd := exec.Command("git", "init", "--initial-branch=main")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init main repo: %v", err)
	}

	// Configure git user
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = mainDir
		_ = cmd.Run()
	}

	// Create initial commit (required for worktree)
	dummyFile := filepath.Join(mainDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create worktree
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Change to worktree directory and run setupGitExclude
	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir to worktree: %v", err)
	}
	defer os.Chdir(origDir)

	if err := setupGitExclude(false); err != nil {
		t.Fatalf("setupGitExclude failed: %v", err)
	}

	// Verify: main repo's .git/info/exclude should have the patterns
	mainExcludePath := filepath.Join(mainDir, ".git", "info", "exclude")
	content, err := os.ReadFile(mainExcludePath)
	if err != nil {
		t.Fatalf("failed to read main exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/") {
		t.Errorf("main repo exclude missing .beads/ pattern: %s", content)
	}
	if !strings.Contains(string(content), ".claude/settings.local.json") {
		t.Errorf("main repo exclude missing .claude/settings.local.json pattern: %s", content)
	}

	// Verify: worktree's .git/worktrees/<name>/info/exclude should NOT exist
	// (or should not have the patterns if it exists)
	worktreeGitDir, err := exec.Command("git", "-C", worktreeDir, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatalf("failed to get worktree git dir: %v", err)
	}
	worktreeExcludePath := filepath.Join(strings.TrimSpace(string(worktreeGitDir)), "info", "exclude")
	if worktreeContent, err := os.ReadFile(worktreeExcludePath); err == nil {
		// If worktree exclude file exists, it should NOT have the beads patterns
		if strings.Contains(string(worktreeContent), ".beads/") {
			t.Errorf("worktree exclude should not have .beads/ pattern (it was written to wrong location)")
		}
	}
	// If the file doesn't exist, that's fine - we didn't create it
}

// TestSetupForkExclude_Worktree verifies that setupForkExclude writes to the main
// repo's .git/info/exclude, not the worktree's path. This is part of GH#1053.
func TestSetupForkExclude_Worktree(t *testing.T) {
	// Create main repo
	mainDir := t.TempDir()
	cmd := exec.Command("git", "init", "--initial-branch=main")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init main repo: %v", err)
	}

	// Configure git user
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = mainDir
		_ = cmd.Run()
	}

	// Create initial commit (required for worktree)
	dummyFile := filepath.Join(mainDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create worktree
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Change to worktree directory and run setupForkExclude
	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir to worktree: %v", err)
	}
	defer os.Chdir(origDir)

	if err := setupForkExclude(false); err != nil {
		t.Fatalf("setupForkExclude failed: %v", err)
	}

	// Verify: main repo's .git/info/exclude should have the patterns
	mainExcludePath := filepath.Join(mainDir, ".git", "info", "exclude")
	content, err := os.ReadFile(mainExcludePath)
	if err != nil {
		t.Fatalf("failed to read main exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/") {
		t.Errorf("main repo exclude missing .beads/ pattern: %s", content)
	}

	// Verify: worktree's .git/worktrees/<name>/info/exclude should NOT exist
	// (or should not have the patterns if it exists)
	worktreeGitDir, err := exec.Command("git", "-C", worktreeDir, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatalf("failed to get worktree git dir: %v", err)
	}
	worktreeExcludePath := filepath.Join(strings.TrimSpace(string(worktreeGitDir)), "info", "exclude")
	if worktreeContent, err := os.ReadFile(worktreeExcludePath); err == nil {
		// If worktree exclude file exists, it should NOT have the beads patterns
		if strings.Contains(string(worktreeContent), ".beads/") {
			t.Errorf("worktree exclude should not have .beads/ pattern (it was written to wrong location)")
		}
	}
}

// TestSetupGitExclude_RegularRepo verifies that setupGitExclude still works
// correctly in a regular (non-worktree) repo.
func TestSetupGitExclude_RegularRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--initial-branch=main")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	if err := setupGitExclude(false); err != nil {
		t.Fatalf("setupGitExclude failed: %v", err)
	}

	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/") {
		t.Errorf("exclude file missing .beads/ pattern: %s", content)
	}
	if !strings.Contains(string(content), ".claude/settings.local.json") {
		t.Errorf("exclude file missing .claude/settings.local.json pattern: %s", content)
	}
}
