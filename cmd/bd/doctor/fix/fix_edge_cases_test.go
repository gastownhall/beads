package fix

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIsWithinWorkspace_PathTraversal tests path traversal attempts
func TestIsWithinWorkspace_PathTraversal(t *testing.T) {
	root := t.TempDir()

	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{
			name:      "simple dotdot traversal",
			candidate: filepath.Join(root, "..", "etc"),
			want:      false,
		},
		{
			name:      "dotdot in middle of path",
			candidate: filepath.Join(root, "subdir", "..", "..", "etc"),
			want:      false,
		},
		{
			name:      "multiple dotdot",
			candidate: filepath.Join(root, "..", "..", ".."),
			want:      false,
		},
		{
			name:      "dotdot stays within workspace",
			candidate: filepath.Join(root, "a", "b", "..", "c"),
			want:      true,
		},
		{
			name:      "relative path with dotdot",
			candidate: filepath.Join(root, "subdir", "..", "file"),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWithinWorkspace(root, tt.candidate)
			if got != tt.want {
				t.Errorf("isWithinWorkspace(%q, %q) = %v, want %v", root, tt.candidate, got, tt.want)
			}
		})
	}
}

// TestValidateBeadsWorkspace_EdgeCases tests edge cases for workspace validation
func TestValidateBeadsWorkspace_EdgeCases(t *testing.T) {
	t.Run("nested .beads directories", func(t *testing.T) {
		// Create a workspace with nested .beads directories
		dir := setupTestWorkspace(t)
		nestedDir := filepath.Join(dir, "subdir")
		nestedBeadsDir := filepath.Join(nestedDir, ".beads")
		if err := os.MkdirAll(nestedBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create nested .beads: %v", err)
		}

		// Root workspace should be valid
		if err := validateBeadsWorkspace(dir); err != nil {
			t.Errorf("expected root workspace to be valid, got: %v", err)
		}

		// Nested workspace should also be valid
		if err := validateBeadsWorkspace(nestedDir); err != nil {
			t.Errorf("expected nested workspace to be valid, got: %v", err)
		}
	})

	t.Run(".beads as a file not directory", func(t *testing.T) {
		dir := t.TempDir()
		beadsFile := filepath.Join(dir, ".beads")
		// Create .beads as a file instead of directory
		if err := os.WriteFile(beadsFile, []byte("not a directory"), 0600); err != nil {
			t.Fatalf("failed to create .beads file: %v", err)
		}

		err := validateBeadsWorkspace(dir)
		// NOTE: Current implementation only checks if .beads exists via os.Stat,
		// but doesn't verify it's a directory. This test documents current behavior.
		// A future improvement could add IsDir() check.
		if err == nil {
			// Currently passes - implementation doesn't validate it's a directory
			t.Log(".beads exists as file - validation passes (edge case)")
		}
	})

	t.Run(".beads as symlink to directory", func(t *testing.T) {
		dir := t.TempDir()
		// Create actual .beads directory elsewhere
		actualBeadsDir := filepath.Join(t.TempDir(), "actual_beads")
		if err := os.MkdirAll(actualBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create actual beads dir: %v", err)
		}

		// Create symlink .beads -> actual_beads
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink(actualBeadsDir, symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		// Should be valid - symlink to directory is acceptable
		if err := validateBeadsWorkspace(dir); err != nil {
			t.Errorf("expected symlinked .beads directory to be valid, got: %v", err)
		}
	})

	t.Run(".beads as symlink to file", func(t *testing.T) {
		dir := t.TempDir()
		// Create a file
		actualFile := filepath.Join(t.TempDir(), "actual_file")
		if err := os.WriteFile(actualFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create actual file: %v", err)
		}

		// Create symlink .beads -> file
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink(actualFile, symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		err := validateBeadsWorkspace(dir)
		// NOTE: os.Stat follows symlinks, so if symlink points to a file,
		// it just sees the file exists and returns no error.
		// Current implementation doesn't verify it's a directory.
		if err == nil {
			t.Log(".beads symlink to file - validation passes (edge case)")
		}
	})

	t.Run(".beads as broken symlink", func(t *testing.T) {
		dir := t.TempDir()
		// Create symlink to non-existent target
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink("/nonexistent/target", symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		err := validateBeadsWorkspace(dir)
		if err == nil {
			t.Error("expected error when .beads is a broken symlink")
		}
	})

	t.Run("relative path resolution", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		// Test with relative path
		originalWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get working directory: %v", err)
		}
		defer func() {
			if err := os.Chdir(originalWd); err != nil {
				t.Logf("failed to restore working directory: %v", err)
			}
		}()

		if err := os.Chdir(filepath.Dir(dir)); err != nil {
			t.Fatalf("failed to change directory: %v", err)
		}

		relPath := filepath.Base(dir)
		if err := validateBeadsWorkspace(relPath); err != nil {
			t.Errorf("expected relative path to be valid, got: %v", err)
		}
	})
}

// TestFindJSONLPath_EdgeCases tests edge cases for finding JSONL files
func TestFindJSONLPath_EdgeCases(t *testing.T) {
	t.Run("multiple JSONL files - issues.jsonl takes precedence", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}

		// Create both files
		issuesPath := filepath.Join(beadsDir, "issues.jsonl")
		beadsPath := filepath.Join(beadsDir, "beads.jsonl")
		if err := os.WriteFile(issuesPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}
		if err := os.WriteFile(beadsPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create beads.jsonl: %v", err)
		}

		path := findJSONLPath(beadsDir)
		if path != issuesPath {
			t.Errorf("expected %s, got %s", issuesPath, path)
		}
	})

	t.Run("only beads.jsonl exists", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}

		beadsPath := filepath.Join(beadsDir, "beads.jsonl")
		if err := os.WriteFile(beadsPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create beads.jsonl: %v", err)
		}

		path := findJSONLPath(beadsDir)
		if path != beadsPath {
			t.Errorf("expected %s, got %s", beadsPath, path)
		}
	})

	t.Run("JSONL file as symlink", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}

		// Create actual file
		actualFile := filepath.Join(t.TempDir(), "actual_issues.jsonl")
		if err := os.WriteFile(actualFile, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create actual file: %v", err)
		}

		// Create symlink
		symlinkPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.Symlink(actualFile, symlinkPath); err != nil {
			t.Skipf("symlink creation failed (may not be supported): %v", err)
		}

		path := findJSONLPath(beadsDir)
		if path != symlinkPath {
			t.Errorf("expected symlink to be found: %s, got %s", symlinkPath, path)
		}
	})

	t.Run("JSONL file is directory", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}

		// Create issues.jsonl as directory instead of file
		issuesDir := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.MkdirAll(issuesDir, 0755); err != nil {
			t.Fatalf("failed to create issues.jsonl dir: %v", err)
		}

		path := findJSONLPath(beadsDir)
		// NOTE: Current implementation only checks if path exists via os.Stat,
		// but doesn't verify it's a regular file. Returns path even for directories.
		// This documents current behavior - a future improvement could add IsRegular() check.
		if path == issuesDir {
			t.Log("issues.jsonl exists as directory - findJSONLPath returns it (edge case)")
		}
	})

	t.Run("no JSONL files present", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}

		path := findJSONLPath(beadsDir)
		if path != "" {
			t.Errorf("expected empty path, got %s", path)
		}
	})

	t.Run("empty beadsDir path", func(t *testing.T) {
		path := findJSONLPath("")
		if path != "" {
			t.Errorf("expected empty path for empty beadsDir, got %s", path)
		}
	})

	t.Run("nonexistent beadsDir", func(t *testing.T) {
		path := findJSONLPath("/nonexistent/path/to/beads")
		if path != "" {
			t.Errorf("expected empty path for nonexistent beadsDir, got %s", path)
		}
	})
}

