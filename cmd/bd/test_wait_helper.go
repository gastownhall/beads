package main

import (
	"os"
	"os/exec"
	"testing"
	"time"

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
// The repo is initialized with git config and an initial commit.
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

	// Reset git caches after changing directory
	git.ResetCaches()

	// Initialize git repo with 'main' as default branch (modern git convention)
	if err := exec.Command("git", "init", "--initial-branch=main").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to init git repo: %v", err)
	}
	git.ResetCaches()

	// Configure git
	_ = exec.Command("git", "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	if err := os.WriteFile("test.txt", []byte("test"), 0600); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write test file: %v", err)
	}
	_ = exec.Command("git", "add", "test.txt").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create initial commit: %v", err)
	}

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
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

	// Reset git caches after changing directory
	git.ResetCaches()

	// Initialize git repo with specific branch
	if err := exec.Command("git", "init", "-b", branch).Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to init git repo: %v", err)
	}
	git.ResetCaches()

	// Configure git
	_ = exec.Command("git", "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	if err := os.WriteFile("test.txt", []byte("test"), 0600); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to write test file: %v", err)
	}
	_ = exec.Command("git", "add", "test.txt").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to create initial commit: %v", err)
	}

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
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

	// Reset git caches after changing directory
	git.ResetCaches()

	// Initialize git repo with 'main' as default branch (modern git convention)
	if err := exec.Command("git", "init", "--initial-branch=main").Run(); err != nil {
		_ = os.Chdir(originalWd)
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git
	_ = exec.Command("git", "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "config", "user.name", "Test User").Run()

	cleanup = func() {
		_ = os.Chdir(originalWd)
		git.ResetCaches()
	}

	return tmpDir, cleanup
}
