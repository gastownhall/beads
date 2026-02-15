package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// waitFor repeatedly evaluates pred until it returns true or timeout expires.
// Use this instead of time.Sleep for event-driven testing.
func waitFor(t *testing.T, timeout, poll time.Duration, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(poll)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// setupGitRepo creates a temporary git repository and returns its path and cleanup function.
// The repo is initialized with git config, a .beads directory, and an initial commit.
// The current directory is changed to the new repo.
func setupGitRepo(t *testing.T) (repoPath string, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}

	// Reset caches after changing directory
	git.ResetCaches()
	beads.ResetCaches()

	// Copy cached git template instead of running git init (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to copy git template: %v", err)
	}
	git.ResetCaches()
	beads.ResetCaches()

	// Create .beads directory with minimal issues.jsonl (required for RepoContext)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0600); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Create initial commit
	if err := os.WriteFile("test.txt", []byte("test"), 0600); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write test file: %v", err)
	}
	_ = exec.Command("git", "add", ".").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create initial commit: %v", err)
	}

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
		beads.ResetCaches()
	}

	return tmpDir, cleanup
}

// setupGitRepoWithBranch creates a git repo and checks out a specific branch.
// Use this when tests need a specific branch name (e.g., "main").
func setupGitRepoWithBranch(t *testing.T, branch string) (repoPath string, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}

	// Reset caches after changing directory
	git.ResetCaches()
	beads.ResetCaches()

	// Copy cached git template instead of running git init (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to copy git template: %v", err)
	}
	// Switch to requested branch if different from template default
	if branch != "main" {
		if err := exec.Command("git", "checkout", "-b", branch).Run(); err != nil {
			_ = os.Chdir(originalWd)
			t.Fatalf("failed to switch to branch %s: %v", branch, err)
		}
	}
	git.ResetCaches()
	beads.ResetCaches()

	// Create .beads directory with minimal issues.jsonl (required for RepoContext)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0600); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Create initial commit
	if err := os.WriteFile("test.txt", []byte("test"), 0600); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write test file: %v", err)
	}
	_ = exec.Command("git", "add", ".").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create initial commit: %v", err)
	}

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
		beads.ResetCaches()
	}

	return tmpDir, cleanup
}

// setupMinimalGitRepo creates a git repo without an initial commit.
// Use this when tests need to control the initial state more precisely.
func setupMinimalGitRepo(t *testing.T) (repoPath string, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}

	// Reset caches after changing directory
	git.ResetCaches()
	beads.ResetCaches()

	// Copy cached git template instead of running git init (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to copy git template: %v", err)
	}

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
		beads.ResetCaches()
	}

	return tmpDir, cleanup
}