// TestGitHooks_EdgeCases tests GitHooks with edge cases
func TestGitHooks_EdgeCases(t *testing.T) {
	// Skip if running as test binary (can't execute bd subcommands)
	skipIfTestBinary(t)

	t.Run("hooks directory does not exist", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Verify .git/hooks doesn't exist or remove it
		hooksDir := filepath.Join(dir, ".git", "hooks")
		_ = os.RemoveAll(hooksDir)

		// GitHooks should create the directory via bd hooks install
		err := GitHooks(dir)
		if err != nil {
			t.Errorf("GitHooks should succeed when hooks directory doesn't exist, got: %v", err)
		}

		// Verify hooks directory was created
		if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
			t.Error("expected hooks directory to be created")
		}
	})

	t.Run("git worktree with .git file", func(t *testing.T) {
		// Create main repo
		mainDir := setupTestGitRepo(t)

		// Create a commit so we can create a worktree
		testFile := filepath.Join(mainDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, mainDir, "add", "test.txt")
		runGit(t, mainDir, "commit", "-m", "initial")

		// Create a worktree
		worktreeDir := t.TempDir()
		runGit(t, mainDir, "worktree", "add", worktreeDir, "-b", "feature")

		// Create .beads in worktree
		beadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads in worktree: %v", err)
		}

		// GitHooks should work with worktrees where .git is a file
		err := GitHooks(worktreeDir)
		if err != nil {
			t.Errorf("GitHooks should work with git worktrees, got: %v", err)
		}
	})
}

