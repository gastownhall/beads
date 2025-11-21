package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairMultiplePrefixes(t *testing.T) {
	// Create a temporary database
	dbPath := t.TempDir() + "/test.db"
	store, err := sqlite.New(context.Background(), dbPath)
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
	// We need to directly insert into the database to bypass prefix validation
	db := store.UnderlyingDB()
	
	now := time.Now()
	issues := []struct {
		ID    string
		Title string
	}{
		{"test-1", "Test issue 1"},
		{"test-2", "Test issue 2"},
		{"old-1", "Old issue 1"},
		{"old-2", "Old issue 2"},
		{"another-1", "Another issue 1"},
	}

	for _, issue := range issues {
		_, err := db.ExecContext(ctx, `
			INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
			VALUES (?, ?, 'open', 2, 'task', ?, ?)
		`, issue.ID, issue.Title, now, now)
		if err != nil {
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
