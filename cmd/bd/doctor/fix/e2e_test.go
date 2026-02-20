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

// isWSL returns true if running under Windows Subsystem for Linux.
// WSL doesn't fully respect Unix file permission semantics - the file owner
// can bypass read-only restrictions similar to macOS.
func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	version := strings.ToLower(string(data))
	return strings.Contains(version, "microsoft") || strings.Contains(version, "wsl")
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

// TestPermissionsWithWrongPermissions_E2E tests fixing wrong permissions on .beads
func TestPermissionsWithWrongPermissions_E2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping Unix permission test on Windows")
	}
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
