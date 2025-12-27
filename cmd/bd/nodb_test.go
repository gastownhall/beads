package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

func TestExtractIssuePrefix(t *testing.T) {
	tests := []struct {
		name     string
		issueID  string
		expected string
	}{
		{"standard ID", "bd-123", "bd"},
		{"custom prefix", "myproject-456", "myproject"},
		{"hash ID", "bd-abc123def", "bd"},
		{"multi-part prefix with numeric suffix", "alpha-beta-1", "alpha-beta"},
		{"multi-part non-numeric suffix", "vc-baseline-test", "vc"}, // Falls back to first hyphen
		{"beads-vscode style", "beads-vscode-42", "beads-vscode"},
		{"no hyphen", "nohyphen", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIssuePrefix(tt.issueID)
			if got != tt.expected {
				t.Errorf("extractIssuePrefix(%q) = %q, want %q", tt.issueID, got, tt.expected)
			}
		})
	}
}

func TestLoadIssuesFromJSONL(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "test.jsonl")

	// Create test JSONL file
	content := `{"id":"bd-1","title":"Test Issue 1","description":"Test"}
{"id":"bd-2","title":"Test Issue 2","description":"Another test"}

{"id":"bd-3","title":"Test Issue 3","description":"Third test"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	issues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("loadIssuesFromJSONL failed: %v", err)
	}

	if len(issues) != 3 {
		t.Errorf("Expected 3 issues, got %d", len(issues))
	}

	if issues[0].ID != "bd-1" || issues[0].Title != "Test Issue 1" {
		t.Errorf("First issue mismatch: %+v", issues[0])
	}
	if issues[1].ID != "bd-2" {
		t.Errorf("Second issue ID mismatch: %s", issues[1].ID)
	}
	if issues[2].ID != "bd-3" {
		t.Errorf("Third issue ID mismatch: %s", issues[2].ID)
	}
}

func TestLoadIssuesFromJSONL_InvalidJSON(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "invalid.jsonl")

	content := `{"id":"bd-1","title":"Valid"}
invalid json here
{"id":"bd-2","title":"Another valid"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err := loadIssuesFromJSONL(jsonlPath)
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

func TestLoadIssuesFromJSONL_NonExistent(t *testing.T) {
	_, err := loadIssuesFromJSONL("/nonexistent/file.jsonl")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

func TestDetectPrefix(t *testing.T) {
	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	t.Run("from existing issues", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))

		// Add issues with common prefix
		issues := []*types.Issue{
			{ID: "myapp-1", Title: "Issue 1"},
			{ID: "myapp-2", Title: "Issue 2"},
		}
		if err := memStore.LoadFromIssues(issues); err != nil {
			t.Fatalf("Failed to load issues: %v", err)
		}

		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "myapp" {
			t.Errorf("Expected prefix 'myapp', got '%s'", prefix)
		}
	})

	t.Run("mixed prefixes error", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))

		issues := []*types.Issue{
			{ID: "app1-1", Title: "Issue 1"},
			{ID: "app2-2", Title: "Issue 2"},
		}
		if err := memStore.LoadFromIssues(issues); err != nil {
			t.Fatalf("Failed to load issues: %v", err)
		}

		_, err := detectPrefix(beadsDir, memStore)
		if err == nil {
			t.Error("Expected error for mixed prefixes, got nil")
		}
	})

	t.Run("empty database defaults to dir name", func(t *testing.T) {
		// Change to temp dir so we can control directory name
		namedDir := filepath.Join(tempDir, "myproject")
		if err := os.MkdirAll(namedDir, 0o755); err != nil {
			t.Fatalf("Failed to create named dir: %v", err)
		}
		t.Chdir(namedDir)

		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))
		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "myproject" {
			t.Errorf("Expected prefix 'myproject', got '%s'", prefix)
		}
	})
}

func TestWriteIssuesToJSONL(t *testing.T) {
	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))

	issues := []*types.Issue{
		{ID: "bd-1", Title: "Test Issue 1", Description: "Desc 1"},
		{ID: "bd-2", Title: "Test Issue 2", Description: "Desc 2"},
	}
	if err := memStore.LoadFromIssues(issues); err != nil {
		t.Fatalf("Failed to load issues: %v", err)
	}

	if err := writeIssuesToJSONL(memStore, beadsDir); err != nil {
		t.Fatalf("writeIssuesToJSONL failed: %v", err)
	}

	// Verify file exists and contains correct data
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	loadedIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to load written JSONL: %v", err)
	}

	if len(loadedIssues) != 2 {
		t.Errorf("Expected 2 issues in JSONL, got %d", len(loadedIssues))
	}
}
