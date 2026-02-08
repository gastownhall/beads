package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestTryResurrectParent_AlreadyExists verifies that resurrection is a no-op when parent exists
func TestTryResurrectParent_AlreadyExists(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create parent issue
	parent := &types.Issue{
		ID:        "bd-abc",
		Title:     "Parent Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	// Try to resurrect - should succeed without doing anything
	resurrected, err := s.TryResurrectParent(ctx, "bd-abc")
	if err != nil {
		t.Fatalf("TryResurrectParent failed: %v", err)
	}
	if !resurrected {
		t.Fatal("Expected resurrected=true for existing parent")
	}

	// Verify parent is still the original (not a tombstone)
	retrieved, err := s.GetIssue(ctx, "bd-abc")
	if err != nil {
		t.Fatalf("Failed to retrieve parent: %v", err)
	}
	if retrieved.Status != types.StatusOpen {
		t.Errorf("Expected status=%s, got %s", types.StatusOpen, retrieved.Status)
	}
	if retrieved.Priority != 1 {
		t.Errorf("Expected priority=1, got %d", retrieved.Priority)
	}
}

// TestTryResurrectParent_FoundInJSONL verifies successful resurrection from JSONL
func TestTryResurrectParent_FoundInJSONL(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create a JSONL file with the parent issue
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	parentIssue := types.Issue{
		ID:          "test-parent",
		ContentHash: "hash123",
		Title:       "Original Parent",
		Description: "Original description text",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	}

	// Write parent to JSONL
	if err := writeIssuesToJSONL(jsonlPath, []types.Issue{parentIssue}); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Try to resurrect
	resurrected, err := s.TryResurrectParent(ctx, "test-parent")
	if err != nil {
		t.Fatalf("TryResurrectParent failed: %v", err)
	}
	if !resurrected {
		t.Fatal("Expected successful resurrection")
	}

	// Verify tombstone was created
	tombstone, err := s.GetIssue(ctx, "test-parent")
	if err != nil {
		t.Fatalf("Failed to retrieve resurrected parent: %v", err)
	}

	// Check tombstone properties
	if tombstone.Status != types.StatusClosed {
		t.Errorf("Expected status=closed, got %s", tombstone.Status)
	}
	if tombstone.Priority != 4 {
		t.Errorf("Expected priority=4, got %d", tombstone.Priority)
	}
	if tombstone.ClosedAt == nil {
		t.Error("Expected ClosedAt to be set")
	}
	if tombstone.Title != "Original Parent" {
		t.Errorf("Expected title preserved, got %s", tombstone.Title)
	}
	if !contains(tombstone.Description, "[RESURRECTED]") {
		t.Error("Expected [RESURRECTED] marker in description")
	}
	if !contains(tombstone.Description, "Original description text") {
		t.Error("Expected original description appended to tombstone")
	}
	if tombstone.IssueType != types.TypeEpic {
		t.Errorf("Expected type=%s, got %s", types.TypeEpic, tombstone.IssueType)
	}
	if !tombstone.CreatedAt.Equal(parentIssue.CreatedAt) {
		t.Error("Expected CreatedAt to be preserved from original")
	}
}

// TestTryResurrectParent_NotFoundInJSONL verifies proper handling when parent not in JSONL
func TestTryResurrectParent_NotFoundInJSONL(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create a JSONL file with different issue
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	otherIssue := types.Issue{
		ID:        "test-other",
		Title:     "Other Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := writeIssuesToJSONL(jsonlPath, []types.Issue{otherIssue}); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Try to resurrect non-existent parent
	resurrected, err := s.TryResurrectParent(ctx, "test-missing")
	if err != nil {
		t.Fatalf("TryResurrectParent should not error on missing parent: %v", err)
	}
	if resurrected {
		t.Error("Expected resurrected=false for missing parent")
	}

	// Verify parent was not created
	issue, err := s.GetIssue(ctx, "test-missing")
	if err == nil && issue != nil {
		t.Error("Expected nil issue when retrieving non-existent parent")
	}
}

// TestTryResurrectParent_NoJSONLFile verifies graceful handling when JSONL file missing
func TestTryResurrectParent_NoJSONLFile(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Don't create JSONL file

	// Try to resurrect - should return false (not found) without error
	resurrected, err := s.TryResurrectParent(ctx, "test-parent")
	if err != nil {
		t.Fatalf("TryResurrectParent should not error when JSONL missing: %v", err)
	}
	if resurrected {
		t.Error("Expected resurrected=false when JSONL missing")
	}
}

// TestTryResurrectParent_MalformedJSONL verifies handling of malformed JSONL lines
func TestTryResurrectParent_MalformedJSONL(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create JSONL file with malformed lines and one valid entry
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	validIssue := types.Issue{
		ID:        "test-valid",
		Title:     "Valid Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	validJSON, _ := json.Marshal(validIssue)

	content := "this is not valid json\n" +
		"{\"id\": \"incomplete\"\n" +
		string(validJSON) + "\n" +
		"\n" // empty line

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Try to resurrect valid issue - should succeed despite malformed lines
	resurrected, err := s.TryResurrectParent(ctx, "test-valid")
	if err != nil {
		t.Fatalf("TryResurrectParent failed: %v", err)
	}
	if !resurrected {
		t.Error("Expected successful resurrection of valid issue")
	}

	// Try to resurrect from malformed line - should return false
	resurrected, err = s.TryResurrectParent(ctx, "incomplete")
	if err != nil {
		t.Fatalf("TryResurrectParent should not error on malformed JSON: %v", err)
	}
	if resurrected {
		t.Error("Expected resurrected=false for malformed JSON")
	}
}

// TestTryResurrectParent_WithDependencies verifies dependency resurrection
func TestTryResurrectParent_WithDependencies(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create dependency target in database
	target := &types.Issue{
		ID:        "bd-target",
		Title:     "Target Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, target, "test"); err != nil {
		t.Fatalf("Failed to create target: %v", err)
	}

	// Create JSONL with parent that has dependencies
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	parentIssue := types.Issue{
		ID:        "test-parent",
		Title:     "Parent with Deps",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Dependencies: []*types.Dependency{
			{IssueID: "test-parent", DependsOnID: "bd-target", Type: types.DepBlocks},
			{IssueID: "test-parent", DependsOnID: "test-missing", Type: types.DepBlocks},
		},
	}

	if err := writeIssuesToJSONL(jsonlPath, []types.Issue{parentIssue}); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Resurrect parent
	resurrected, err := s.TryResurrectParent(ctx, "test-parent")
	if err != nil {
		t.Fatalf("TryResurrectParent failed: %v", err)
	}
	if !resurrected {
		t.Fatal("Expected successful resurrection")
	}

	// Verify dependency to existing target was resurrected
	_, err = s.GetIssue(ctx, "test-parent")
	if err != nil {
		t.Fatalf("Failed to retrieve tombstone: %v", err)
	}

	// Get dependencies separately (GetIssue doesn't load them)
	depIssues, err := s.GetDependencies(ctx, "test-parent")
	if err != nil {
		t.Fatalf("Failed to get dependencies: %v", err)
	}
	if len(depIssues) != 1 {
		t.Fatalf("Expected 1 dependency (only the valid one), got %d", len(depIssues))
	}
	if depIssues[0].ID != "bd-target" {
		t.Errorf("Expected dependency to bd-target, got %s", depIssues[0].ID)
	}
}

// TestTryResurrectParentChain_MultiLevel verifies recursive chain resurrection
func TestTryResurrectParentChain_MultiLevel(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create JSONL with multi-level hierarchy
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	root := types.Issue{
		ID:        "test-root",
		Title:     "Root Epic",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	level1 := types.Issue{
		ID:        "test-root.1",
		Title:     "Level 1 Task",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	level2 := types.Issue{
		ID:        "test-root.1.1",
		Title:     "Level 2 Subtask",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := writeIssuesToJSONL(jsonlPath, []types.Issue{root, level1, level2}); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Resurrect entire chain for deepest child
	resurrected, err := s.TryResurrectParentChain(ctx, "test-root.1.1")
	if err != nil {
		t.Fatalf("TryResurrectParentChain failed: %v", err)
	}
	if !resurrected {
		t.Fatal("Expected successful chain resurrection")
	}

	// Verify all parents were created
	for _, id := range []string{"test-root", "test-root.1"} {
		issue, err := s.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("Failed to retrieve %s: %v", id, err)
			continue
		}
		if issue.Status != types.StatusClosed {
			t.Errorf("Expected %s to be closed tombstone, got %s", id, issue.Status)
		}
		if !contains(issue.Description, "[RESURRECTED]") {
			t.Errorf("Expected %s to have [RESURRECTED] marker", id)
		}
	}
}

// TestTryResurrectParentChain_PartialChainMissing verifies behavior when some parents missing
func TestTryResurrectParentChain_PartialChainMissing(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create JSONL with only root, missing intermediate level
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	root := types.Issue{
		ID:        "test-root",
		Title:     "Root Epic",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Note: test-root.1 is NOT in JSONL

	if err := writeIssuesToJSONL(jsonlPath, []types.Issue{root}); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Try to resurrect chain - should fail when intermediate parent not found
	resurrected, err := s.TryResurrectParentChain(ctx, "test-root.1.1")
	if err != nil {
		t.Fatalf("TryResurrectParentChain should not error: %v", err)
	}
	if resurrected {
		t.Error("Expected resurrected=false when intermediate parent missing")
	}

	// Verify root was created (first in chain)
	rootIssue, err := s.GetIssue(ctx, "test-root")
	if err != nil {
		t.Error("Expected root to be resurrected before failure")
	} else if rootIssue.Status != types.StatusClosed {
		t.Error("Expected root to be tombstone")
	}

	// Verify intermediate level was NOT created
	midIssue, err := s.GetIssue(ctx, "test-root.1")
	if err == nil && midIssue != nil {
		t.Error("Expected nil issue when retrieving missing intermediate parent")
	}
}

// TestExtractParentChain verifies parent chain extraction logic
func TestExtractParentChain(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected []string
	}{
		{
			name:     "top-level ID",
			id:       "test-abc",
			expected: nil,
		},
		{
			name:     "one level deep",
			id:       "test-abc.1",
			expected: []string{"test-abc"},
		},
		{
			name:     "two levels deep",
			id:       "test-abc.1.2",
			expected: []string{"test-abc", "test-abc.1"},
		},
		{
			name:     "three levels deep",
			id:       "test-abc.1.2.3",
			expected: []string{"test-abc", "test-abc.1", "test-abc.1.2"},
		},
		// GH#664: Prefixes with dots should be handled correctly
		{
			name:     "prefix with dot - top-level",
			id:       "test.example-abc",
			expected: nil, // No numeric suffix, not hierarchical
		},
		{
			name:     "prefix with dot - one level deep",
			id:       "test.example-abc.1",
			expected: []string{"test.example-abc"},
		},
		{
			name:     "prefix with dot - two levels deep",
			id:       "test.example-abc.1.2",
			expected: []string{"test.example-abc", "test.example-abc.1"},
		},
		{
			name:     "prefix with multiple dots - one level deep",
			id:       "my.company.project-xyz.1",
			expected: []string{"my.company.project-xyz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractParentChain(tt.id)
			if len(result) != len(tt.expected) {
				t.Fatalf("Expected %d parents, got %d", len(tt.expected), len(result))
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("Parent[%d]: expected %s, got %s", i, tt.expected[i], result[i])
				}
			}
		})
	}
}

// TestTryResurrectParent_Idempotent verifies resurrection can be called multiple times safely
func TestTryResurrectParent_Idempotent(t *testing.T) {
	s := newTestStore(t, "")

	ctx := context.Background()

	// Create JSONL with parent
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	parentIssue := types.Issue{
		ID:        "test-parent",
		Title:     "Parent Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := writeIssuesToJSONL(jsonlPath, []types.Issue{parentIssue}); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// First resurrection
	resurrected, err := s.TryResurrectParent(ctx, "test-parent")
	if err != nil {
		t.Fatalf("First resurrection failed: %v", err)
	}
	if !resurrected {
		t.Fatal("Expected first resurrection to succeed")
	}

	firstTombstone, err := s.GetIssue(ctx, "test-parent")
	if err != nil {
		t.Fatalf("Failed to retrieve first tombstone: %v", err)
	}

	// Second resurrection (should be no-op)
	resurrected, err = s.TryResurrectParent(ctx, "test-parent")
	if err != nil {
		t.Fatalf("Second resurrection failed: %v", err)
	}
	if !resurrected {
		t.Fatal("Expected second resurrection to succeed (already exists)")
	}

	// Verify tombstone unchanged
	secondTombstone, err := s.GetIssue(ctx, "test-parent")
	if err != nil {
		t.Fatalf("Failed to retrieve second tombstone: %v", err)
	}

	if firstTombstone.UpdatedAt != secondTombstone.UpdatedAt {
		t.Error("Expected tombstone to be unchanged by second resurrection")
	}
}

// Helper function to write issues to JSONL file
func writeIssuesToJSONL(path string, issues []types.Issue) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return err
		}
	}

	return nil
}

// TestTryResurrectParent_MultipleVersionsInJSONL verifies that the LAST occurrence is used
func TestTryResurrectParent_MultipleVersionsInJSONL(t *testing.T) {
	s := newTestStore(t, "")
	ctx := context.Background()

	// Create JSONL with multiple versions of the same issue (append-only semantics)
	dbDir := filepath.Dir(s.dbPath)
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	// First version: priority 3, title "Old Version"
	v1 := &types.Issue{
		ID:        "bd-multi",
		Title:     "Old Version",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	v1JSON, _ := json.Marshal(v1)

	// Second version: priority 2, title "Updated Version"
	time.Sleep(10 * time.Millisecond) // Ensure different timestamp
	v2 := &types.Issue{
		ID:        "bd-multi",
		Title:     "Updated Version",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: v1.CreatedAt, // Same creation time
		UpdatedAt: time.Now(),
	}
	v2JSON, _ := json.Marshal(v2)

	// Third version: priority 1, title "Latest Version"
	time.Sleep(10 * time.Millisecond)
	v3 := &types.Issue{
		ID:        "bd-multi",
		Title:     "Latest Version",
		Status:    types.StatusClosed,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: v1.CreatedAt,
		UpdatedAt: time.Now(),
	}
	v3JSON, _ := json.Marshal(v3)

	// Write all three versions (append-only)
	content := string(v1JSON) + "\n" + string(v2JSON) + "\n" + string(v3JSON) + "\n"
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}

	// Resurrect - should get the LAST version (v3)
	resurrected, err := s.TryResurrectParent(ctx, "bd-multi")
	if err != nil {
		t.Fatalf("TryResurrectParent failed: %v", err)
	}
	if !resurrected {
		t.Error("Expected successful resurrection")
	}

	// Verify we got the latest version's data
	retrieved, err := s.GetIssue(ctx, "bd-multi")
	if err != nil {
		t.Fatalf("Failed to retrieve resurrected issue: %v", err)
	}

	// Most important: title should be from LAST occurrence (v3)
	if retrieved.Title != "Latest Version" {
		t.Errorf("Expected title 'Latest Version', got '%s' (should use LAST occurrence in JSONL)", retrieved.Title)
	}

	// CreatedAt should be preserved from original (all versions share this)
	if !retrieved.CreatedAt.Equal(v1.CreatedAt) {
		t.Errorf("Expected CreatedAt %v, got %v", v1.CreatedAt, retrieved.CreatedAt)
	}

	// Note: Priority, Status, and Description are modified by tombstone logic
	// (Priority=4, Status=Closed, Description="[RESURRECTED]...")
	// This is expected behavior - the test verifies we read the LAST occurrence
	// before creating the tombstone.
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
