//go:build integration

package fix

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestGitRepoIntegration creates a temporary git repository with a .beads directory
func setupTestGitRepoIntegration(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	return dir
}

// runGitIntegration runs a git command and returns output
func runGitIntegration(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s", args, output)
	}
	return string(output)
}

// TestSyncBranchHealth_LocalAndRemoteDiverged tests fix when branches diverged
func TestSyncBranchHealth_LocalAndRemoteDiverged(t *testing.T) {
	// Setup bare remote repo
	remoteDir := t.TempDir()
	runGitIntegration(t, remoteDir, "init", "--bare")

	// Setup local repo
	dir := setupTestGitRepoIntegration(t)
	runGitIntegration(t, dir, "remote", "add", "origin", remoteDir)

	// Create main branch with initial commit
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	runGitIntegration(t, dir, "add", "test.txt")
	runGitIntegration(t, dir, "commit", "-m", "initial commit")
	runGitIntegration(t, dir, "branch", "-M", "main")
	runGitIntegration(t, dir, "push", "-u", "origin", "main")

	// Create sync branch
	runGitIntegration(t, dir, "checkout", "-b", "beads-sync")
	syncFile := filepath.Join(dir, "sync.txt")
	if err := os.WriteFile(syncFile, []byte("sync content"), 0600); err != nil {
		t.Fatalf("failed to create sync file: %v", err)
	}
	runGitIntegration(t, dir, "add", "sync.txt")
	runGitIntegration(t, dir, "commit", "-m", "sync commit")
	runGitIntegration(t, dir, "push", "-u", "origin", "beads-sync")

	// Simulate divergence: update main
	runGitIntegration(t, dir, "checkout", "main")
	if err := os.WriteFile(testFile, []byte("updated main content"), 0600); err != nil {
		t.Fatalf("failed to update test file: %v", err)
	}
	runGitIntegration(t, dir, "add", "test.txt")
	runGitIntegration(t, dir, "commit", "-m", "update main")
	runGitIntegration(t, dir, "push", "origin", "main")

	// Now beads-sync is behind main - fix it
	err := SyncBranchHealth(dir, "beads-sync")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify beads-sync was reset to main
	runGitIntegration(t, dir, "checkout", "beads-sync")
	runGitIntegration(t, dir, "pull", "origin", "beads-sync")

	// Check that beads-sync now has main's content
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}
	if string(content) != "updated main content" {
		t.Errorf("expected beads-sync to have main's content, got: %s", content)
	}

	// Check that sync.txt no longer exists (branch was reset)
	if _, err := os.Stat(syncFile); !os.IsNotExist(err) {
		t.Error("sync.txt should not exist after reset to main")
	}
}

// TestSyncBranchHealth_UncommittedChanges tests fix with uncommitted changes
func TestSyncBranchHealth_UncommittedChanges(t *testing.T) {
	// Setup bare remote repo
	remoteDir := t.TempDir()
	runGitIntegration(t, remoteDir, "init", "--bare")

	// Setup local repo
	dir := setupTestGitRepoIntegration(t)
	runGitIntegration(t, dir, "remote", "add", "origin", remoteDir)

	// Create main branch with initial commit
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	runGitIntegration(t, dir, "add", "test.txt")
	runGitIntegration(t, dir, "commit", "-m", "initial commit")
	runGitIntegration(t, dir, "branch", "-M", "main")
	runGitIntegration(t, dir, "push", "-u", "origin", "main")

	// Create sync branch and push it
	runGitIntegration(t, dir, "checkout", "-b", "beads-sync")
	runGitIntegration(t, dir, "push", "-u", "origin", "beads-sync")

	// Add uncommitted changes to sync branch
	dirtyFile := filepath.Join(dir, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted"), 0600); err != nil {
		t.Fatalf("failed to create dirty file: %v", err)
	}

	// Checkout main to allow sync branch reset
	runGitIntegration(t, dir, "checkout", "main")

	// Fix should succeed - it resets the branch, not the working tree
	err := SyncBranchHealth(dir, "beads-sync")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify sync branch was reset
	output := runGitIntegration(t, dir, "log", "--oneline", "beads-sync")
	if !strings.Contains(output, "initial commit") {
		t.Errorf("beads-sync should be reset to main, got log: %s", output)
	}
}

// TestSyncBranchHealth_RemoteUnreachable tests fix when remote is unreachable
func TestSyncBranchHealth_RemoteUnreachable(t *testing.T) {
	dir := setupTestGitRepoIntegration(t)

	// Add unreachable remote
	runGitIntegration(t, dir, "remote", "add", "origin", "https://nonexistent.example.com/repo.git")

	// Create main branch with initial commit
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	runGitIntegration(t, dir, "add", "test.txt")
	runGitIntegration(t, dir, "commit", "-m", "initial commit")
	runGitIntegration(t, dir, "branch", "-M", "main")

	// Create local sync branch
	runGitIntegration(t, dir, "checkout", "-b", "beads-sync")
	runGitIntegration(t, dir, "checkout", "main")

	// Fix should fail when trying to fetch
	err := SyncBranchHealth(dir, "beads-sync")
	if err == nil {
		t.Error("expected error when remote is unreachable")
	}
	if err != nil && !strings.Contains(err.Error(), "failed to fetch") {
		t.Errorf("expected fetch error, got: %v", err)
	}
}

// TestSyncBranchHealth_CurrentlyOnSyncBranch tests error when on sync branch
func TestSyncBranchHealth_CurrentlyOnSyncBranch(t *testing.T) {
	// Setup bare remote repo
	remoteDir := t.TempDir()
	runGitIntegration(t, remoteDir, "init", "--bare")

	// Setup local repo
	dir := setupTestGitRepoIntegration(t)
	runGitIntegration(t, dir, "remote", "add", "origin", remoteDir)

	// Create main branch with initial commit
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("main content"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	runGitIntegration(t, dir, "add", "test.txt")
	runGitIntegration(t, dir, "commit", "-m", "initial commit")
	runGitIntegration(t, dir, "branch", "-M", "main")
	runGitIntegration(t, dir, "push", "-u", "origin", "main")

	// Create and checkout sync branch
	runGitIntegration(t, dir, "checkout", "-b", "beads-sync")
	runGitIntegration(t, dir, "push", "-u", "origin", "beads-sync")

	// Try to fix while on sync branch
	err := SyncBranchHealth(dir, "beads-sync")
	if err == nil {
		t.Error("expected error when currently on sync branch")
	}
	if err != nil && !strings.Contains(err.Error(), "currently on beads-sync branch") {
		t.Errorf("expected 'currently on branch' error, got: %v", err)
	}
}
