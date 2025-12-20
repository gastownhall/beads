package linear

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestGenerateIssueIDs(t *testing.T) {
	// Create test issues without IDs
	issues := []*types.Issue{
		{
			Title:       "First issue",
			Description: "Description 1",
			CreatedAt:   time.Now(),
		},
		{
			Title:       "Second issue",
			Description: "Description 2",
			CreatedAt:   time.Now().Add(-time.Hour),
		},
		{
			Title:       "Third issue",
			Description: "Description 3",
			CreatedAt:   time.Now().Add(-2 * time.Hour),
		},
	}

	// Generate IDs
	err := GenerateIssueIDs(issues, "test", "linear-import", IDGenerationOptions{})
	if err != nil {
		t.Fatalf("GenerateIssueIDs failed: %v", err)
	}

	// Verify all issues have IDs
	for i, issue := range issues {
		if issue.ID == "" {
			t.Errorf("Issue %d has empty ID", i)
		}
		// Verify prefix
		if !hasPrefix(issue.ID, "test-") {
			t.Errorf("Issue %d ID '%s' doesn't have prefix 'test-'", i, issue.ID)
		}
	}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for i, issue := range issues {
		if seen[issue.ID] {
			t.Errorf("Duplicate ID found: %s (issue %d)", issue.ID, i)
		}
		seen[issue.ID] = true
	}
}

func TestGenerateIssueIDsPreservesExisting(t *testing.T) {
	existingID := "test-existing"
	issues := []*types.Issue{
		{
			ID:          existingID,
			Title:       "Existing issue",
			Description: "Has an ID already",
			CreatedAt:   time.Now(),
		},
		{
			Title:       "New issue",
			Description: "Needs an ID",
			CreatedAt:   time.Now(),
		},
	}

	err := GenerateIssueIDs(issues, "test", "linear-import", IDGenerationOptions{})
	if err != nil {
		t.Fatalf("GenerateIssueIDs failed: %v", err)
	}

	// First issue should keep its original ID
	if issues[0].ID != existingID {
		t.Errorf("Existing ID was changed: got %s, want %s", issues[0].ID, existingID)
	}

	// Second issue should have a new ID
	if issues[1].ID == "" {
		t.Error("Second issue has empty ID")
	}
	if issues[1].ID == existingID {
		t.Error("Second issue has same ID as first (collision)")
	}
}

func TestGenerateIssueIDsNoDuplicates(t *testing.T) {
	// Create issues with identical content - should still get unique IDs
	now := time.Now()
	issues := []*types.Issue{
		{
			Title:       "Same title",
			Description: "Same description",
			CreatedAt:   now,
		},
		{
			Title:       "Same title",
			Description: "Same description",
			CreatedAt:   now,
		},
	}

	err := GenerateIssueIDs(issues, "bd", "linear-import", IDGenerationOptions{})
	if err != nil {
		t.Fatalf("GenerateIssueIDs failed: %v", err)
	}

	// Both should have IDs
	if issues[0].ID == "" || issues[1].ID == "" {
		t.Error("One or both issues have empty IDs")
	}

	// IDs should be different (nonce handles collision)
	if issues[0].ID == issues[1].ID {
		t.Errorf("Both issues have same ID: %s", issues[0].ID)
	}
}

func TestNormalizeIssueForLinearHashCanonicalizesExternalRef(t *testing.T) {
	slugged := "https://linear.app/crown-dev/issue/BEA-93/updated-title-for-beads"
	canonical := "https://linear.app/crown-dev/issue/BEA-93"
	issue := &types.Issue{
		Title:       "Title",
		Description: "Description",
		ExternalRef: &slugged,
	}

	normalized := NormalizeIssueForLinearHash(issue)
	if normalized.ExternalRef == nil {
		t.Fatal("expected external_ref to be present")
	}
	if *normalized.ExternalRef != canonical {
		t.Fatalf("expected canonical external_ref %q, got %q", canonical, *normalized.ExternalRef)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
