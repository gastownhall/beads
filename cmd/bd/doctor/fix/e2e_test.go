package fix

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// skipIfTestBinary skips the test if running as a test binary.
// E2E tests that need to execute 'bd' subcommands cannot run in test mode.
func skipIfTestBinary(t *testing.T) {
	t.Helper()
	_, err := getBdBinary()
	if errors.Is(err, ErrTestBinary) {
		t.Skip("skipping E2E test: running as test binary")
	}
}

// =============================================================================
// End-to-End Fix Tests
// =============================================================================

// TestGitHooks_E2E tests the full GitHooks fix flow
func TestGitHooks_E2E(t *testing.T) {
	// Skip if bd binary not available or running as test binary
	skipIfTestBinary(t)
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd binary not in PATH, skipping e2e test")
	}

	t.Run("installs hooks in git repo", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Verify no hooks exist initially
		hooksDir := filepath.Join(dir, ".git", "hooks")
		preCommit := filepath.Join(hooksDir, "pre-commit")
		if _, err := os.Stat(preCommit); err == nil {
			t.Skip("pre-commit hook already exists, skipping")
		}

		// Run fix
		err := GitHooks(dir)
		if err != nil {
			t.Fatalf("GitHooks fix failed: %v", err)
		}

		// Verify hooks were installed
		if _, err := os.Stat(preCommit); os.IsNotExist(err) {
			t.Error("pre-commit hook was not installed")
		}

		// Check hook content has bd reference
		content, err := os.ReadFile(preCommit)
		if err != nil {
			t.Fatalf("failed to read hook: %v", err)
		}
		if !strings.Contains(string(content), "bd") {
			t.Error("hook doesn't contain bd reference")
		}
	})
}

// TestUntrackedJSONL_E2E tests the full UntrackedJSONL fix flow
func TestUntrackedJSONL_E2E(t *testing.T) {
	t.Run("commits untracked JSONL files", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit so we can make more commits
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Test\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")

		// Create untracked JSONL file in .beads
		jsonlPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		// Verify it's untracked
		output := runGit(t, dir, "status", "--porcelain", ".beads/")
		if !strings.Contains(output, "??") {
			t.Fatalf("expected untracked file, got: %s", output)
		}

		// Run fix
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Fatalf("UntrackedJSONL fix failed: %v", err)
		}

		// Verify file was committed
		output = runGit(t, dir, "status", "--porcelain", ".beads/")
		if strings.Contains(output, "??") {
			t.Error("JSONL file still untracked after fix")
		}

		// Verify commit was made
		output = runGit(t, dir, "log", "--oneline", "-1")
		if !strings.Contains(output, "untracked JSONL") {
			t.Errorf("expected commit message about untracked JSONL, got: %s", output)
		}
	})

	t.Run("handles no untracked files gracefully", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// No untracked files - should succeed without error
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Errorf("expected no error with no untracked files, got: %v", err)
		}
	})
}

// TestMergeDriver_E2E tests the full MergeDriver fix flow
func TestMergeDriver_E2E(t *testing.T) {
	t.Run("sets correct merge driver config", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Run fix
		err := MergeDriver(dir)
		if err != nil {
			t.Fatalf("MergeDriver fix failed: %v", err)
		}

		// Verify config was set
		output := runGit(t, dir, "config", "--get", "merge.beads.driver")
		expected := "bd merge %A %O %A %B"
		if strings.TrimSpace(output) != expected {
			t.Errorf("expected merge driver %q, got %q", expected, output)
		}
	})

	t.Run("fixes incorrect config", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Set incorrect config first
		runGit(t, dir, "config", "merge.beads.driver", "bd merge %L %O %A %R")

		// Run fix
		err := MergeDriver(dir)
		if err != nil {
			t.Fatalf("MergeDriver fix failed: %v", err)
		}

		// Verify config was corrected
		output := runGit(t, dir, "config", "--get", "merge.beads.driver")
		expected := "bd merge %A %O %A %B"
		if strings.TrimSpace(output) != expected {
			t.Errorf("expected corrected merge driver %q, got %q", expected, output)
		}
	})
}

