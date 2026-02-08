package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetIssueByExternalRef(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Create test issue with external_ref
	externalRef := "JIRA-123"
	issue := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Test issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,
	}

	err := s.CreateIssue(ctx, issue, "test")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Test: Find by external_ref
	found, err := s.GetIssueByExternalRef(ctx, externalRef)
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}

	if found == nil {
		t.Fatal("Expected to find issue by external_ref, got nil")
	}

	if found.ID != issue.ID {
		t.Errorf("Expected ID %s, got %s", issue.ID, found.ID)
	}

	if found.ExternalRef == nil || *found.ExternalRef != externalRef {
		t.Errorf("Expected external_ref %s, got %v", externalRef, found.ExternalRef)
	}
}

func TestGetIssueByExternalRefNotFound(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Test: Search for non-existent external_ref
	found, err := s.GetIssueByExternalRef(ctx, "NONEXISTENT-999")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef failed: %v", err)
	}

	if found != nil {
		t.Errorf("Expected nil for non-existent external_ref, got %v", found)
	}
}

func TestDetectCollisionsWithExternalRef(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Create existing issue with external_ref
	externalRef := "JIRA-456"
	existing := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Original title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,
	}

	err := s.CreateIssue(ctx, existing, "test")
	if err != nil {
		t.Fatalf("Failed to create existing issue: %v", err)
	}

	// Incoming issue with same external_ref but different ID and content
	incoming := &types.Issue{
		ID:          "bd-test-2", // Different ID
		Title:       "Updated title",
		Description: "Updated description",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeBug,
		ExternalRef: &externalRef,                  // Same external_ref
		UpdatedAt:   time.Now().Add(1 * time.Hour), // Newer timestamp
	}

	// Test: Detect collision by external_ref
	result, err := DetectCollisions(ctx, s, []*types.Issue{incoming})
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	// Should detect as collision (update needed)
	if len(result.Collisions) != 1 {
		t.Fatalf("Expected 1 collision, got %d", len(result.Collisions))
	}

	collision := result.Collisions[0]
	if collision.ExistingIssue.ID != existing.ID {
		t.Errorf("Expected existing issue ID %s, got %s", existing.ID, collision.ExistingIssue.ID)
	}

	if collision.IncomingIssue.ID != incoming.ID {
		t.Errorf("Expected incoming issue ID %s, got %s", incoming.ID, collision.IncomingIssue.ID)
	}

	// Should have conflicting fields
	expectedConflicts := []string{"title", "description", "status", "priority"}
	for _, field := range expectedConflicts {
		found := false
		for _, conflictField := range collision.ConflictingFields {
			if conflictField == field {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected conflict on field %s, but not found in %v", field, collision.ConflictingFields)
		}
	}
}

func TestDetectCollisionsExternalRefPriorityOverID(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Create existing issue with external_ref
	externalRef := "GH-789"
	existing := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Original title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeFeature,
		ExternalRef: &externalRef,
	}

	err := s.CreateIssue(ctx, existing, "test")
	if err != nil {
		t.Fatalf("Failed to create existing issue: %v", err)
	}

	// Create a second issue with a different ID and no external_ref
	otherIssue := &types.Issue{
		ID:          "bd-test-2",
		Title:       "Other issue",
		Description: "Other description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	err = s.CreateIssue(ctx, otherIssue, "test")
	if err != nil {
		t.Fatalf("Failed to create other issue: %v", err)
	}

	// Incoming issue with:
	// - Same external_ref as bd-test-1
	// - Same ID as bd-test-2
	// This tests that external_ref matching takes priority over ID matching
	incoming := &types.Issue{
		ID:          "bd-test-2", // Matches otherIssue.ID
		Title:       "Updated from external system",
		Description: "Updated description",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeFeature,
		ExternalRef: &externalRef, // Matches existing.ExternalRef
		UpdatedAt:   time.Now().Add(1 * time.Hour),
	}

	// Test: DetectCollisions should match by external_ref first
	result, err := DetectCollisions(ctx, s, []*types.Issue{incoming})
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	// Should match by external_ref, not ID
	if len(result.Collisions) != 1 {
		t.Fatalf("Expected 1 collision, got %d", len(result.Collisions))
	}

	collision := result.Collisions[0]

	// The existing issue matched should be bd-test-1 (by external_ref), not bd-test-2 (by ID)
	if collision.ExistingIssue.ID != existing.ID {
		t.Errorf("Expected external_ref match with %s, but got %s", existing.ID, collision.ExistingIssue.ID)
	}

	if collision.ExistingIssue.ExternalRef == nil || *collision.ExistingIssue.ExternalRef != externalRef {
		t.Errorf("Expected matched issue to have external_ref %s", externalRef)
	}
}

func TestDetectCollisionsNoExternalRef(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Create existing issue without external_ref
	existing := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Local issue",
		Description: "Local description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	err := s.CreateIssue(ctx, existing, "test")
	if err != nil {
		t.Fatalf("Failed to create existing issue: %v", err)
	}

	// Incoming issue with same ID but no external_ref
	incoming := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Updated local issue",
		Description: "Updated description",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeTask,
		UpdatedAt:   time.Now().Add(1 * time.Hour),
	}

	// Test: Should still match by ID when no external_ref
	result, err := DetectCollisions(ctx, s, []*types.Issue{incoming})
	if err != nil {
		t.Fatalf("DetectCollisions failed: %v", err)
	}

	if len(result.Collisions) != 1 {
		t.Fatalf("Expected 1 collision, got %d", len(result.Collisions))
	}

	collision := result.Collisions[0]
	if collision.ExistingIssue.ID != existing.ID {
		t.Errorf("Expected ID match with %s, got %s", existing.ID, collision.ExistingIssue.ID)
	}
}

func TestExternalRefIndex(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Verify that the external_ref index exists
	var indexExists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_master 
			WHERE type='index' AND name='idx_issues_external_ref'
		)
	`).Scan(&indexExists)

	if err != nil {
		t.Fatalf("Failed to check for index: %v", err)
	}

	if !indexExists {
		t.Error("Expected idx_issues_external_ref index to exist")
	}
}

func TestExternalRefIndexUsage(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()

	externalRef := "JIRA-123"
	issue := &types.Issue{
		ID:          "bd-test-1",
		Title:       "Test issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		ExternalRef: &externalRef,
	}

	err := s.CreateIssue(ctx, issue, "test")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		EXPLAIN QUERY PLAN
		SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee,
			created_at, updated_at, closed_at, external_ref,
			compaction_level, compacted_at, compacted_at_commit, original_size
		FROM issues
		WHERE external_ref = ?
	`, externalRef)
	if err != nil {
		t.Fatalf("Failed to get query plan: %v", err)
	}
	defer rows.Close()

	var planFound bool
	var indexUsed bool

	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("Failed to scan query plan row: %v", err)
		}
		planFound = true

		if detail == "SEARCH TABLE issues USING INDEX idx_issues_external_ref (external_ref=?)" ||
			detail == "SEARCH issues USING INDEX idx_issues_external_ref (external_ref=?)" ||
			detail == "SEARCH TABLE issues USING INDEX idx_issues_external_ref_unique (external_ref=?)" ||
			detail == "SEARCH issues USING INDEX idx_issues_external_ref_unique (external_ref=?)" {
			indexUsed = true
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("Error reading query plan: %v", err)
	}

	if !planFound {
		t.Error("Expected query plan output, got none")
	}

	if !indexUsed {
		t.Error("Expected query planner to use idx_issues_external_ref index, but it didn't")
	}
}
