package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestImportReturnsCorrectCounts reproduces bd-88
// Import should report correct "created" count when importing new issues
func TestImportReturnsCorrectCounts(t *testing.T) {
	// Create temporary database
	tmpDir, err := os.MkdirTemp("", "beads-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, ".beads", "issues.db")
	// Initialize database
	store := newTestStore(t, dbPath)

	ctx := context.Background()

	// Create test issues to import
	issues := make([]*types.Issue, 0, 5)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("test-%d", i)
		issues = append(issues, &types.Issue{
			ID:          id,
			Title:       fmt.Sprintf("Test Issue %d", i),
			Description: "Test description",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		})
	}

	// Import with default options
	opts := ImportOptions{

		DryRun:     false,
		SkipUpdate: false,
		Strict:     false,
	}

	result, err := importIssuesCore(ctx, dbPath, store, issues, opts)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Check that Created count matches
	if result.Created != len(issues) {
		t.Errorf("Expected Created=%d, got %d", len(issues), result.Created)
	}

	// Verify issues are actually in the database
	for _, issue := range issues {
		retrieved, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Errorf("Failed to get issue %s: %v", issue.ID, err)
		}
		if retrieved == nil {
			t.Errorf("Issue %s not found in database", issue.ID)
		}
	}

	// Now test re-importing the same issues (idempotent case)
	result2, err := importIssuesCore(ctx, dbPath, store, issues, opts)
	if err != nil {
		t.Fatalf("Second import failed: %v", err)
	}

	// bd-88: When reimporting unchanged issues, should report them as "Unchanged"
	if result2.Created != 0 {
		t.Errorf("Second import: expected Created=0, got %d", result2.Created)
	}
	if result2.Updated != 0 {
		t.Errorf("Second import: expected Updated=0, got %d", result2.Updated)
	}
	if result2.Unchanged != len(issues) {
		t.Errorf("Second import: expected Unchanged=%d, got %d", len(issues), result2.Unchanged)
	}

	t.Logf("Second import: Created=%d, Updated=%d, Unchanged=%d, Skipped=%d",
		result2.Created, result2.Updated, result2.Unchanged, result2.Skipped)
}
