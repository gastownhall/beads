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

// setupTestWorkspace creates a temporary directory with a .beads directory
func setupTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	return dir
}

// setupTestGitRepo creates a temporary git repository with a .beads directory
func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := setupTestWorkspace(t)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()

	return dir
}

// runGit runs a git command and returns output
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s", args, output)
	}
	return string(output)
}

// TestValidateBeadsWorkspace tests the workspace validation function
func TestValidateBeadsWorkspace(t *testing.T) {
	t.Run("invalid path", func(t *testing.T) {
		err := validateBeadsWorkspace("/nonexistent/path/that/does/not/exist")
		if err == nil {
			t.Error("expected error for nonexistent path")
		}
	})
}

// TestGitHooks_Validation tests GitHooks validation
func TestGitHooks_Validation(t *testing.T) {
	t.Run("not a git repository", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := GitHooks(dir)
		if err == nil {
			t.Error("expected error for non-git repository")
		}
		if err.Error() != "not a git repository" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestMergeDriver_Validation tests MergeDriver validation
func TestMergeDriver_Validation(t *testing.T) {
	t.Run("sets correct merge driver config", func(t *testing.T) {
		dir := setupTestGitRepo(t)

		err := MergeDriver(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the config was set
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

// TestDaemon_Validation tests Daemon validation
func TestDaemon_Validation(t *testing.T) {
	t.Run("no socket - nothing to do", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := Daemon(dir)
		if err != nil {
			t.Errorf("expected no error when no socket exists, got: %v", err)
		}
	})
}

// TestDBJSONLSync_Validation tests DBJSONLSync validation
func TestDBJSONLSync_Validation(t *testing.T) {
	t.Run("no database - nothing to do", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := DBJSONLSync(dir)
		if err != nil {
			t.Errorf("expected no error when no database exists, got: %v", err)
		}
	})

	t.Run("no JSONL - nothing to do", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		// Create a database file
		dbPath := filepath.Join(dir, ".beads", "beads.db")
		if err := os.WriteFile(dbPath, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test db: %v", err)
		}
		err := DBJSONLSync(dir)
		if err != nil {
			t.Errorf("expected no error when no JSONL exists, got: %v", err)
		}
	})
}

// TestSyncBranchConfig_Validation tests SyncBranchConfig validation
func TestSyncBranchConfig_Validation(t *testing.T) {
	t.Run("not a git repository", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := SyncBranchConfig(dir)
		if err == nil {
			t.Error("expected error for non-git repository")
		}
	})
}

// TestSyncBranchHealth_Validation tests SyncBranchHealth validation
func TestSyncBranchHealth_Validation(t *testing.T) {
	t.Run("no main or master branch", func(t *testing.T) {
		dir := setupTestGitRepo(t)
		// Create a commit on a different branch
		cmd := exec.Command("git", "checkout", "-b", "other")
		cmd.Dir = dir
		_ = cmd.Run()

		// Create a file and commit
		testFile := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		cmd = exec.Command("git", "add", "test.txt")
		cmd.Dir = dir
		_ = cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "initial")
		cmd.Dir = dir
		_ = cmd.Run()

		err := SyncBranchHealth(dir, "beads-sync")
		if err == nil {
			t.Error("expected error when neither main nor master exists")
		}
	})
}

// TestUntrackedJSONL_Validation tests UntrackedJSONL validation
func TestUntrackedJSONL_Validation(t *testing.T) {
	t.Run("not a git repository", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := UntrackedJSONL(dir)
		if err == nil {
			t.Error("expected error for non-git repository")
		}
	})

	t.Run("no untracked files", func(t *testing.T) {
		dir := setupTestGitRepo(t)
		err := UntrackedJSONL(dir)
		// Should succeed with no untracked files
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})
}

