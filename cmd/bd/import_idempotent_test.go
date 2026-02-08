package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestIssueDataChanged tests the issueDataChanged helper function
func TestIssueDataChanged(t *testing.T) {
	tests := []struct {
		name     string
		existing *types.Issue
		updates  map[string]interface{}
		want     bool // true if changed, false if unchanged
	}{
		{
			name: "no changes",
			existing: &types.Issue{
				Title:       "Test",
				Description: "Desc",
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
			},
			updates: map[string]interface{}{
				"title":       "Test",
				"description": "Desc",
				"status":      types.StatusOpen,
				"priority":    2,
				"issue_type":  types.TypeTask,
			},
			want: false,
		},
		{
			name: "title changed",
			existing: &types.Issue{
				Title: "Old Title",
			},
			updates: map[string]interface{}{
				"title": "New Title",
			},
			want: true,
		},
		{
			name: "status as string vs enum - unchanged",
			existing: &types.Issue{
				Status: types.StatusOpen,
			},
			updates: map[string]interface{}{
				"status": "open", // string instead of enum
			},
			want: false,
		},
		{
			name: "status as enum - unchanged",
			existing: &types.Issue{
				Status: types.StatusInProgress,
			},
			updates: map[string]interface{}{
				"status": types.StatusInProgress, // enum
			},
			want: false,
		},
		{
			name: "issue_type as string vs enum - unchanged",
			existing: &types.Issue{
				IssueType: types.TypeBug,
			},
			updates: map[string]interface{}{
				"issue_type": "bug", // string instead of enum
			},
			want: false,
		},
		{
			name: "priority as int - unchanged",
			existing: &types.Issue{
				Priority: 3,
			},
			updates: map[string]interface{}{
				"priority": 3,
			},
			want: false,
		},
		{
			name: "priority as int64 - unchanged",
			existing: &types.Issue{
				Priority: 2,
			},
			updates: map[string]interface{}{
				"priority": int64(2),
			},
			want: false,
		},
		{
			name: "priority as float64 whole number - unchanged",
			existing: &types.Issue{
				Priority: 1,
			},
			updates: map[string]interface{}{
				"priority": float64(1),
			},
			want: false,
		},
		{
			name: "priority as float64 fractional - changed",
			existing: &types.Issue{
				Priority: 1,
			},
			updates: map[string]interface{}{
				"priority": 1.5, // fractional not allowed
			},
			want: true,
		},
		{
			name: "empty string vs empty - unchanged",
			existing: &types.Issue{
				Design: "",
			},
			updates: map[string]interface{}{
				"design": "",
			},
			want: false,
		},
		{
			name: "empty string vs nil - unchanged (treated as equal)",
			existing: &types.Issue{
				Assignee: "",
			},
			updates: map[string]interface{}{
				"assignee": nil,
			},
			want: false,
		},
		{
			name: "non-empty to empty - changed",
			existing: &types.Issue{
				Notes: "Some notes",
			},
			updates: map[string]interface{}{
				"notes": "",
			},
			want: true,
		},
		{
			name: "external_ref nil vs empty - unchanged",
			existing: &types.Issue{
				ExternalRef: nil,
			},
			updates: map[string]interface{}{
				"external_ref": nil,
			},
			want: false,
		},
		{
			name: "external_ref pointer to empty vs nil - unchanged",
			existing: &types.Issue{
				ExternalRef: strPtr(""),
			},
			updates: map[string]interface{}{
				"external_ref": nil,
			},
			want: false,
		},
		{
			name: "external_ref changed",
			existing: &types.Issue{
				ExternalRef: strPtr("gh-123"),
			},
			updates: map[string]interface{}{
				"external_ref": "gh-456",
			},
			want: true,
		},
		{
			name: "unknown field - treated as changed",
			existing: &types.Issue{
				Title: "Test",
			},
			updates: map[string]interface{}{
				"title":         "Test",
				"unknown_field": "value",
			},
			want: true,
		},
		{
			name: "invalid type for title - treated as changed",
			existing: &types.Issue{
				Title: "Test",
			},
			updates: map[string]interface{}{
				"title": 123, // wrong type
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := issueDataChanged(tt.existing, tt.updates)
			if got != tt.want {
				t.Errorf("issueDataChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

// strPtr helper for tests
func strPtr(s string) *string {
	return &s
}

// TestIdempotentImportNoTimestampChurn verifies that importing unchanged issues
// does not update their timestamps (bd-84)
func TestIdempotentImportNoTimestampChurn(t *testing.T) {
	// FIX: Initialize rootCtx for autoImportIfNewer (issue #355)
	testRootCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = testRootCtx
	defer func() { rootCtx = oldRootCtx }()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "bd-test-idempotent-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	ctx := context.Background()

	// Create an issue
	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Test Issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Get initial timestamp
	issue1, err := testStore.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	initialUpdatedAt := issue1.UpdatedAt

	// Export to JSONL
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(issue1); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Wait a bit to ensure timestamps would be different if updated
	time.Sleep(100 * time.Millisecond)

	// Import the same JSONL (should be idempotent)
	autoImportIfNewer()

	// Get issue again
	issue2, err := testStore.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("Failed to get issue after import: %v", err)
	}

	// Verify timestamp was NOT updated
	if !issue2.UpdatedAt.Equal(initialUpdatedAt) {
		t.Errorf("Import updated timestamp even though data unchanged!\n"+
			"Before: %v\nAfter:  %v",
			initialUpdatedAt, issue2.UpdatedAt)
	}
}

// TestImportMultipleUnchangedIssues verifies that importing multiple unchanged issues
// does not update any of their timestamps (bd-84)
func TestImportMultipleUnchangedIssues(t *testing.T) {
	// FIX: Initialize rootCtx for autoImportIfNewer (issue #355)
	testRootCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldRootCtx := rootCtx
	rootCtx = testRootCtx
	defer func() { rootCtx = oldRootCtx }()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "bd-test-changed-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath = filepath.Join(tmpDir, "test.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create store
	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	store = testStore
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	}()

	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{
		ID:          "bd-1",
		Title:       "Unchanged Issue",
		Description: "Will not change",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	issue2 := &types.Issue{
		ID:          "bd-2",
		Title:       "Changed Issue",
		Description: "Will change",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue 1: %v", err)
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue 2: %v", err)
	}

	// Get initial timestamps
	unchanged, _ := testStore.GetIssue(ctx, "bd-1")
	changed, _ := testStore.GetIssue(ctx, "bd-2")
	unchangedInitialTS := unchanged.UpdatedAt
	changedInitialTS := changed.UpdatedAt

	// Export both issues to JSONL (unchanged)
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(unchanged); err != nil {
		t.Fatalf("Failed to encode issue 1: %v", err)
	}
	if err := encoder.Encode(changed); err != nil {
		t.Fatalf("Failed to encode issue 2: %v", err)
	}
	f.Close()

	// Wait to ensure timestamps would differ if updated
	time.Sleep(100 * time.Millisecond)

	// Import same JSONL (both issues unchanged - should be idempotent)
	autoImportIfNewer()

	// Check timestamps - neither should have changed
	issue1After, _ := testStore.GetIssue(ctx, "bd-1")
	issue2After, _ := testStore.GetIssue(ctx, "bd-2")

	// bd-1 should have same timestamp
	if !issue1After.UpdatedAt.Equal(unchangedInitialTS) {
		t.Errorf("bd-1 timestamp changed even though issue unchanged!\n"+
			"Before: %v\nAfter:  %v",
			unchangedInitialTS, issue1After.UpdatedAt)
	}

	// bd-2 should also have same timestamp
	if !issue2After.UpdatedAt.Equal(changedInitialTS) {
		t.Errorf("bd-2 timestamp changed even though issue unchanged!\n"+
			"Before: %v\nAfter:  %v",
			changedInitialTS, issue2After.UpdatedAt)
	}
}