// TestMergeDriver_EdgeCases tests MergeDriver with edge cases
func TestMergeDriver_EdgeCases(t *testing.T) {
	t.Run("read-only git config file", func(t *testing.T) {
		dir := setupTestGitRepo(t)
		gitDir := filepath.Join(dir, ".git")
		gitConfigPath := filepath.Join(gitDir, "config")

		// Make both .git directory and config file read-only to truly prevent writes
		// (git might otherwise create a new file and rename it)
		if err := os.Chmod(gitConfigPath, 0400); err != nil {
			t.Fatalf("failed to make config read-only: %v", err)
		}
		if err := os.Chmod(gitDir, 0500); err != nil {
			t.Fatalf("failed to make .git read-only: %v", err)
		}

		// Restore write permissions at the end
		defer func() {
			_ = os.Chmod(gitDir, 0700)
			_ = os.Chmod(gitConfigPath, 0600)
		}()

		// MergeDriver should fail with read-only config
		err := MergeDriver(dir)
		if err == nil {
			t.Error("expected error when git config is read-only")
		}
	})

	t.Run("succeeds after config was previously set", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Set the merge driver config initially
		err := MergeDriver(dir)
		if err != nil {
			t.Fatalf("first MergeDriver call failed: %v", err)
		}

		// Run again to verify it handles existing config
		err = MergeDriver(dir)
		if err != nil {
			t.Errorf("MergeDriver should succeed when config already exists, got: %v", err)
		}

		// Verify the config is still correct
		cmd := exec.Command("git", "config", "merge.beads.driver")
		cmd.Dir = dir
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("failed to get git config: %v", err)
		}

		expected := "bd merge %A %O %A %B\n"
		if string(output) != expected {
			t.Errorf("expected %q, got %q", expected, string(output))
		}
	})
}

// TestUntrackedJSONL_EdgeCases tests UntrackedJSONL with edge cases
func TestUntrackedJSONL_EdgeCases(t *testing.T) {
	t.Run("staged but uncommitted JSONL files", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit
		testFile := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "test.txt")
		runGit(t, dir, "commit", "-m", "initial")

		// Create a JSONL file and stage it but don't commit
		jsonlFile := filepath.Join(dir, ".beads", "deletions.jsonl")
		if err := os.WriteFile(jsonlFile, []byte(`{"id":"test-1","ts":"2024-01-01T00:00:00Z","by":"user"}`+"\n"), 0600); err != nil {
			t.Fatalf("failed to create JSONL file: %v", err)
		}
		runGit(t, dir, "add", ".beads/deletions.jsonl")

		// Check git status - should show staged file
		output := runGit(t, dir, "status", "--porcelain", ".beads/")
		if !strings.Contains(output, "A  .beads/deletions.jsonl") {
			t.Logf("git status output: %s", output)
			t.Error("expected file to be staged")
		}

		// UntrackedJSONL should not process staged files (only untracked)
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}

		// File should still be staged, not committed again
		output = runGit(t, dir, "status", "--porcelain", ".beads/")
		if !strings.Contains(output, "A  .beads/deletions.jsonl") {
			t.Error("file should still be staged after UntrackedJSONL")
		}
	})

	t.Run("mixed tracked and untracked JSONL files", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit with one JSONL file
		trackedFile := filepath.Join(dir, ".beads", "issues.jsonl")
		if err := os.WriteFile(trackedFile, []byte(`{"id":"test-1"}`+"\n"), 0600); err != nil {
			t.Fatalf("failed to create tracked JSONL: %v", err)
		}
		runGit(t, dir, "add", ".beads/issues.jsonl")
		runGit(t, dir, "commit", "-m", "initial")

		// Create an untracked JSONL file
		untrackedFile := filepath.Join(dir, ".beads", "deletions.jsonl")
		if err := os.WriteFile(untrackedFile, []byte(`{"id":"test-2"}`+"\n"), 0600); err != nil {
			t.Fatalf("failed to create untracked JSONL: %v", err)
		}

		// UntrackedJSONL should only process the untracked file
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}

		// Verify untracked file was committed
		output := runGit(t, dir, "status", "--porcelain", ".beads/")
		if output != "" {
			t.Errorf("expected clean status, got: %s", output)
		}

		// Verify both files are now tracked
		output = runGit(t, dir, "ls-files", ".beads/")
		if !strings.Contains(output, "issues.jsonl") || !strings.Contains(output, "deletions.jsonl") {
			t.Errorf("expected both files to be tracked, got: %s", output)
		}
	})

	t.Run("JSONL file outside .beads directory is ignored", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		// Create initial commit
		testFile := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		runGit(t, dir, "add", "test.txt")
		runGit(t, dir, "commit", "-m", "initial")

		// Create a JSONL file outside .beads
		outsideFile := filepath.Join(dir, "data.jsonl")
		if err := os.WriteFile(outsideFile, []byte(`{"test":"data"}`+"\n"), 0600); err != nil {
			t.Fatalf("failed to create outside JSONL: %v", err)
		}

		// UntrackedJSONL should ignore it
		err := UntrackedJSONL(dir)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}

		// Verify the file is still untracked
		output := runGit(t, dir, "status", "--porcelain")
		if !strings.Contains(output, "?? data.jsonl") {
			t.Error("expected file outside .beads to remain untracked")
		}
	})
}

