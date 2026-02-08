package sqlite

import (
	"context"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestChildCountersTableExists(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Verify table exists by querying it
	var count int
	err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='child_counters'`).Scan(&count)
	if err != nil {
		t.Fatalf("failed to check for child_counters table: %v", err)
	}

	if count != 1 {
		t.Errorf("child_counters table not found, got count %d", count)
	}
}

func TestGetNextChildNumber(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	parentID := "bd-af78e9a2"

	// Create parent issue first (required by foreign key)
	parent := &types.Issue{
		ID:          parentID,
		Title:       "Parent epic",
		Description: "Test parent",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test-user"); err != nil {
		t.Fatalf("failed to create parent issue: %v", err)
	}

	// First child should be 1
	child1, err := store.getNextChildNumber(ctx, parentID)
	if err != nil {
		t.Fatalf("getNextChildNumber failed: %v", err)
	}
	if child1 != 1 {
		t.Errorf("expected first child to be 1, got %d", child1)
	}

	// Second child should be 2
	child2, err := store.getNextChildNumber(ctx, parentID)
	if err != nil {
		t.Fatalf("getNextChildNumber failed: %v", err)
	}
	if child2 != 2 {
		t.Errorf("expected second child to be 2, got %d", child2)
	}

	// Third child should be 3
	child3, err := store.getNextChildNumber(ctx, parentID)
	if err != nil {
		t.Fatalf("getNextChildNumber failed: %v", err)
	}
	if child3 != 3 {
		t.Errorf("expected third child to be 3, got %d", child3)
	}
}

func TestGetNextChildNumber_DifferentParents(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	parent1 := "bd-af78e9a2"
	parent2 := "bd-bf89e0b3" // Use non-hierarchical ID to avoid counter interaction

	// Create parent issues first
	for _, id := range []string{parent1, parent2} {
		parent := &types.Issue{
			ID:          id,
			Title:       "Parent " + id,
			Description: "Test parent",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
		}
		if err := store.CreateIssue(ctx, parent, "test-user"); err != nil {
			t.Fatalf("failed to create parent issue %s: %v", id, err)
		}
	}

	// Each parent should have independent counters
	child1_1, err := store.getNextChildNumber(ctx, parent1)
	if err != nil {
		t.Fatalf("getNextChildNumber failed: %v", err)
	}
	if child1_1 != 1 {
		t.Errorf("expected parent1 child to be 1, got %d", child1_1)
	}

	child2_1, err := store.getNextChildNumber(ctx, parent2)
	if err != nil {
		t.Fatalf("getNextChildNumber failed: %v", err)
	}
	if child2_1 != 1 {
		t.Errorf("expected parent2 child to be 1, got %d", child2_1)
	}

	child1_2, err := store.getNextChildNumber(ctx, parent1)
	if err != nil {
		t.Fatalf("getNextChildNumber failed: %v", err)
	}
	if child1_2 != 2 {
		t.Errorf("expected parent1 second child to be 2, got %d", child1_2)
	}
}

func TestGetNextChildNumber_Concurrent(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	parentID := "bd-af78e9a2"
	numWorkers := 10

	// Create parent issue first
	parent := &types.Issue{
		ID:          parentID,
		Title:       "Parent epic",
		Description: "Test parent",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "test-user"); err != nil {
		t.Fatalf("failed to create parent issue: %v", err)
	}

	// Track all generated child numbers
	childNumbers := make([]int, numWorkers)
	var wg sync.WaitGroup

	// Spawn concurrent workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			child, err := store.getNextChildNumber(ctx, parentID)
			if err != nil {
				t.Errorf("concurrent getNextChildNumber failed: %v", err)
				return
			}
			childNumbers[idx] = child
		}(i)
	}

	wg.Wait()

	// Verify all numbers are unique and in range [1, numWorkers]
	seen := make(map[int]bool)
	for _, num := range childNumbers {
		if num < 1 || num > numWorkers {
			t.Errorf("child number %d out of expected range [1, %d]", num, numWorkers)
		}
		if seen[num] {
			t.Errorf("duplicate child number: %d", num)
		}
		seen[num] = true
	}

	// Verify we got all numbers from 1 to numWorkers
	if len(seen) != numWorkers {
		t.Errorf("expected %d unique child numbers, got %d", numWorkers, len(seen))
	}
}

func TestGetNextChildNumber_NestedHierarchy(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create parent issues for nested hierarchy
	// Note: When creating bd-af78e9a2.1, the counter for bd-af78e9a2 is set to 1 (GH#728 fix)
	// When creating bd-af78e9a2.1.2, the counter for bd-af78e9a2.1 is set to 2 (GH#728 fix)
	parents := []string{"bd-af78e9a2", "bd-af78e9a2.1", "bd-af78e9a2.1.2"}
	for _, id := range parents {
		parent := &types.Issue{
			ID:          id,
			Title:       "Parent " + id,
			Description: "Test parent",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
		}
		if err := store.CreateIssue(ctx, parent, "test-user"); err != nil {
			t.Fatalf("failed to create parent issue %s: %v", id, err)
		}
	}

	// With GH#728 fix, counters are updated when explicit hierarchical IDs are created:
	// - Creating bd-af78e9a2.1 sets counter for bd-af78e9a2 to 1
	// - Creating bd-af78e9a2.1.2 sets counter for bd-af78e9a2.1 to 2
	// So getNextChildNumber returns the NEXT number after the existing children

	tests := []struct {
		parent   string
		expected []int
	}{
		{"bd-af78e9a2", []int{2, 3}},     // counter was 1 after creating .1
		{"bd-af78e9a2.1", []int{3, 4}},   // counter was 2 after creating .1.2
		{"bd-af78e9a2.1.2", []int{1, 2}}, // no children created, starts at 1
	}

	for _, tt := range tests {
		for _, want := range tt.expected {
			got, err := store.getNextChildNumber(ctx, tt.parent)
			if err != nil {
				t.Fatalf("getNextChildNumber(%s) failed: %v", tt.parent, err)
			}
			if got != want {
				t.Errorf("parent %s: expected child %d, got %d", tt.parent, want, got)
			}
		}
	}
}