// TestMigrateTombstones tests the MigrateTombstones function
func TestMigrateTombstones(t *testing.T) {
	t.Run("no deletions.jsonl - nothing to migrate", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		err := MigrateTombstones(dir)
		if err != nil {
			t.Errorf("expected no error when no deletions.jsonl exists, got: %v", err)
		}
	})

	t.Run("empty deletions.jsonl", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		deletionsPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		if err := os.WriteFile(deletionsPath, []byte(""), 0600); err != nil {
			t.Fatalf("failed to create deletions.jsonl: %v", err)
		}
		err := MigrateTombstones(dir)
		if err != nil {
			t.Errorf("expected no error for empty deletions.jsonl, got: %v", err)
		}
	})

	t.Run("migrates deletions to tombstones", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		// Create deletions.jsonl with a deletion record
		deletionsPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		deletion := legacyDeletionRecord{
			ID:        "test-123",
			Timestamp: time.Now(),
			Actor:     "testuser",
			Reason:    "test deletion",
		}
		data, _ := json.Marshal(deletion)
		if err := os.WriteFile(deletionsPath, append(data, '\n'), 0600); err != nil {
			t.Fatalf("failed to create deletions.jsonl: %v", err)
		}

		// Create empty issues.jsonl
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(""), 0600); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}

		err := MigrateTombstones(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify deletions.jsonl was renamed
		if _, err := os.Stat(deletionsPath); !os.IsNotExist(err) {
			t.Error("deletions.jsonl should have been renamed")
		}
		migratedPath := deletionsPath + ".migrated"
		if _, err := os.Stat(migratedPath); os.IsNotExist(err) {
			t.Error("deletions.jsonl.migrated should exist")
		}

		// Verify tombstone was written to issues.jsonl
		content, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("failed to read issues.jsonl: %v", err)
		}
		if len(content) == 0 {
			t.Error("expected tombstone to be written to issues.jsonl")
		}

		// Verify the tombstone content
		var issue struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(content[:len(content)-1], &issue); err != nil {
			t.Fatalf("failed to parse tombstone: %v", err)
		}
		if issue.ID != "test-123" {
			t.Errorf("expected ID test-123, got %s", issue.ID)
		}
		if issue.Status != "tombstone" {
			t.Errorf("expected status tombstone, got %s", issue.Status)
		}
	})

	t.Run("skips already existing tombstones", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		// Create deletions.jsonl with a deletion record
		deletionsPath := filepath.Join(dir, ".beads", "deletions.jsonl")
		deletion := legacyDeletionRecord{
			ID:        "test-123",
			Timestamp: time.Now(),
			Actor:     "testuser",
		}
		data, _ := json.Marshal(deletion)
		if err := os.WriteFile(deletionsPath, append(data, '\n'), 0600); err != nil {
			t.Fatalf("failed to create deletions.jsonl: %v", err)
		}

		// Create issues.jsonl with an existing tombstone for the same ID
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		existingTombstone := map[string]interface{}{
			"id":     "test-123",
			"status": "tombstone",
		}
		existingData, _ := json.Marshal(existingTombstone)
		if err := os.WriteFile(jsonlPath, append(existingData, '\n'), 0600); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}

		originalContent, _ := os.ReadFile(jsonlPath)

		err := MigrateTombstones(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify issues.jsonl was not modified (tombstone already exists)
		newContent, _ := os.ReadFile(jsonlPath)
		if string(newContent) != string(originalContent) {
			t.Error("issues.jsonl should not have been modified when tombstone already exists")
		}
	})
}

