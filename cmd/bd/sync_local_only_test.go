//go:build cgo && integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLocalOnlyMode tests that daemon works with local git repos (no remote)
func TestLocalOnlyMode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory for local-only repo
	tempDir := t.TempDir()

	// Initialize local git repo without remote
	runGitCmd(t, tempDir, "init")
	runGitCmd(t, tempDir, "config", "user.email", "test@example.com")
	runGitCmd(t, tempDir, "config", "user.name", "Test User")

	// Change to temp directory so git commands run in the test repo
	t.Chdir(tempDir)

	// Verify no remote exists
	cmd := exec.Command("git", "remote")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to check git remotes: %v", err)
	}
	if len(output) > 0 {
		t.Fatalf("Expected no remotes, got: %s", output)
	}

	ctx := context.Background()

	// Test hasGitRemote returns false
	if hasGitRemote(ctx) {
		t.Error("Expected hasGitRemote to return false for local-only repo")
	}

	// Test gitPull returns nil (no error)
	if err := gitPull(ctx, ""); err != nil {
		t.Errorf("gitPull should gracefully skip when no remote, got error: %v", err)
	}

	// Test gitPush returns nil (no error)
	if err := gitPush(ctx, ""); err != nil {
		t.Errorf("gitPush should gracefully skip when no remote, got error: %v", err)
	}

	// Create a dummy JSONL file to commit
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Test gitCommit works (local commits should work fine)
	runGitCmd(t, tempDir, "add", ".beads")
	if err := gitCommit(ctx, jsonlPath, "Test commit"); err != nil {
		t.Errorf("gitCommit should work in local-only mode, got error: %v", err)
	}

	// Verify commit was created
	cmd = exec.Command("git", "log", "--oneline")
	output, err = cmd.Output()
	if err != nil {
		t.Fatalf("Failed to check git log: %v", err)
	}
	if len(output) == 0 {
		t.Error("Expected at least one commit in git log")
	}
}

// TestWithRemote verifies hasGitRemote detects remotes correctly
func TestWithRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directories
	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote")
	cloneDir := filepath.Join(tempDir, "clone")

	// Create bare remote
	if err := os.MkdirAll(remoteDir, 0750); err != nil {
		t.Fatalf("Failed to create remote dir: %v", err)
	}
	runGitCmd(t, remoteDir, "init", "--bare")

	// Clone it
	runGitCmd(t, tempDir, "clone", remoteDir, cloneDir)

	// Change to clone directory
	t.Chdir(cloneDir)

	ctx := context.Background()

	// Test hasGitRemote returns true
	if !hasGitRemote(ctx) {
		t.Error("Expected hasGitRemote to return true when origin exists")
	}

	// Verify git pull doesn't error (even with empty remote)
	// Note: pull might fail with "couldn't find remote ref", but that's different
	// from the fatal "'origin' does not appear to be a git repository" error
	_ = gitPull(ctx, "") // Just verify it doesn't panic
}
