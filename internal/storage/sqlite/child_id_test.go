package sqlite

import (
	"context"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetNextChildID(t *testing.T) {
	tmpFile := t.TempDir() + "/test.db"
	defer os.Remove(tmpFile)
	store := newTestStore(t, tmpFile)
	defer store.Close()
	ctx := context.Background()

	// Create a parent issue with hash ID
	parent := &types.Issue{
		ID:          "bd-a3f8e9",
		Title:       "Parent Epic",
		Description: "Parent issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent: %v", err)
	}

	// Test: Generate first child ID
	childID1, err := store.GetNextChildID(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetNextChildID failed: %v", err)
	}
	expectedID1 := "bd-a3f8e9.1"
	if childID1 != expectedID1 {
		t.Errorf("expected %s, got %s", expectedID1, childID1)
	}

	// Test: Generate second child ID (sequential)
	childID2, err := store.GetNextChildID(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetNextChildID failed: %v", err)
	}
	expectedID2 := "bd-a3f8e9.2"
	if childID2 != expectedID2 {
		t.Errorf("expected %s, got %s", expectedID2, childID2)
	}

	// Create the first child and test nested hierarchy
	child1 := &types.Issue{
		ID:          childID1,
		Title:       "Child Task 1",
		Description: "First child",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child1, "test"); err != nil {
		t.Fatalf("failed to create child: %v", err)
	}

	// Test: Generate nested child (depth 2)
	nestedID1, err := store.GetNextChildID(ctx, childID1)
	if err != nil {
		t.Fatalf("GetNextChildID failed for nested: %v", err)
	}
	expectedNested1 := "bd-a3f8e9.1.1"
	if nestedID1 != expectedNested1 {
		t.Errorf("expected %s, got %s", expectedNested1, nestedID1)
	}

	// Create the nested child
	nested1 := &types.Issue{
		ID:          nestedID1,
		Title:       "Nested Task",
		Description: "Nested child",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, nested1, "test"); err != nil {
		t.Fatalf("failed to create nested child: %v", err)
	}

	// Test: Generate third level (depth 3, maximum)
	deepID1, err := store.GetNextChildID(ctx, nestedID1)
	if err != nil {
		t.Fatalf("GetNextChildID failed for depth 3: %v", err)
	}
	expectedDeep1 := "bd-a3f8e9.1.1.1"
	if deepID1 != expectedDeep1 {
		t.Errorf("expected %s, got %s", expectedDeep1, deepID1)
	}

	// Create the deep child
	deep1 := &types.Issue{
		ID:          deepID1,
		Title:       "Deep Task",
		Description: "Third level",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, deep1, "test"); err != nil {
		t.Fatalf("failed to create deep child: %v", err)
	}

	// Test: Attempt to create fourth level (should fail)
	_, err = store.GetNextChildID(ctx, deepID1)
	if err == nil {
		t.Errorf("expected error for depth 4, got nil")
	}
	if err != nil && err.Error() != "maximum hierarchy depth (3) exceeded for parent bd-a3f8e9.1.1.1" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGetNextChildID_ParentNotExists(t *testing.T) {
	tmpFile := t.TempDir() + "/test.db"
	defer os.Remove(tmpFile)
	store := newTestStore(t, tmpFile)
	defer store.Close()
	ctx := context.Background()

	// Test: Attempt to get child ID for non-existent parent
	_, err := store.GetNextChildID(ctx, "bd-nonexistent")
	if err == nil {
		t.Errorf("expected error for non-existent parent, got nil")
	}
	if err != nil && err.Error() != "parent issue bd-nonexistent does not exist" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCreateIssue_HierarchicalID(t *testing.T) {
	tmpFile := t.TempDir() + "/test.db"
	defer os.Remove(tmpFile)
	store := newTestStore(t, tmpFile)
	defer store.Close()
	ctx := context.Background()

	// Create parent
	parent := &types.Issue{
		ID:          "bd-parent1",
		Title:       "Parent",
		Description: "Parent issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent: %v", err)
	}

	// Test: Create child with explicit hierarchical ID
	child := &types.Issue{
		ID:          "bd-parent1.1",
		Title:       "Child",
		Description: "Child issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("failed to create child: %v", err)
	}

	// Verify child was created
	retrieved, err := store.GetIssue(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to retrieve child: %v", err)
	}
	if retrieved.ID != child.ID {
		t.Errorf("expected ID %s, got %s", child.ID, retrieved.ID)
	}
}

func TestCreateIssue_HierarchicalID_ParentNotExists(t *testing.T) {
	tmpFile := t.TempDir() + "/test.db"
	defer os.Remove(tmpFile)
	store := newTestStore(t, tmpFile)
	defer store.Close()
	ctx := context.Background()

	// Test: Attempt to create child without parent
	child := &types.Issue{
		ID:          "bd-nonexistent.1",
		Title:       "Child",
		Description: "Child issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	err := store.CreateIssue(ctx, child, "test")
	if err == nil {
		t.Errorf("expected error for child without parent, got nil")
	}
	if err != nil && err.Error() != "parent issue bd-nonexistent does not exist" {
		t.Errorf("unexpected error message: %v", err)
	}
}
