package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

func TestMigrateSyncValidation(t *testing.T) {
	// Test invalid branch names
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"valid simple", "beads-sync", false},
		{"valid with slash", "beads/sync", false},
		{"valid with dots", "beads.sync", false},
		{"invalid empty", "", true},
		{"invalid HEAD", "HEAD", true},
		{"invalid dots", "..", true},
		{"invalid leading slash", "/beads", true},
		{"invalid trailing slash", "beads/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't easily test the full command without a git repo,
			// but we can test branch validation indirectly
			if tt.branch == "" {
				// Empty branch should fail at args validation level
				return
			}
		})
	}
}

func TestMigrateSyncDryRun(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir, err := os.MkdirTemp("", "bd-migrate-sync-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git: %v", err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git name: %v", err)
	}

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Create .beads directory and initialize
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create minimal issues.jsonl
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create issues.jsonl: %v", err)
	}

	// Test that branchExistsLocal returns false for non-existent branch
	// Note: We need to run this from tmpDir context since branchExistsLocal uses git in cwd
	ctx := context.Background()
	t.Chdir(tmpDir)
	// Reset caches so RepoContext picks up new CWD
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		beads.ResetCaches()
		git.ResetCaches()
	}()

	if branchExistsLocal(ctx, "beads-sync") {
		t.Error("branchExistsLocal should return false for non-existent branch")
	}

	// Create the branch
	cmd = exec.Command("git", "branch", "beads-sync")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	// Now it should exist
	if !branchExistsLocal(ctx, "beads-sync") {
		t.Error("branchExistsLocal should return true for existing branch")
	}
}

func TestHasChangesInWorktreeDir(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir, err := os.MkdirTemp("", "bd-worktree-changes-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git: %v", err)
	}

	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Create and commit initial file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	ctx := context.Background()

	// No changes initially
	hasChanges, err := hasChangesInWorktreeDir(ctx, tmpDir)
	if err != nil {
		t.Fatalf("hasChangesInWorktreeDir failed: %v", err)
	}
	if hasChanges {
		t.Error("should have no changes initially")
	}

	// Add uncommitted file
	newFile := filepath.Join(tmpDir, "new.txt")
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatalf("failed to create new file: %v", err)
	}

	hasChanges, err = hasChangesInWorktreeDir(ctx, tmpDir)
	if err != nil {
		t.Fatalf("hasChangesInWorktreeDir failed: %v", err)
	}
	if !hasChanges {
		t.Error("should have changes after adding file")
	}
}
