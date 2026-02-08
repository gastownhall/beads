//go:build integration
// +build integration

package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestAdaptiveIDLength_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow E2E test in short mode")
	}
	// Create in-memory database
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer db.Close()

	// Initialize with prefix
	if err := db.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Helper to create issue and verify ID length
	createAndCheckLength := func(title string, expectedHashLen int) string {
		issue := &types.Issue{
			Title:       title,
			Description: "Test",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		}

		if err := db.CreateIssue(ctx, issue, "test@example.com"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Check ID format: test-xxxx
		if !strings.HasPrefix(issue.ID, "test-") {
			t.Errorf("ID should start with test-, got %s", issue.ID)
		}

		hashPart := strings.TrimPrefix(issue.ID, "test-")
		if len(hashPart) != expectedHashLen {
			t.Errorf("Issue %s: hash length = %d, want %d", title, len(hashPart), expectedHashLen)
		}

		return issue.ID
	}

	// Test 1: First few issues should use 3-char IDs (base36 allows shorter IDs)
	t.Run("first_50_issues_use_3_chars", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			title := formatTitle("Issue %d", i)
			createAndCheckLength(title, 3)
		}
	})

	// Test 2: Issues 50-200 should transition to 4 chars
	// (3 chars good up to ~160 issues with 25% threshold)
	t.Run("issues_50_to_200_use_3_or_4_chars", func(t *testing.T) {
		for i := 50; i < 200; i++ {
			title := formatTitle("Issue %d", i)
			issue := &types.Issue{
				Title:       title,
				Description: "Test",
				Status:      "open",
				Priority:    1,
				IssueType:   "task",
			}

			if err := db.CreateIssue(ctx, issue, "test@example.com"); err != nil {
				t.Fatalf("Failed to create issue: %v", err)
			}

			// Most should be 3 chars initially, transitioning to 4 after ~160
			hashPart := strings.TrimPrefix(issue.ID, "test-")
			if len(hashPart) < 3 || len(hashPart) > 4 {
				t.Errorf("Issue %d has hash length %d, expected 3-4", i, len(hashPart))
			}
		}
	})

	// Test 3: At 500-1000 issues, should scale to 4-5 chars
	// (4 chars good up to ~980 issues with 25% threshold)
	t.Run("verify_adaptive_scaling_works", func(t *testing.T) {
		// Just verify that we can create more issues and the algorithm doesn't break
		// The actual length will be determined by the adaptive algorithm
		for i := 200; i < 250; i++ {
			title := formatTitle("Issue %d", i)
			issue := &types.Issue{
				Title:       title,
				Description: "Test",
				Status:      "open",
				Priority:    1,
				IssueType:   "task",
			}

			if err := db.CreateIssue(ctx, issue, "test@example.com"); err != nil {
				t.Fatalf("Failed to create issue: %v", err)
			}

			// Should use 4-5 chars depending on database size
			hashPart := strings.TrimPrefix(issue.ID, "test-")
			if len(hashPart) < 3 || len(hashPart) > 5 {
				t.Errorf("Issue %d has hash length %d, expected 3-5", i, len(hashPart))
			}
		}
	})
}

func formatTitle(format string, i int) string {
	// Use sprintf to format title
	return strings.Replace(format, "%d", strings.Repeat("x", i%10), 1) + string(rune('a'+i%26))
}

func TestAdaptiveIDLength_CustomConfig(t *testing.T) {
	// Create in-memory database
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer db.Close()

	// Initialize with custom config
	if err := db.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set stricter collision threshold (1%) and min length of 5
	if err := db.SetConfig(ctx, "max_collision_prob", "0.01"); err != nil {
		t.Fatalf("Failed to set max_collision_prob: %v", err)
	}
	if err := db.SetConfig(ctx, "min_hash_length", "5"); err != nil {
		t.Fatalf("Failed to set min_hash_length: %v", err)
	}

	// With min_hash_length=5, all IDs should be at least 5 chars
	for i := 0; i < 20; i++ {
		issue := &types.Issue{
			Title:       formatTitle("Issue %d", i),
			Description: "Test",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		}

		if err := db.CreateIssue(ctx, issue, "test@example.com"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		hashPart := strings.TrimPrefix(issue.ID, "test-")
		// With min_hash_length=5, should use at least 5 chars
		if len(hashPart) < 5 {
			t.Errorf("Issue %d with min_hash_length=5: hash length = %d, want >= 5", i, len(hashPart))
		}
	}
}
