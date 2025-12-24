package compact

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

func TestGetCurrentCommitHash_InGitRepo(t *testing.T) {
	// This test runs in the actual beads repo, so it should return a valid hash
	hash := GetCurrentCommitHash()

	// Should be a 40-character hex string
	if len(hash) != 40 {
		t.Errorf("expected 40-char hash, got %d chars: %s", len(hash), hash)
	}

	// Should be valid hex
	matched, err := regexp.MatchString("^[0-9a-f]{40}$", hash)
	if err != nil {
		t.Fatalf("regex error: %v", err)
	}
	if !matched {
		t.Errorf("expected hex hash, got: %s", hash)
	}
}

func TestGetCurrentCommitHash_NotInGitRepo(t *testing.T) {
	// Save current directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	// Create a temporary directory that is NOT a git repo
	tmpDir := t.TempDir()

	// Change to the temp directory
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to temp dir: %v", err)
	}
	defer func() {
		// Restore original directory
		if err := os.Chdir(originalDir); err != nil {
			t.Fatalf("failed to restore cwd: %v", err)
		}
	}()

	// Should return empty string when not in a git repo
	hash := GetCurrentCommitHash()
	if hash != "" {
		t.Errorf("expected empty string outside git repo, got: %s", hash)
	}
}

func TestGetCurrentCommitHash_NewGitRepo(t *testing.T) {
	// Save current directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	// Create a temporary directory
	tmpDir := t.TempDir()

	// Initialize a new git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for the commit
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git email: %v", err)
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git name: %v", err)
	}

	// Create a file and commit it
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "test commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Change to the new git repo
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to git repo: %v", err)
	}
	defer func() {
		// Restore original directory
		if err := os.Chdir(originalDir); err != nil {
			t.Fatalf("failed to restore cwd: %v", err)
		}
	}()

	// Should return a valid hash
	hash := GetCurrentCommitHash()
	if len(hash) != 40 {
		t.Errorf("expected 40-char hash, got %d chars: %s", len(hash), hash)
	}

	// Verify it matches git rev-parse output
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = tmpDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to run git rev-parse: %v", err)
	}

	expected := string(out)
	expected = expected[:len(expected)-1] // trim newline
	if hash != expected {
		t.Errorf("hash mismatch: got %s, expected %s", hash, expected)
	}
}

func TestGetCurrentCommitHash_EmptyGitRepo(t *testing.T) {
	// Save current directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	// Create a temporary directory
	tmpDir := t.TempDir()

	// Initialize a new git repo but don't commit anything
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Change to the empty git repo
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to git repo: %v", err)
	}
	defer func() {
		// Restore original directory
		if err := os.Chdir(originalDir); err != nil {
			t.Fatalf("failed to restore cwd: %v", err)
		}
	}()

	// Should return empty string for repo with no commits
	hash := GetCurrentCommitHash()
	if hash != "" {
		t.Errorf("expected empty string for empty git repo, got: %s", hash)
	}
}
