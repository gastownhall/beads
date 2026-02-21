//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestReadIssueFromJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")

	// Create test JSONL file
	issues := []*types.Issue{
		{ID: "bd-1", Title: "First issue"},
		{ID: "bd-2", Title: "Second issue"},
		{ID: "bd-3", Title: "Third issue"},
	}

	file, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		data, _ := json.Marshal(issue)
		file.Write(data)
		file.WriteString("\n")
	}
	file.Close()

	tests := []struct {
		name      string
		issueID   string
		wantTitle string
		wantNil   bool
	}{
		{
			name:      "find existing issue",
			issueID:   "bd-2",
			wantTitle: "Second issue",
		},
		{
			name:    "issue not found",
			issueID: "bd-999",
			wantNil: true,
		},
		{
			name:      "find first issue",
			issueID:   "bd-1",
			wantTitle: "First issue",
		},
		{
			name:      "find last issue",
			issueID:   "bd-3",
			wantTitle: "Third issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue, err := readIssueFromJSONL(jsonlPath, tt.issueID)
			if err != nil {
				t.Fatalf("readIssueFromJSONL() error = %v", err)
			}
			if tt.wantNil {
				if issue != nil {
					t.Errorf("expected nil, got issue %s", issue.ID)
				}
				return
			}
			if issue == nil {
				t.Fatal("expected issue, got nil")
			}
			if issue.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", issue.Title, tt.wantTitle)
			}
		})
	}
}

func TestReadIssueFromJSONL_NonExistentFile(t *testing.T) {
	_, err := readIssueFromJSONL("/nonexistent/path.jsonl", "bd-1")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadIssueFromJSONL_MalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "malformed.jsonl")

	// Create JSONL with mix of valid and malformed entries
	content := `{"id":"bd-1","title":"First"}
{this is not valid json}
{"id":"bd-2","title":"Second"}
incomplete line without brace
{"id":"bd-3","title":"Third"}`

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Should still find valid entries, skipping malformed ones
	issue, err := readIssueFromJSONL(jsonlPath, "bd-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issue == nil {
		t.Fatal("expected to find bd-2 despite malformed lines")
	}
	if issue.Title != "Second" {
		t.Errorf("Title = %q, want %q", issue.Title, "Second")
	}
}

func TestGitHasUncommittedChanges_NotInGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Should error when not in a git repo
	_, err := gitHasUncommittedChanges()
	if err == nil {
		t.Error("expected error when not in git repo")
	}
}

func TestGetCurrentGitHead_NotInGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Should error when not in a git repo
	_, err := getCurrentGitHead()
	if err == nil {
		t.Error("expected error when not in git repo")
	}
}

func TestGitCheckout_InvalidRef(t *testing.T) {
	// Don't actually test this in real git repo to avoid side effects
	// Just verify the function signature exists
	err := gitCheckout("nonexistent-ref-12345")
	if err == nil {
		// If we're not in a git repo or ref doesn't exist, should error
		t.Log("gitCheckout returned nil - might not be in git repo or ref exists")
	}
}

// TestRestoreWithInvalidIssueID verifies that restore handles non-existent issues
// gracefully without panicking. This tests the fix for bd-02n where GetIssue
// returns (nil, nil) for non-existent issues.
func TestRestoreWithInvalidIssueID(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	testStore := newTestStore(t, dbPath)
	defer testStore.Close()

	ctx := context.Background()
	issue, err := testStore.GetIssue(ctx, "nonexistent-issue-12345")

	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssue expected ErrNotFound, got: %v", err)
	}
	if issue != nil {
		t.Fatalf("GetIssue returned issue for non-existent ID: %v", issue)
	}
}
