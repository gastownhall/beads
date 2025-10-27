package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairMultiplePrefixes(t *testing.T) {
	// Create a temporary database
	dbPath := t.TempDir() + "/test.db"
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set initial prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create issues with multiple prefixes (simulating corruption)
	// We'll manually create issues with different prefixes
	issues := []*types.Issue{
		{ID: "test-1", Title: "Test issue 1", Status: "open", Priority: 2, IssueType: "task"},
		{ID: "test-2", Title: "Test issue 2", Status: "open", Priority: 2, IssueType: "task"},
		{ID: "old-1", Title: "Old issue 1", Status: "open", Priority: 2, IssueType: "task"},
		{ID: "old-2", Title: "Old issue 2", Status: "open", Priority: 2, IssueType: "task"},
		{ID: "another-1", Title: "Another issue 1", Status: "open", Priority: 2, IssueType: "task"},
	}

	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Verify we have multiple prefixes
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	prefixes := detectPrefixes(allIssues)
	if len(prefixes) != 3 {
		t.Fatalf("expected 3 prefixes, got %d: %v", len(prefixes), prefixes)
	}

	// Test repair
	if err := repairPrefixes(ctx, store, "test", "test", allIssues, prefixes, false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	// Verify all issues now have correct prefix
	allIssues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after repair: %v", err)
	}

	prefixes = detectPrefixes(allIssues)
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix after repair, got %d: %v", len(prefixes), prefixes)
	}

	if _, ok := prefixes["test"]; !ok {
		t.Fatalf("expected prefix 'test', got %v", prefixes)
	}

	// Verify the original test-1 and test-2 are unchanged
	for _, id := range []string{"test-1", "test-2"} {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("expected issue %s to exist unchanged: %v", id, err)
		}
		if issue == nil {
			t.Fatalf("expected issue %s to exist", id)
		}
	}

	// Verify the others were renumbered
	issue, err := store.GetIssue(ctx, "test-3")
	if err != nil || issue == nil {
		t.Fatalf("expected test-3 to exist (renamed from another-1)")
	}

	issue, err = store.GetIssue(ctx, "test-4")
	if err != nil || issue == nil {
		t.Fatalf("expected test-4 to exist (renamed from old-1)")
	}

	issue, err = store.GetIssue(ctx, "test-5")
	if err != nil || issue == nil {
		t.Fatalf("expected test-5 to exist (renamed from old-2)")
	}
}