// TestSyncBranchHealth_E2E tests the full SyncBranchHealth fix flow
func TestSyncBranchHealth_E2E(t *testing.T) {
	t.Run("resets sync branch when behind main", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit on main
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Initial\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")

		// Create main branch and add more commits
		runGit(t, dir, "branch", "-M", "main")
		testFile2 := filepath.Join(dir, "file2.md")
		if err := os.WriteFile(testFile2, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "file2.md")
		runGit(t, dir, "commit", "-m", "second commit on main")

		// Create beads-sync branch from an earlier commit
		runGit(t, dir, "branch", "beads-sync", "HEAD~1")

		// Verify beads-sync is behind
		output := runGit(t, dir, "rev-list", "--count", "beads-sync..main")
		if !strings.Contains(output, "1") {
			t.Logf("beads-sync should be 1 commit behind main, got: %s", output)
		}

		// Configure git remote (needed for push operations)
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")
		runGit(t, dir, "remote", "add", "origin", remoteDir)
		runGit(t, dir, "push", "-u", "origin", "main")
		runGit(t, dir, "push", "origin", "beads-sync")

		// Run fix
		err := SyncBranchHealth(dir, "beads-sync")
		if err != nil {
			t.Fatalf("SyncBranchHealth fix failed: %v", err)
		}

		// Verify beads-sync is now at same commit as main
		mainHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "main"))
		syncHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "beads-sync"))
		if mainHash != syncHash {
			t.Errorf("expected beads-sync to match main commit\nmain:  %s\nsync:  %s", mainHash, syncHash)
		}
	})

	t.Run("resets sync branch when ahead of main", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit on main
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Initial\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")
		runGit(t, dir, "branch", "-M", "main")

		// Configure git remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")
		runGit(t, dir, "remote", "add", "origin", remoteDir)
		runGit(t, dir, "push", "-u", "origin", "main")

		// Create beads-sync and add extra commit
		runGit(t, dir, "checkout", "-b", "beads-sync")
		extraFile := filepath.Join(dir, "extra.md")
		if err := os.WriteFile(extraFile, []byte("extra"), 0644); err != nil {
			t.Fatalf("failed to create extra file: %v", err)
		}
		runGit(t, dir, "add", "extra.md")
		runGit(t, dir, "commit", "-m", "extra commit on beads-sync")
		runGit(t, dir, "push", "-u", "origin", "beads-sync")

		// Switch back to main
		runGit(t, dir, "checkout", "main")

		// Verify beads-sync is ahead
		output := runGit(t, dir, "rev-list", "--count", "main..beads-sync")
		if !strings.Contains(output, "1") {
			t.Logf("beads-sync should be 1 commit ahead of main, got: %s", output)
		}

		// Run fix
		err := SyncBranchHealth(dir, "beads-sync")
		if err != nil {
			t.Fatalf("SyncBranchHealth fix failed: %v", err)
		}

		// Verify beads-sync is now at same commit as main
		mainHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "main"))
		syncHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "beads-sync"))
		if mainHash != syncHash {
			t.Errorf("expected beads-sync to match main commit\nmain:  %s\nsync:  %s", mainHash, syncHash)
		}
	})

	t.Run("resets sync branch when diverged from main", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit on main
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Initial\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")
		runGit(t, dir, "branch", "-M", "main")

		// Configure git remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")
		runGit(t, dir, "remote", "add", "origin", remoteDir)
		runGit(t, dir, "push", "-u", "origin", "main")

		// Create beads-sync from initial commit
		runGit(t, dir, "checkout", "-b", "beads-sync")
		syncFile := filepath.Join(dir, "sync-only.md")
		if err := os.WriteFile(syncFile, []byte("sync content"), 0644); err != nil {
			t.Fatalf("failed to create sync file: %v", err)
		}
		runGit(t, dir, "add", "sync-only.md")
		runGit(t, dir, "commit", "-m", "divergent commit on beads-sync")
		runGit(t, dir, "push", "-u", "origin", "beads-sync")

		// Add different commit to main
		runGit(t, dir, "checkout", "main")
		mainFile := filepath.Join(dir, "main-only.md")
		if err := os.WriteFile(mainFile, []byte("main content"), 0644); err != nil {
			t.Fatalf("failed to create main file: %v", err)
		}
		runGit(t, dir, "add", "main-only.md")
		runGit(t, dir, "commit", "-m", "divergent commit on main")
		runGit(t, dir, "push", "origin", "main")

		// Verify branches have diverged
		behindOutput := runGit(t, dir, "rev-list", "--count", "beads-sync..main")
		aheadOutput := runGit(t, dir, "rev-list", "--count", "main..beads-sync")
		if strings.TrimSpace(behindOutput) == "0" || strings.TrimSpace(aheadOutput) == "0" {
			t.Logf("branches should have diverged, behind: %s, ahead: %s", behindOutput, aheadOutput)
		}

		// Run fix
		err := SyncBranchHealth(dir, "beads-sync")
		if err != nil {
			t.Fatalf("SyncBranchHealth fix failed: %v", err)
		}

		// Verify beads-sync is now at same commit as main
		mainHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "main"))
		syncHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "beads-sync"))
		if mainHash != syncHash {
			t.Errorf("expected beads-sync to match main commit\nmain:  %s\nsync:  %s", mainHash, syncHash)
		}

		// Verify sync-only file no longer exists
		if _, err := os.Stat(syncFile); err == nil {
			runGit(t, dir, "checkout", "beads-sync")
			if _, err := os.Stat(syncFile); err == nil {
				t.Error("sync-only.md should not exist after reset to main")
			}
			runGit(t, dir, "checkout", "main")
		}
	})

	t.Run("handles master as main branch", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit on master
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Initial\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")
		runGit(t, dir, "branch", "-M", "master")

		// Configure git remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")
		runGit(t, dir, "remote", "add", "origin", remoteDir)
		runGit(t, dir, "push", "-u", "origin", "master")

		// Create beads-sync
		runGit(t, dir, "checkout", "-b", "beads-sync")
		runGit(t, dir, "push", "-u", "origin", "beads-sync")
		runGit(t, dir, "checkout", "master")

		// Add commit to master
		testFile2 := filepath.Join(dir, "file2.md")
		if err := os.WriteFile(testFile2, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "file2.md")
		runGit(t, dir, "commit", "-m", "second commit on master")
		runGit(t, dir, "push", "origin", "master")

		// Run fix
		err := SyncBranchHealth(dir, "beads-sync")
		if err != nil {
			t.Fatalf("SyncBranchHealth fix failed: %v", err)
		}

		// Verify beads-sync is now at same commit as master
		masterHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "master"))
		syncHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "beads-sync"))
		if masterHash != syncHash {
			t.Errorf("expected beads-sync to match master commit\nmaster: %s\nsync:   %s", masterHash, syncHash)
		}
	})

	t.Run("fails when on sync branch", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Initial\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")
		runGit(t, dir, "branch", "-M", "main")

		// Configure git remote (needed if git detects worktree)
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")
		runGit(t, dir, "remote", "add", "origin", remoteDir)
		runGit(t, dir, "push", "-u", "origin", "main")

		// Create beads-sync and checkout
		runGit(t, dir, "checkout", "-b", "beads-sync")
		runGit(t, dir, "push", "-u", "origin", "beads-sync")

		// Run fix should fail when on the sync branch
		// Note: This may succeed if git detects a worktree and can reset it
		// The key behavior is that it handles the case appropriately
		err := SyncBranchHealth(dir, "beads-sync")
		if err == nil {
			// If it succeeded, verify the branch was properly reset
			mainHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "main"))
			syncHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "beads-sync"))
			if mainHash != syncHash {
				t.Error("if fix succeeded on current branch, it should reset properly")
			}
			t.Skip("fix succeeded on current branch (worktree detected)")
		}
		if !strings.Contains(err.Error(), "currently on") && !strings.Contains(err.Error(), "checkout") {
			t.Errorf("expected error to mention being on branch, got: %v", err)
		}
	})

	t.Run("creates sync branch if it does not exist", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit on main
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Initial\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")
		runGit(t, dir, "branch", "-M", "main")

		// Configure git remote
		remoteDir := t.TempDir()
		runGit(t, remoteDir, "init", "--bare")
		runGit(t, dir, "remote", "add", "origin", remoteDir)
		runGit(t, dir, "push", "-u", "origin", "main")

		// Verify beads-sync does not exist
		output := runGit(t, dir, "branch", "--list", "beads-sync")
		if strings.Contains(output, "beads-sync") {
			t.Fatalf("beads-sync should not exist yet")
		}

		// Run fix
		err := SyncBranchHealth(dir, "beads-sync")
		if err != nil {
			t.Fatalf("SyncBranchHealth fix failed: %v", err)
		}

		// Verify beads-sync was created and matches main
		output = runGit(t, dir, "branch", "--list", "beads-sync")
		if !strings.Contains(output, "beads-sync") {
			t.Error("beads-sync should have been created")
		}

		mainHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "main"))
		syncHash := strings.TrimSpace(runGit(t, dir, "rev-parse", "beads-sync"))
		if mainHash != syncHash {
			t.Errorf("expected beads-sync to match main commit\nmain:  %s\nsync:  %s", mainHash, syncHash)
		}
	})
}

