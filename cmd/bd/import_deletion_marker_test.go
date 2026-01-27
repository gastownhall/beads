package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestIsDeletionMarker tests the isDeletionMarker function
func TestIsDeletionMarker(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantMarker bool
		wantID     string
	}{
		{
			name:       "valid deletion marker",
			input:      `{"id": "gt-123", "_deleted": true, "_deleted_at": "2026-01-17T10:30:00Z"}`,
			wantMarker: true,
			wantID:     "gt-123",
		},
		{
			name:       "deletion marker without timestamp",
			input:      `{"id": "bd-abc", "_deleted": true}`,
			wantMarker: true,
			wantID:     "bd-abc",
		},
		{
			name:       "not a deletion marker - _deleted is false",
			input:      `{"id": "gt-123", "_deleted": false}`,
			wantMarker: false,
		},
		{
			name:       "not a deletion marker - no _deleted field",
			input:      `{"id": "gt-123", "title": "Test Issue"}`,
			wantMarker: false,
		},
		{
			name:       "not a deletion marker - missing ID",
			input:      `{"_deleted": true}`,
			wantMarker: false,
		},
		{
			name:       "regular issue",
			input:      `{"id": "bd-xyz", "title": "Regular Issue", "status": "open", "priority": 2}`,
			wantMarker: false,
		},
		{
			name:       "invalid JSON",
			input:      `{invalid json`,
			wantMarker: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker, isMarker := isDeletionMarker([]byte(tt.input))
			if isMarker != tt.wantMarker {
				t.Errorf("isDeletionMarker() isMarker = %v, want %v", isMarker, tt.wantMarker)
			}
			if tt.wantMarker && marker != nil && marker.ID != tt.wantID {
				t.Errorf("isDeletionMarker() marker.ID = %v, want %v", marker.ID, tt.wantID)
			}
		})
	}
}

// TestDeletionMarkerSerialization tests that DeletionMarker can be properly marshaled/unmarshaled
func TestDeletionMarkerSerialization(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	marker := DeletionMarker{
		ID:        "test-123",
		Deleted:   true,
		DeletedAt: &now,
	}

	// Marshal
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("Failed to marshal DeletionMarker: %v", err)
	}

	// Unmarshal
	var unmarshaled DeletionMarker
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal DeletionMarker: %v", err)
	}

	// Verify
	if unmarshaled.ID != marker.ID {
		t.Errorf("ID mismatch: got %v, want %v", unmarshaled.ID, marker.ID)
	}
	if unmarshaled.Deleted != marker.Deleted {
		t.Errorf("Deleted mismatch: got %v, want %v", unmarshaled.Deleted, marker.Deleted)
	}
	if unmarshaled.DeletedAt == nil || !unmarshaled.DeletedAt.Equal(now) {
		t.Errorf("DeletedAt mismatch: got %v, want %v", unmarshaled.DeletedAt, marker.DeletedAt)
	}
}

// TestImportWithDeletionMarkers tests that deletion markers in JSONL are processed correctly
func TestImportWithDeletionMarkers(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "bd-deletion-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create database
	ctx := context.Background()
	dbPath := filepath.Join(tmpDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create some test issues
	issues := []*types.Issue{
		{
			ID:        "test-abc",
			Title:     "Issue to keep",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "test-def",
			Title:     "Issue to delete",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "test-ghi",
			Title:     "Another issue to delete",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Verify all 3 issues exist
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}
	if len(allIssues) != 3 {
		t.Fatalf("Expected 3 issues, got %d", len(allIssues))
	}

	// Now import with deletion markers for two of the issues
	opts := ImportOptions{
		DeletionIDs: []string{"test-def", "test-ghi"},
	}

	result, err := importIssuesCore(ctx, dbPath, store, nil, opts)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify deletion count
	if result.Deleted != 2 {
		t.Errorf("Expected 2 deletions, got %d", result.Deleted)
	}

	// Verify only one issue remains
	remainingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues after deletion: %v", err)
	}
	if len(remainingIssues) != 1 {
		t.Errorf("Expected 1 remaining issue, got %d", len(remainingIssues))
	}
	if remainingIssues[0].ID != "test-abc" {
		t.Errorf("Expected remaining issue to be test-abc, got %s", remainingIssues[0].ID)
	}
}

// TestImportDeletionMarkerDryRun tests that dry-run correctly counts deletions without actually deleting
func TestImportDeletionMarkerDryRun(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "bd-deletion-dryrun-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create database
	ctx := context.Background()
	dbPath := filepath.Join(tmpDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create a test issue
	issue := &types.Issue{
		ID:        "test-xyz",
		Title:     "Issue for dry-run test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Import with dry-run and deletion marker
	opts := ImportOptions{
		DryRun:      true,
		DeletionIDs: []string{"test-xyz"},
	}

	result, err := importIssuesCore(ctx, dbPath, store, nil, opts)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify dry-run reports the deletion
	if result.Deleted != 1 {
		t.Errorf("Expected 1 deletion in dry-run, got %d", result.Deleted)
	}

	// Verify issue still exists (dry-run didn't actually delete)
	remainingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("Failed to search issues: %v", err)
	}
	if len(remainingIssues) != 1 {
		t.Errorf("Expected issue to still exist after dry-run, got %d issues", len(remainingIssues))
	}
}
