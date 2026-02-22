package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestSearchIssues_MetadataFieldMatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	issue1 := &types.Issue{
		Title:     "Platform issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"platform","sprint":"Q1"}`),
	}
	issue2 := &types.Issue{
		Title:     "Frontend issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"frontend","sprint":"Q1"}`),
	}
	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Search for team=platform → should find only issue1
	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MetadataFields: map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != issue1.ID {
		t.Errorf("expected issue %s, got %s", issue1.ID, results[0].ID)
	}
}

func TestSearchIssues_MetadataFieldNoMatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Platform issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"platform"}`),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MetadataFields: map[string]string{"team": "backend"},
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSearchIssues_HasMetadataKey(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	issue1 := &types.Issue{
		Title:     "Has team key",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"platform"}`),
	}
	issue2 := &types.Issue{
		Title:     "No metadata",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		HasMetadataKey: "team",
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != issue1.ID {
		t.Errorf("expected issue %s, got %s", issue1.ID, results[0].ID)
	}
}

func TestSearchIssues_MultipleMetadataFieldsANDed(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	issue1 := &types.Issue{
		Title:     "Both match",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"platform","sprint":"Q1"}`),
	}
	issue2 := &types.Issue{
		Title:     "Partial match",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"platform","sprint":"Q2"}`),
	}
	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MetadataFields: map[string]string{
			"team":   "platform",
			"sprint": "Q1",
		},
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != issue1.ID {
		t.Errorf("expected issue %s, got %s", issue1.ID, results[0].ID)
	}
}

func TestSearchIssues_MetadataFieldInvalidKey(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	_, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MetadataFields: map[string]string{"'; DROP TABLE issues; --": "val"},
	})
	if err == nil {
		t.Fatal("expected error for invalid metadata key, got nil")
	}
}

func TestSearchIssues_HasMetadataKeyInvalidKey(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	_, err := store.SearchIssues(ctx, "", types.IssueFilter{
		HasMetadataKey: "bad key!",
	})
	if err == nil {
		t.Fatal("expected error for invalid metadata key, got nil")
	}
}

func TestSearchIssues_NoMetadataDoesNotMatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "No metadata",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MetadataFields: map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for issue without metadata, got %d", len(results))
	}
}

// Key validation unit tests (don't need a store)

func TestValidateMetadataKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key     string
		wantErr bool
	}{
		{"team", false},
		{"story_points", false},
		{"jira.sprint", false},
		{"_private", false},
		{"CamelCase", false},
		{"a1b2c3", false},
		{"", true},
		{"bad key", true},
		{"bad-key", true},       // hyphens not allowed
		{"123start", true},      // must start with letter/underscore
		{"key=value", true},     // equals not allowed
		{"'; DROP TABLE", true}, // SQL injection
		{"$.path", true},        // JSON path chars not allowed
		{"key\nvalue", true},    // newlines not allowed
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			err := storage.ValidateMetadataKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMetadataKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}