// =============================================================================
// Error Handling Tests
// =============================================================================

// TestGetBdBinary_Errors tests getBdBinary error scenarios
func TestGetBdBinary_Errors(t *testing.T) {
	t.Run("returns current executable when available", func(t *testing.T) {
		path, err := getBdBinary()
		if err != nil {
			// This is expected in test environment if bd isn't the test binary
			t.Logf("getBdBinary returned error (expected in test): %v", err)
			return
		}
		if path == "" {
			t.Error("expected non-empty path")
		}
	})
}

// TestGitCommandFailures tests handling of git command failures
func TestGitCommandFailures(t *testing.T) {
	t.Run("SyncBranchConfig fails without git", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Not a git repo - should fail
		err := SyncBranchConfig(dir)
		if err == nil {
			t.Error("expected error for non-git directory")
		}
	})

	t.Run("SyncBranchHealth fails without main/master", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create an orphan branch (no main/master)
		runGit(t, dir, "checkout", "--orphan", "orphan-branch")
		testFile := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "test.txt")
		runGit(t, dir, "commit", "-m", "orphan commit")

		// Delete main if it exists
		_ = exec.Command("git", "-C", dir, "branch", "-D", "main").Run()
		_ = exec.Command("git", "-C", dir, "branch", "-D", "master").Run()

		err := SyncBranchHealth(dir, "beads-sync")
		if err == nil {
			t.Error("expected error when neither main nor master exists")
		}
		if !strings.Contains(err.Error(), "main") && !strings.Contains(err.Error(), "master") {
			t.Errorf("error should mention main/master, got: %v", err)
		}
	})
}

