package sqlite

import (
	"context"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/config"
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
	expectedErr := "parent issue bd-nonexistent does not exist"
	if err != nil && err.Error() != expectedErr {
		t.Errorf("unexpected error message: got %q, want %q", err.Error(), expectedErr)
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

// TestExplicitChildIDUpdatesCounter verifies that creating issues with explicit
// hierarchical IDs (e.g., bd-test.1, bd-test.2) updates the child counter so that
// GetNextChildID returns the correct next ID (GH#728 fix)
func TestExplicitChildIDUpdatesCounter(t *testing.T) {
	tmpFile := t.TempDir() + "/test.db"
	defer os.Remove(tmpFile)
	store := newTestStore(t, tmpFile)
	defer store.Close()
	ctx := context.Background()

	// Create parent
	parent := &types.Issue{
		ID:          "bd-test",
		Title:       "Parent",
		Description: "Test parent",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent: %v", err)
	}

	// Create explicit child .1
	child1 := &types.Issue{
		ID:          "bd-test.1",
		Title:       "Existing child 1",
		Description: "Created with explicit ID",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child1, "test"); err != nil {
		t.Fatalf("failed to create child1: %v", err)
	}

	// Create explicit child .2
	child2 := &types.Issue{
		ID:          "bd-test.2",
		Title:       "Existing child 2",
		Description: "Created with explicit ID",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child2, "test"); err != nil {
		t.Fatalf("failed to create child2: %v", err)
	}

	// Now use GetNextChildID - should return .3 (not .1 which would collide)
	nextID, err := store.GetNextChildID(ctx, "bd-test")
	if err != nil {
		t.Fatalf("GetNextChildID failed: %v", err)
	}

	expected := "bd-test.3"
	if nextID != expected {
		t.Errorf("GetNextChildID returned %s, expected %s (GH#728 - counter should be updated when explicit child IDs are created)", nextID, expected)
	}

	// Verify we can create an issue with the returned ID without collision
	child3 := &types.Issue{
		ID:          nextID,
		Title:       "New child via --parent",
		Description: "Created with GetNextChildID",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child3, "test"); err != nil {
		t.Fatalf("failed to create child3 with ID %s: %v", nextID, err)
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
	expectedErr := "parent issue bd-nonexistent does not exist"
	if err != nil && err.Error() != expectedErr {
		t.Errorf("unexpected error message: got %q, want %q", err.Error(), expectedErr)
	}
}


// TestGetNextChildID_ConfigurableMaxDepth tests that hierarchy.max-depth config is respected (GH#995)
func TestGetNextChildID_ConfigurableMaxDepth(t *testing.T) {
	// Initialize config for testing
	if err := config.Initialize(); err != nil {
		t.Fatalf("failed to initialize config: %v", err)
	}

	// Ensure config is reset even if test fails or panics
	t.Cleanup(func() {
		config.Set("hierarchy.max-depth", 3)
	})

	tmpFile := t.TempDir() + "/test.db"
	defer os.Remove(tmpFile)
	store := newTestStore(t, tmpFile)
	defer store.Close()
	ctx := context.Background()

	// Create a chain of issues up to depth 3
	issues := []struct {
		id    string
		title string
	}{
		{"bd-depth", "Root"},
		{"bd-depth.1", "Level 1"},
		{"bd-depth.1.1", "Level 2"},
		{"bd-depth.1.1.1", "Level 3"},
	}

	for _, issue := range issues {
		iss := &types.Issue{
			ID:          issue.id,
			Title:       issue.title,
			Description: "Test issue",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.id, err)
		}
	}

	// Test 1: With default max-depth (3), depth 4 should fail
	config.Set("hierarchy.max-depth", 3)
	_, err := store.GetNextChildID(ctx, "bd-depth.1.1.1")
	if err == nil {
		t.Errorf("expected error for depth 4 with max-depth=3, got nil")
	}
	if err != nil && err.Error() != "maximum hierarchy depth (3) exceeded for parent bd-depth.1.1.1" {
		t.Errorf("unexpected error message: %v", err)
	}

	// Test 2: With max-depth=5, depth 4 should succeed
	config.Set("hierarchy.max-depth", 5)
	childID, err := store.GetNextChildID(ctx, "bd-depth.1.1.1")
	if err != nil {
		t.Errorf("depth 4 should be allowed with max-depth=5, got error: %v", err)
	}
	expectedID := "bd-depth.1.1.1.1"
	if childID != expectedID {
		t.Errorf("expected %s, got %s", expectedID, childID)
	}

	// Test 3: With max-depth=2, depth 3 should fail
	config.Set("hierarchy.max-depth", 2)
	_, err = store.GetNextChildID(ctx, "bd-depth.1.1")
	if err == nil {
		t.Errorf("expected error for depth 3 with max-depth=2, got nil")
	}
	if err != nil && err.Error() != "maximum hierarchy depth (2) exceeded for parent bd-depth.1.1" {
		t.Errorf("unexpected error message: %v", err)
	}
}

