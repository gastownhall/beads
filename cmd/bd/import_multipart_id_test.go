package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestImportMultiPartIDs tests that issue IDs with hyphens in the suffix
// (like "vc-baseline-test") are correctly recognized as having prefix "vc"
// and not treated as having different prefixes like "vc-baseline-"
func TestImportMultiPartIDs(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	
	// Create database with "vc" prefix
	st := newTestStoreWithPrefix(t, dbPath, "vc")

	ctx := context.Background()

	// Create issues with multi-part IDs
	issues := []*types.Issue{
		{
			ID:          "vc-baseline-test",
			Title:       "Baseline test issue",
			Description: "Issue with hyphenated suffix",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
		{
			ID:          "vc-92cl-gate-test",
			Title:       "Gate test issue",
			Description: "Another issue with hyphenated suffix",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
		{
			ID:          "vc-test",
			Title:       "Simple test issue",
			Description: "Issue without hyphenated suffix",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
	}

	// Import should succeed without prefix mismatch errors
	opts := ImportOptions{
		DryRun:     false,
		SkipUpdate: false,
		Strict:     false,
	}

	result, err := importIssuesCore(ctx, dbPath, st, issues, opts)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Should not detect prefix mismatch
	if result.PrefixMismatch {
		t.Errorf("Import incorrectly detected prefix mismatch")
		t.Logf("Mismatched prefixes: %v", result.MismatchPrefixes)
	}

	// All issues should be created
	if result.Created != 3 {
		t.Errorf("Expected 3 issues created, got %d", result.Created)
	}

	// Verify issues exist in database
	for _, issue := range issues {
		dbIssue, err := st.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Errorf("Failed to get issue %s: %v", issue.ID, err)
			continue
		}
		if dbIssue.Title != issue.Title {
			t.Errorf("Issue %s title mismatch: got %q, want %q", issue.ID, dbIssue.Title, issue.Title)
		}
	}
}

// TestImportMultiHyphenPrefix tests GH#422: importing with multi-hyphen prefixes
// like "asianops-audit-" should not cause false positive prefix mismatch errors.
func TestImportMultiHyphenPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	// Create database with multi-hyphen prefix "asianops-audit"
	st := newTestStoreWithPrefix(t, dbPath, "asianops-audit")

	ctx := context.Background()

	// Create issues with hash-like suffixes that could be mistaken for words
	// The key is that "test", "task", "demo" look like English words (4+ chars, no digits)
	// which previously caused ExtractIssuePrefix to fall back to first hyphen
	issues := []*types.Issue{
		{
			ID:          "asianops-audit-sa0",
			Title:       "Issue with short hash suffix",
			Description: "Short hash suffix should work",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
		{
			ID:          "asianops-audit-test",
			Title:       "Issue with word-like suffix",
			Description: "Word-like suffix 'test' was causing false positive",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
		{
			ID:          "asianops-audit-task",
			Title:       "Another word-like suffix",
			Description: "Word-like suffix 'task' was also problematic",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
		{
			ID:          "asianops-audit-demo",
			Title:       "Demo issue",
			Description: "Word-like suffix 'demo'",
			Status:      "open",
			Priority:    1,
			IssueType:   "task",
		},
	}

	// Import should succeed without prefix mismatch errors
	opts := ImportOptions{
		DryRun:     false,
		SkipUpdate: false,
		Strict:     false,
	}

	result, err := importIssuesCore(ctx, dbPath, st, issues, opts)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// GH#422: Should NOT detect prefix mismatch
	if result.PrefixMismatch {
		t.Errorf("Import incorrectly detected prefix mismatch for multi-hyphen prefix")
		t.Logf("Expected prefix: asianops-audit")
		t.Logf("Mismatched prefixes detected: %v", result.MismatchPrefixes)
	}

	// All issues should be created
	if result.Created != 4 {
		t.Errorf("Expected 4 issues created, got %d", result.Created)
	}

	// Verify issues exist in database
	for _, issue := range issues {
		dbIssue, err := st.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Errorf("Failed to get issue %s: %v", issue.ID, err)
			continue
		}
		if dbIssue.Title != issue.Title {
			t.Errorf("Issue %s title mismatch: got %q, want %q", issue.ID, dbIssue.Title, issue.Title)
		}
	}
}

// TestDetectPrefixFromIssues tests the detectPrefixFromIssues function
// with multi-part IDs
func TestDetectPrefixFromIssues(t *testing.T) {
	tests := []struct {
		name     string
		issues   []*types.Issue
		expected string
	}{
		{
			name: "simple IDs",
			issues: []*types.Issue{
				{ID: "bd-1"},
				{ID: "bd-2"},
				{ID: "bd-3"},
			},
			expected: "bd",
		},
		{
			name: "multi-part IDs",
			issues: []*types.Issue{
				{ID: "vc-baseline-test"},
				{ID: "vc-92cl-gate-test"},
				{ID: "vc-test"},
			},
			expected: "vc",
		},
		{
			name: "mixed multi-part IDs",
			issues: []*types.Issue{
				{ID: "prefix-a-b-c"},
				{ID: "prefix-x-y-z"},
				{ID: "prefix-simple"},
			},
			expected: "prefix",
		},
		{
			name:     "empty list",
			issues:   []*types.Issue{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectPrefixFromIssues(tt.issues)
			if result != tt.expected {
				t.Errorf("detectPrefixFromIssues() = %q, want %q", result, tt.expected)
			}
		})
	}
}