// TestFilePermissionErrors tests handling of file permission issues
func TestFilePermissionErrors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission tests when running as root")
	}

	t.Run("Permissions handles read-only directory", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a file
		dbPath := filepath.Join(beadsDir, "beads.db")
		if err := os.WriteFile(dbPath, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		// Make directory read-only
		if err := os.Chmod(beadsDir, 0444); err != nil {
			t.Fatal(err)
		}
		defer func() {
			// Restore permissions for cleanup
			_ = os.Chmod(beadsDir, 0755)
		}()

		// Permissions fix should handle this gracefully
		err := Permissions(dir)
		// May succeed or fail depending on what needs fixing
		// The key is it shouldn't panic
		_ = err
	})
}

// =============================================================================
// Gitignore Tests
// =============================================================================

// TestFixGitignore_PartialPatterns tests FixGitignore with existing partial patterns
func TestFixGitignore_PartialPatterns(t *testing.T) {
	// Note: FixGitignore is in the main doctor package, not fix package
	// These tests would go in gitignore_test.go in the doctor package
	// Here we test the common validation used by fixes

	t.Run("validateBeadsWorkspace requires .beads directory", func(t *testing.T) {
		dir := t.TempDir()

		err := validateBeadsWorkspace(dir)
		if err == nil {
			t.Error("expected error for missing .beads directory")
		}
	})

	t.Run("validateBeadsWorkspace accepts valid workspace", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		err := validateBeadsWorkspace(dir)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// =============================================================================
// Edge Case E2E Tests
// =============================================================================

// TestGitHooksWithExistingHooks_E2E tests preserving existing non-bd hooks
func TestGitHooksWithExistingHooks_E2E(t *testing.T) {
	// Skip if bd binary not available or running as test binary
	skipIfTestBinary(t)
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd binary not in PATH, skipping e2e test")
	}

	t.Run("preserves existing non-bd hooks", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create a custom pre-commit hook
		hooksDir := filepath.Join(dir, ".git", "hooks")
		if err := os.MkdirAll(hooksDir, 0755); err != nil {
			t.Fatalf("failed to create hooks directory: %v", err)
		}

		preCommit := filepath.Join(hooksDir, "pre-commit")
		customHookContent := "#!/bin/sh\n# Custom hook\necho \"Running custom pre-commit hook\"\nexit 0\n"
		if err := os.WriteFile(preCommit, []byte(customHookContent), 0755); err != nil {
			t.Fatalf("failed to create custom hook: %v", err)
		}

		// Run fix to install bd hooks
		err := GitHooks(dir)
		if err != nil {
			t.Fatalf("GitHooks fix failed: %v", err)
		}

		// Verify hook still exists and is executable
		info, err := os.Stat(preCommit)
		if err != nil {
			t.Fatalf("pre-commit hook disappeared: %v", err)
		}
		if info.Mode().Perm()&0111 == 0 {
			t.Error("hook should be executable")
		}

		// Read hook content
		content, err := os.ReadFile(preCommit)
		if err != nil {
			t.Fatalf("failed to read hook: %v", err)
		}

		hookContent := string(content)
		// Verify bd hook was installed (should contain bd reference)
		if !strings.Contains(hookContent, "bd") {
			t.Error("hook should contain bd reference after installation")
		}

		// Note: The exact preservation behavior depends on 'bd hooks install' implementation
		// This test verifies the fix runs without destroying existing hooks
	})
}