// TestLoadLegacyDeletions tests the loadLegacyDeletions helper
func TestLoadLegacyDeletions(t *testing.T) {
	t.Run("nonexistent file returns empty map", func(t *testing.T) {
		records, err := loadLegacyDeletions("/nonexistent/path")
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
		if len(records) != 0 {
			t.Errorf("expected empty map, got %d records", len(records))
		}
	})

	t.Run("parses valid deletions", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "deletions.jsonl")

		deletion := legacyDeletionRecord{
			ID:        "test-abc",
			Timestamp: time.Now(),
			Actor:     "user",
			Reason:    "testing",
		}
		data, _ := json.Marshal(deletion)
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		records, err := loadLegacyDeletions(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("expected 1 record, got %d", len(records))
		}
		if records["test-abc"].Actor != "user" {
			t.Errorf("expected actor 'user', got %s", records["test-abc"].Actor)
		}
	})

	t.Run("skips invalid lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "deletions.jsonl")

		content := `{"id":"valid-1","ts":"2024-01-01T00:00:00Z","by":"user"}
invalid json line
{"id":"valid-2","ts":"2024-01-01T00:00:00Z","by":"user"}
`
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		records, err := loadLegacyDeletions(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("expected 2 valid records, got %d", len(records))
		}
	})

	t.Run("skips records without ID", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "deletions.jsonl")

		content := `{"id":"valid-1","ts":"2024-01-01T00:00:00Z","by":"user"}
{"ts":"2024-01-01T00:00:00Z","by":"user"}
`
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		records, err := loadLegacyDeletions(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("expected 1 valid record, got %d", len(records))
		}
	})
}

// TestConvertLegacyDeletionToTombstone tests tombstone conversion
func TestConvertLegacyDeletionToTombstone(t *testing.T) {
	t.Run("converts with all fields", func(t *testing.T) {
		ts := time.Now()
		record := legacyDeletionRecord{
			ID:        "test-xyz",
			Timestamp: ts,
			Actor:     "admin",
			Reason:    "cleanup",
		}

		tombstone := convertLegacyDeletionToTombstone(record)

		if tombstone.ID != "test-xyz" {
			t.Errorf("expected ID test-xyz, got %s", tombstone.ID)
		}
		if tombstone.Status != "tombstone" {
			t.Errorf("expected status tombstone, got %s", tombstone.Status)
		}
		if tombstone.DeletedBy != "admin" {
			t.Errorf("expected DeletedBy admin, got %s", tombstone.DeletedBy)
		}
		if tombstone.DeleteReason != "cleanup" {
			t.Errorf("expected DeleteReason cleanup, got %s", tombstone.DeleteReason)
		}
		if tombstone.DeletedAt == nil {
			t.Error("expected DeletedAt to be set")
		}
	})

	t.Run("handles zero timestamp", func(t *testing.T) {
		record := legacyDeletionRecord{
			ID:    "test-zero",
			Actor: "user",
		}

		tombstone := convertLegacyDeletionToTombstone(record)

		if tombstone.DeletedAt == nil {
			t.Error("expected DeletedAt to be set even with zero timestamp")
		}
	})
}

