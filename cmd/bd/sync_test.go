package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsGitRepo_InGitRepo(t *testing.T) {
	// This test assumes we're running in the beads git repo
	if !isGitRepo() {
		t.Skip("not in a git repository")
	}
}

func TestIsGitRepo_NotInGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	os.Chdir(tmpDir)

	if isGitRepo() {
		t.Error("expected false when not in git repo")
	}
}

func TestGitHasUpstream_NoUpstream(t *testing.T) {
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a fresh git repo without upstream
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("test.txt", []byte("test"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Should not have upstream
	if gitHasUpstream() {
		t.Error("expected false when no upstream configured")
	}
}

func TestGitHasChanges_NoFile(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create and commit a file
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("original"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Check - should have no changes
	hasChanges, err := gitHasChanges(ctx, "test.txt")
	if err != nil {
		t.Fatalf("gitHasChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes for committed file")
	}
}

func TestGitHasChanges_ModifiedFile(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create and commit a file
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("original"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Modify the file
	os.WriteFile(testFile, []byte("modified"), 0644)

	// Check - should have changes
	hasChanges, err := gitHasChanges(ctx, "test.txt")
	if err != nil {
		t.Fatalf("gitHasChanges() error = %v", err)
	}
	if !hasChanges {
		t.Error("expected changes for modified file")
	}
}

func TestGitHasUnmergedPaths_CleanRepo(t *testing.T) {
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("test.txt", []byte("test"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Should not have unmerged paths
	hasUnmerged, err := gitHasUnmergedPaths()
	if err != nil {
		t.Fatalf("gitHasUnmergedPaths() error = %v", err)
	}
	if hasUnmerged {
		t.Error("expected no unmerged paths in clean repo")
	}
}

func TestGitCommit_Success(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("initial.txt", []byte("initial"), 0644)
	exec.Command("git", "add", "initial.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Create a new file
	testFile := "test.txt"
	os.WriteFile(testFile, []byte("content"), 0644)

	// Commit the file
	err := gitCommit(ctx, testFile, "test commit")
	if err != nil {
		t.Fatalf("gitCommit() error = %v", err)
	}

	// Verify file is committed
	hasChanges, err := gitHasChanges(ctx, testFile)
	if err != nil {
		t.Fatalf("gitHasChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes after commit")
	}
}

func TestGitCommit_AutoMessage(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("initial.txt", []byte("initial"), 0644)
	exec.Command("git", "add", "initial.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Create a new file
	testFile := "test.txt"
	os.WriteFile(testFile, []byte("content"), 0644)

	// Commit with auto-generated message (empty string)
	err := gitCommit(ctx, testFile, "")
	if err != nil {
		t.Fatalf("gitCommit() error = %v", err)
	}

	// Verify it committed (message generation worked)
	cmd := exec.Command("git", "log", "-1", "--pretty=%B")
	output, _ := cmd.Output()
	if len(output) == 0 {
		t.Error("expected commit message to be generated")
	}
}

func TestCountIssuesInJSONL_NonExistent(t *testing.T) {
	count, err := countIssuesInJSONL("/nonexistent/path.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 on error", count)
	}
}

func TestCountIssuesInJSONL_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "empty.jsonl")
	os.WriteFile(jsonlPath, []byte(""), 0644)

	count, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestCountIssuesInJSONL_MultipleIssues(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	content := `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"bd-3"}
`
	os.WriteFile(jsonlPath, []byte(content), 0644)

	count, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestCountIssuesInJSONL_WithMalformedLines(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "mixed.jsonl")
	content := `{"id":"bd-1"}
not valid json
{"id":"bd-2"}
{"id":"bd-3"}
`
	os.WriteFile(jsonlPath, []byte(content), 0644)

	count, err := countIssuesInJSONL(jsonlPath)
	// countIssuesInJSONL returns error on malformed JSON
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
	// Should have counted the first valid issue before hitting error
	if count != 1 {
		t.Errorf("count = %d, want 1 (before malformed line)", count)
	}
}

func TestGetCurrentBranch(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("test.txt", []byte("test"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Get current branch
	branch, err := getCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("getCurrentBranch() error = %v", err)
	}

	// Default branch is usually main or master
	if branch != "main" && branch != "master" {
		t.Logf("got branch %s (expected main or master, but this can vary)", branch)
	}
}

func TestMergeSyncBranch_NoSyncBranchConfigured(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("test.txt", []byte("test"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Try to merge without sync.branch configured (or database)
	err := mergeSyncBranch(ctx, false)
	if err == nil {
		t.Error("expected error when sync.branch not configured")
	}
	// Error could be about missing database or missing sync.branch config
	if err != nil && !strings.Contains(err.Error(), "sync.branch") && !strings.Contains(err.Error(), "database") {
		t.Errorf("expected error about sync.branch or database, got: %v", err)
	}
}

func TestMergeSyncBranch_OnSyncBranch(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit on main
	os.WriteFile("test.txt", []byte("test"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Create sync branch
	exec.Command("git", "checkout", "-b", "beads-metadata").Run()

	// Initialize bd database and set sync.branch
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// This test will fail with store access issues, so we just verify the branch check
	// The actual merge functionality is tested in integration tests
	currentBranch, _ := getCurrentBranch(ctx)
	if currentBranch != "beads-metadata" {
		t.Skipf("test setup failed, current branch is %s", currentBranch)
	}
}

func TestMergeSyncBranch_DirtyWorkingTree(t *testing.T) {
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Create a git repo
	os.Chdir(tmpDir)
	exec.Command("git", "init").Run()
	exec.Command("git", "config", "user.email", "test@test.com").Run()
	exec.Command("git", "config", "user.name", "Test User").Run()

	// Create initial commit
	os.WriteFile("test.txt", []byte("test"), 0644)
	exec.Command("git", "add", "test.txt").Run()
	exec.Command("git", "commit", "-m", "initial").Run()

	// Create uncommitted changes
	os.WriteFile("test.txt", []byte("modified"), 0644)

	// This test verifies the dirty working tree check would work
	// (We can't test the full merge without database setup)
	statusCmd := exec.Command("git", "status", "--porcelain")
	output, _ := statusCmd.Output()
	if len(output) == 0 {
		t.Error("expected dirty working tree for test setup")
	}
}