// TestMigrateTombstones_EdgeCases tests MigrateTombstones with edge cases
func TestMigrateTombstones_EdgeCases(t *testing.T) {
	t.Run("malformed deletions.jsonl with corrupt JSON", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		deletionsPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

		// Create deletions.jsonl with mix of valid and malformed JSON
		content := `{"id":"valid-1","ts":"2024-01-01T00:00:00Z","by":"user1"}
{corrupt json line without proper structure
{"id":"valid-2","ts":"2024-01-02T00:00:00Z","by":"user2","reason":"cleanup"}
{"incomplete":"object"
{"id":"valid-3","ts":"2024-01-03T00:00:00Z","by":"user3"}
`
		if err := os.WriteFile(deletionsPath, []byte(content), 0600); err != nil {
			t.Fatalf("failed to create deletions.jsonl: %v", err)
		}

		// Create empty issues.jsonl
		if err := os.WriteFile(jsonlPath, []byte(""), 0600); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}

		// Should succeed and migrate only valid records
		err := MigrateTombstones(dir)
		if err != nil {
			t.Fatalf("expected MigrateTombstones to handle malformed JSON, got: %v", err)
		}

		// Verify only valid records were migrated
		resultBytes, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("failed to read issues.jsonl: %v", err)
		}

		lines := strings.Split(strings.TrimSpace(string(resultBytes)), "\n")
		validCount := 0
		for _, line := range lines {
			if line == "" {
				continue
			}
			var issue struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(line), &issue); err == nil && issue.Status == "tombstone" {
				validCount++
			}
		}

		if validCount != 3 {
			t.Errorf("expected 3 valid tombstones, got %d", validCount)
		}
	})

	t.Run("deletions without ID field are skipped", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		deletionsPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

		// Create deletions.jsonl with records missing ID
		content := `{"id":"valid-1","ts":"2024-01-01T00:00:00Z","by":"user"}
{"ts":"2024-01-02T00:00:00Z","by":"user2"}
{"id":"","ts":"2024-01-03T00:00:00Z","by":"user3"}
{"id":"valid-2","ts":"2024-01-04T00:00:00Z","by":"user4"}
`
		if err := os.WriteFile(deletionsPath, []byte(content), 0600); err != nil {
			t.Fatalf("failed to create deletions.jsonl: %v", err)
		}

		// Create empty issues.jsonl
		if err := os.WriteFile(jsonlPath, []byte(""), 0600); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}

		err := MigrateTombstones(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify only records with valid IDs were migrated
		resultBytes, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("failed to read issues.jsonl: %v", err)
		}

		lines := strings.Split(strings.TrimSpace(string(resultBytes)), "\n")
		validCount := 0
		for _, line := range lines {
			if line == "" {
				continue
			}
			var issue struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(line), &issue); err == nil && issue.ID != "" {
				validCount++
			}
		}

		if validCount != 2 {
			t.Errorf("expected 2 valid tombstones, got %d", validCount)
		}
	})

	t.Run("handles missing issues.jsonl", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		deletionsPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

		// Create deletions.jsonl
		deletion := legacyDeletionRecord{
			ID:        "test-123",
			Timestamp: time.Now(),
			Actor:     "testuser",
		}
		data, _ := json.Marshal(deletion)
		if err := os.WriteFile(deletionsPath, append(data, '\n'), 0600); err != nil {
			t.Fatalf("failed to create deletions.jsonl: %v", err)
		}

		// Don't create issues.jsonl - it should be created by MigrateTombstones

		err := MigrateTombstones(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify issues.jsonl was created
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			t.Error("expected issues.jsonl to be created")
		}

		// Verify tombstone was written
		content, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("failed to read issues.jsonl: %v", err)
		}
		if len(content) == 0 {
			t.Error("expected tombstone to be written")
		}
	})
}