// TestFindJSONLPath tests the findJSONLPath helper
func TestFindJSONLPath(t *testing.T) {
	t.Run("returns empty for no JSONL", func(t *testing.T) {
		dir := t.TempDir()
		path := findJSONLPath(dir)
		if path != "" {
			t.Errorf("expected empty path, got %s", path)
		}
	})

	t.Run("finds issues.jsonl", func(t *testing.T) {
		dir := t.TempDir()
		jsonlPath := filepath.Join(dir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		path := findJSONLPath(dir)
		if path != jsonlPath {
			t.Errorf("expected %s, got %s", jsonlPath, path)
		}
	})

	t.Run("finds beads.jsonl as fallback", func(t *testing.T) {
		dir := t.TempDir()
		jsonlPath := filepath.Join(dir, "beads.jsonl")
		if err := os.WriteFile(jsonlPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		path := findJSONLPath(dir)
		if path != jsonlPath {
			t.Errorf("expected %s, got %s", jsonlPath, path)
		}
	})

	t.Run("prefers issues.jsonl over beads.jsonl", func(t *testing.T) {
		dir := t.TempDir()
		issuesPath := filepath.Join(dir, "issues.jsonl")
		beadsPath := filepath.Join(dir, "beads.jsonl")
		if err := os.WriteFile(issuesPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create issues.jsonl: %v", err)
		}
		if err := os.WriteFile(beadsPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("failed to create beads.jsonl: %v", err)
		}

		path := findJSONLPath(dir)
		if path != issuesPath {
			t.Errorf("expected %s, got %s", issuesPath, path)
		}
	})
}

// TestIsWithinWorkspace tests the isWithinWorkspace helper
func TestIsWithinWorkspace(t *testing.T) {
	root := t.TempDir()

	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{
			name:      "same directory",
			candidate: root,
			want:      true,
		},
		{
			name:      "subdirectory",
			candidate: filepath.Join(root, "subdir"),
			want:      true,
		},
		{
			name:      "nested subdirectory",
			candidate: filepath.Join(root, "sub", "dir", "nested"),
			want:      true,
		},
		{
			name:      "parent directory",
			candidate: filepath.Dir(root),
			want:      false,
		},
		{
			name:      "sibling directory",
			candidate: filepath.Join(filepath.Dir(root), "sibling"),
			want:      false,
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


// TestDBJSONLSync_MissingDatabase tests DBJSONLSync when database doesn't exist
func TestDBJSONLSync_MissingDatabase(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Create only JSONL file
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	issue := map[string]interface{}{
		"id":     "test-no-db",
		"title":  "No DB Test",
		"status": "open",
	}
	data, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0600); err != nil {
		t.Fatalf("failed to create jsonl: %v", err)
	}

	// Should return without error since there's nothing to sync
	err := DBJSONLSync(dir)
	if err != nil {
		t.Errorf("expected no error when database doesn't exist, got: %v", err)
	}
}


// TestSyncBranchConfig_BranchDoesNotExist tests fixing config when branch doesn't exist
func TestSyncBranchConfig_BranchDoesNotExist(t *testing.T) {
	// Skip if running as test binary (can't execute bd subcommands)
	skipIfTestBinary(t)

	dir := setupTestGitRepo(t)

	// Try to run fix without any commits (no branch exists yet)
	err := SyncBranchConfig(dir)
	if err == nil {
		t.Error("expected error when no branch exists")
	}
	if err != nil && !strings.Contains(err.Error(), "failed to get current branch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSyncBranchConfig_InvalidRemoteURL tests fix behavior with invalid remote
func TestSyncBranchConfig_InvalidRemoteURL(t *testing.T) {
	// Skip if running as test binary (can't execute bd subcommands)
	skipIfTestBinary(t)

	dir := setupTestGitRepo(t)

	// Create initial commit
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	runGit(t, dir, "add", "test.txt")
	runGit(t, dir, "commit", "-m", "initial commit")

	// Add invalid remote
	runGit(t, dir, "remote", "add", "origin", "invalid://bad-url")

	// Fix should still succeed - it only sets config, doesn't interact with remote
	err := SyncBranchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error with invalid remote: %v", err)
	}

	// Verify config was set
	cmd := exec.Command("git", "config", "sync.branch")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get sync.branch config: %v", err)
	}
	if strings.TrimSpace(string(output)) == "" {
		t.Error("sync.branch config was not set")
	}
}

func TestCountJSONLIssues(t *testing.T) {
	t.Parallel()

	t.Run("empty_JSONL", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

		// Create empty JSONL
		if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		count, err := countJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Errorf("expected 0, got %d", count)
		}
	})

	t.Run("valid_issues", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

		// Create JSONL with 3 issues
		jsonl := []byte(`{"id":"bd-1","title":"First"}
{"id":"bd-2","title":"Second"}
{"id":"bd-3","title":"Third"}
`)
		if err := os.WriteFile(jsonlPath, jsonl, 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		count, err := countJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 3 {
			t.Errorf("expected 3, got %d", count)
		}
	})

	t.Run("mixed_valid_and_invalid", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

		// Create JSONL with 2 valid and some invalid lines
		jsonl := []byte(`{"id":"bd-1","title":"First"}
invalid json line
{"id":"bd-2","title":"Second"}
{"title":"No ID"}
`)
		if err := os.WriteFile(jsonlPath, jsonl, 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		count, err := countJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 2 {
			t.Errorf("expected 2, got %d", count)
		}
	})

	t.Run("nonexistent_file", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		jsonlPath := filepath.Join(dir, ".beads", "nonexistent.jsonl")

		count, err := countJSONLIssues(jsonlPath)
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
		if count != 0 {
			t.Errorf("expected 0, got %d", count)
		}
	})
}