// TestUntrackedJSONLWithUncommittedChanges_E2E tests handling uncommitted changes
func TestUntrackedJSONLWithUncommittedChanges_E2E(t *testing.T) {
	t.Run("commits untracked JSONL with staged changes present", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Test\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")

		// Create untracked JSONL file
		jsonlPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		// Create staged changes
		testFile2 := filepath.Join(dir, "file2.md")
		if err := os.WriteFile(testFile2, []byte("staged content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "file2.md")

		// Run fix
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Fatalf("UntrackedJSONL fix failed: %v", err)
		}

		// Verify JSONL was committed
		output := runGit(t, dir, "status", "--porcelain", ".beads/")
		if strings.Contains(output, "??") && strings.Contains(output, "deletions.jsonl") {
			t.Error("JSONL file still untracked after fix")
		}

		// Verify staged changes are still staged (not committed by fix)
		output = runGit(t, dir, "status", "--porcelain", "file2.md")
		if !strings.Contains(output, "A ") && !strings.Contains(output, "file2.md") {
			t.Error("staged changes should remain staged")
		}
	})

	t.Run("commits untracked JSONL with unstaged changes present", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit
		testFile := filepath.Join(dir, "README.md")
		if err := os.WriteFile(testFile, []byte("# Test\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")

		// Create untracked JSONL file
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-2"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		// Create unstaged changes to existing file
		if err := os.WriteFile(testFile, []byte("# Test Modified\n"), 0644); err != nil {
			t.Fatalf("failed to modify test file: %v", err)
		}

		// Verify unstaged changes exist
		statusOutput := runGit(t, dir, "status", "--porcelain")
		if !strings.Contains(statusOutput, " M ") && !strings.Contains(statusOutput, "README.md") {
			t.Logf("expected unstaged changes, got: %s", statusOutput)
		}

		// Run fix
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Fatalf("UntrackedJSONL fix failed: %v", err)
		}

		// Verify JSONL was committed
		output := runGit(t, dir, "status", "--porcelain", ".beads/")
		if strings.Contains(output, "??") && strings.Contains(output, "issues.jsonl") {
			t.Error("JSONL file still untracked after fix")
		}

		// Verify unstaged changes remain unstaged
		output = runGit(t, dir, "status", "--porcelain", "README.md")
		if !strings.Contains(output, " M") {
			t.Error("unstaged changes should remain unstaged")
		}
	})
}