// TestPermissions_EdgeCases tests Permissions with edge cases
func TestPermissions_EdgeCases(t *testing.T) {
	t.Run("symbolic link to .beads directory", func(t *testing.T) {
		dir := t.TempDir()

		// Create actual .beads directory elsewhere
		actualBeadsDir := filepath.Join(t.TempDir(), "actual-beads")
		if err := os.MkdirAll(actualBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create actual .beads: %v", err)
		}

		// Create symlink to it
		symlinkPath := filepath.Join(dir, ".beads")
		if err := os.Symlink(actualBeadsDir, symlinkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}

		// Permissions should skip symlinked directories
		err := Permissions(dir)
		if err != nil {
			t.Errorf("expected no error for symlinked .beads, got: %v", err)
		}

		// Verify target directory permissions were not changed
		info, err := os.Stat(actualBeadsDir)
		if err != nil {
			t.Fatalf("failed to stat actual .beads: %v", err)
		}

		// Should still have 0755, not 0700
		if info.Mode().Perm() == 0700 {
			t.Error("symlinked directory permissions should not be changed to 0700")
		}
	})

	t.Run("symbolic link to database file", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		// Create actual database file elsewhere
		actualDbPath := filepath.Join(t.TempDir(), "actual-beads.db")
		if err := os.WriteFile(actualDbPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create actual db: %v", err)
		}

		// Create symlink to it
		dbSymlinkPath := filepath.Join(dir, ".beads", "beads.db")
		if err := os.Symlink(actualDbPath, dbSymlinkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}

		// Permissions should skip symlinked files
		err := Permissions(dir)
		if err != nil {
			t.Errorf("expected no error for symlinked db, got: %v", err)
		}

		// Verify target file permissions were not changed
		info, err := os.Stat(actualDbPath)
		if err != nil {
			t.Fatalf("failed to stat actual db: %v", err)
		}

		// Should still have 0644, not 0600
		if info.Mode().Perm() == 0600 {
			t.Error("symlinked database permissions should not be changed to 0600")
		}
	})

	t.Run("fixes incorrect .beads directory permissions", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		beadsDir := filepath.Join(dir, ".beads")

		// Set incorrect permissions (too permissive)
		if err := os.Chmod(beadsDir, 0755); err != nil {
			t.Fatalf("failed to set permissions: %v", err)
		}

		err := Permissions(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify permissions were fixed to 0700
		info, err := os.Stat(beadsDir)
		if err != nil {
			t.Fatalf("failed to stat .beads: %v", err)
		}

		if info.Mode().Perm() != 0700 {
			t.Errorf("expected permissions 0700, got %o", info.Mode().Perm())
		}
	})

	t.Run("fixes incorrect database file permissions", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		dbPath := filepath.Join(dir, ".beads", "beads.db")
		if err := os.WriteFile(dbPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create db: %v", err)
		}

		err := Permissions(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify permissions were fixed to 0600
		info, err := os.Stat(dbPath)
		if err != nil {
			t.Fatalf("failed to stat db: %v", err)
		}

		if info.Mode().Perm() != 0600 {
			t.Errorf("expected permissions 0600, got %o", info.Mode().Perm())
		}
	})

	t.Run("handles missing database file gracefully", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		// No database file exists
		err := Permissions(dir)
		if err != nil {
			t.Errorf("expected no error when database doesn't exist, got: %v", err)
		}
	})
}
