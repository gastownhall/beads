package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestHashIDGeneration(t *testing.T) {
	ctx := context.Background()

	store, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx = context.Background()

	// Set up database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create an issue - should get a hash ID
	issue := &types.Issue{
		Title:       "Test Issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Verify hash ID format: bd-<3-8 base36 chars> with adaptive length
	// For empty/small database, should use 3 chars
	if len(issue.ID) < 6 || len(issue.ID) > 11 { // "bd-" (3) + 3-8 base36 chars = 6-11
		t.Errorf("Expected ID length 6-11, got %d: %s", len(issue.ID), issue.ID)
	}

	if issue.ID[:3] != "bd-" {
		t.Errorf("Expected ID to start with 'bd-', got: %s", issue.ID)
	}

	// Verify we can retrieve the issue
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if retrieved.Title != issue.Title {
		t.Errorf("Expected title %q, got %q", issue.Title, retrieved.Title)
	}
}

func TestHashIDDeterministic(t *testing.T) {
	// Same inputs should produce same hash (with same nonce)
	prefix := "bd"
	title := "Test Issue"
	description := "Test description"
	actor := "test-actor"
	timestamp := time.Now()

	id1 := generateHashID(prefix, title, description, actor, timestamp, 6, 0)
	id2 := generateHashID(prefix, title, description, actor, timestamp, 6, 0)

	if id1 != id2 {
		t.Errorf("Expected same hash for same inputs, got %s and %s", id1, id2)
	}
}

func TestHashIDCollisionHandling(t *testing.T) {
	ctx := context.Background()

	store, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx = context.Background()

	// Set up database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create first issue
	issue1 := &types.Issue{
		Title:       "Duplicate Title",
		Description: "Same description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}

	if err := store.CreateIssue(ctx, issue1, "actor"); err != nil {
		t.Fatalf("Failed to create first issue: %v", err)
	}

	// Create second issue with same content at same time
	// This should get a different hash due to nonce increment
	issue2 := &types.Issue{
		Title:       "Duplicate Title",
		Description: "Same description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   issue1.CreatedAt, // Force same timestamp
	}

	if err := store.CreateIssue(ctx, issue2, "actor"); err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// Verify both issues exist with different IDs
	if issue1.ID == issue2.ID {
		t.Errorf("Expected different IDs for duplicate content, both got: %s", issue1.ID)
	}

	// Verify both can be retrieved
	_, err = store.GetIssue(ctx, issue1.ID)
	if err != nil {
		t.Errorf("Failed to retrieve first issue: %v", err)
	}

	_, err = store.GetIssue(ctx, issue2.ID)
	if err != nil {
		t.Errorf("Failed to retrieve second issue: %v", err)
	}
}

func TestHashIDBatchCreation(t *testing.T) {
	ctx := context.Background()

	store, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx = context.Background()

	// Set up database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create multiple issues with similar content
	issues := []*types.Issue{
		{
			Title:       "Issue 1",
			Description: "Description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			Title:       "Issue 1", // Same title
			Description: "Description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
		{
			Title:       "Issue 2",
			Description: "Description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
	}

	if err := store.CreateIssues(ctx, issues, "actor"); err != nil {
		t.Fatalf("Failed to create issues: %v", err)
	}

	// Verify all issues got unique IDs
	ids := make(map[string]bool)
	for _, issue := range issues {
		if ids[issue.ID] {
			t.Errorf("Duplicate ID found: %s", issue.ID)
		}
		ids[issue.ID] = true

		// Verify hash ID format (3-8 chars with adaptive length)
		if len(issue.ID) < 6 || len(issue.ID) > 11 {
			t.Errorf("Expected ID length 6-11, got %d: %s", len(issue.ID), issue.ID)
		}
		if issue.ID[:3] != "bd-" {
			t.Errorf("Expected ID to start with 'bd-', got: %s", issue.ID)
		}
	}
}