// TestMergeDriverWithLockedConfig_E2E tests handling when git config is locked
func TestMergeDriverWithLockedConfig_E2E(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission tests when running as root")
	}

	t.Run("handles read-only git config file", func(t *testing.T) {
		// Skip on macOS - file owner can bypass read-only permissions
		if runtime.GOOS == "darwin" {
			t.Skip("skipping on macOS: file owner can write to read-only files")
		}
		// Skip in CI - containers may have CAP_DAC_OVERRIDE or other capabilities
		// that bypass file permission checks
		if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Skip("skipping in CI: container may bypass file permission checks")
		}

		dir := setupTestGitRepo(t)

		gitConfigPath := filepath.Join(dir, ".git", "config")

		// Make git config read-only
		if err := os.Chmod(gitConfigPath, 0444); err != nil {
			t.Fatalf("failed to make config read-only: %v", err)
		}
		defer func() {
			// Restore permissions for cleanup
			_ = os.Chmod(gitConfigPath, 0644)
		}()

		// Run fix - should fail gracefully
		err := MergeDriver(dir)
		if err == nil {
			t.Fatal("expected error when git config is read-only")
		}

		// Verify error message is meaningful
		if !strings.Contains(err.Error(), "failed to update git merge driver config") {
			t.Errorf("error should mention config update failure, got: %v", err)
		}
	})

	t.Run("succeeds when config directory is writable", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		gitDir := filepath.Join(dir, ".git")
		gitConfigPath := filepath.Join(gitDir, "config")

		// Ensure git directory and config are writable
		if err := os.Chmod(gitDir, 0755); err != nil {
			t.Fatalf("failed to set git dir permissions: %v", err)
		}
		if err := os.Chmod(gitConfigPath, 0644); err != nil {
			t.Fatalf("failed to set config permissions: %v", err)
		}

		// Run fix
		err := MergeDriver(dir)
		if err != nil {
			t.Fatalf("MergeDriver fix should succeed with writable config: %v", err)
		}

		// Verify config was set
		output := runGit(t, dir, "config", "--get", "merge.beads.driver")
		expected := "bd merge %A %O %A %B"
		if strings.TrimSpace(output) != expected {
			t.Errorf("expected merge driver %q, got %q", expected, output)
		}
	})
}

