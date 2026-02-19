package fix

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