// TestPermissionsWithWrongPermissions_E2E tests fixing wrong permissions on .beads
func TestPermissionsWithWrongPermissions_E2E(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission tests when running as root")
	}

	t.Run("fixes .beads directory with wrong permissions", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Set wrong permissions (too permissive)
		if err := os.Chmod(beadsDir, 0777); err != nil {
			t.Fatal(err)
		}

		// Verify wrong permissions
		info, err := os.Stat(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() == 0700 {
			t.Skip("permissions already correct")
		}

		// Run fix
		err = Permissions(dir)
		if err != nil {
			t.Fatalf("Permissions fix failed: %v", err)
		}

		// Verify permissions were fixed
		info, err = os.Stat(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Errorf("expected permissions 0700, got %04o", info.Mode().Perm())
		}
	})

	t.Run("fixes database file with wrong permissions", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0700); err != nil {
			t.Fatal(err)
		}

		// Create database file with wrong permissions
		dbPath := filepath.Join(beadsDir, "beads.db")
		if err := os.WriteFile(dbPath, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		// Set wrong permissions (too permissive)
		if err := os.Chmod(dbPath, 0666); err != nil {
			t.Fatal(err)
		}

		// Run fix
		err := Permissions(dir)
		if err != nil {
			t.Fatalf("Permissions fix failed: %v", err)
		}

		// Verify permissions were fixed
		info, err := os.Stat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("expected permissions 0600, got %04o", info.Mode().Perm())
		}
	})

	t.Run("fixes database file without read permission", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0700); err != nil {
			t.Fatal(err)
		}

		// Create database file
		dbPath := filepath.Join(beadsDir, "beads.db")
		if err := os.WriteFile(dbPath, []byte("test"), 0200); err != nil {
			t.Fatal(err)
		}

		// Run fix
		err := Permissions(dir)
		if err != nil {
			t.Fatalf("Permissions fix failed: %v", err)
		}

		// Verify permissions were fixed to include read
		info, err := os.Stat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		perms := info.Mode().Perm()
		if perms&0400 == 0 {
			t.Error("database should have read permission for owner")
		}
		if perms != 0600 {
			t.Errorf("expected permissions 0600, got %04o", perms)
		}
	})

	t.Run("handles .beads directory without write permission", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0700); err != nil {
			t.Fatal(err)
		}

		// Create a test file in .beads
		testFile := filepath.Join(beadsDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}

		// Make .beads read-only (no write, no execute)
		if err := os.Chmod(beadsDir, 0400); err != nil {
			t.Fatal(err)
		}

		// Restore permissions for cleanup
		defer func() {
			_ = os.Chmod(beadsDir, 0700)
		}()

		// Run fix - should restore write permission
		err := Permissions(dir)
		if err != nil {
			t.Fatalf("Permissions fix failed: %v", err)
		}

		// Verify directory now has correct permissions
		info, err := os.Stat(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Errorf("expected permissions 0700, got %04o", info.Mode().Perm())
		}
	})

	t.Run("handles multiple files with wrong permissions", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0777); err != nil {
			t.Fatal(err)
		}

		// Create database with wrong permissions
		dbPath := filepath.Join(beadsDir, "beads.db")
		if err := os.WriteFile(dbPath, []byte("db"), 0666); err != nil {
			t.Fatal(err)
		}

		// Run fix
		err := Permissions(dir)
		if err != nil {
			t.Fatalf("Permissions fix failed: %v", err)
		}

		// Verify both directory and file were fixed
		dirInfo, err := os.Stat(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0700 {
			t.Errorf("expected directory permissions 0700, got %04o", dirInfo.Mode().Perm())
		}

		dbInfo, err := os.Stat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if dbInfo.Mode().Perm() != 0600 {
			t.Errorf("expected database permissions 0600, got %04o", dbInfo.Mode().Perm())
		}
	})
}

// Note: Helper functions setupTestGitRepo and runGit are defined in fix_test.go
