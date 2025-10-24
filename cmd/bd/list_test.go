package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestListCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-list-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s, err := sqlite.New(testDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Create test issues
	now := time.Now()
	issues := []*types.Issue{
		{
			Title:       "Bug Issue",
			Description: "Test bug",
			Priority:    0,
			IssueType:   types.TypeBug,
			Status:      types.StatusOpen,
		},
		{
			Title:       "Feature Issue",
			Description: "Test feature",
			Priority:    1,
			IssueType:   types.TypeFeature,
			Status:      types.StatusInProgress,
			Assignee:    "alice",
		},
		{
			Title:       "Task Issue",
			Description: "Test task",
			Priority:    2,
			IssueType:   types.TypeTask,
			Status:      types.StatusClosed,
			ClosedAt:    &now,
		},
	}

	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Add labels to first issue
	if err := s.AddLabel(ctx, issues[0].ID, "critical", "test-user"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}

	t.Run("list all issues", func(t *testing.T) {
		filter := types.IssueFilter{}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 3 {
			t.Errorf("Expected 3 issues, got %d", len(results))
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		status := types.StatusOpen
		filter := types.IssueFilter{Status: &status}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 open issue, got %d", len(results))
		}
		if results[0].Status != types.StatusOpen {
			t.Errorf("Expected status %s, got %s", types.StatusOpen, results[0].Status)
		}
	})

	t.Run("filter by priority", func(t *testing.T) {
		priority := 0
		filter := types.IssueFilter{Priority: &priority}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 P0 issue, got %d", len(results))
		}
		if results[0].Priority != 0 {
			t.Errorf("Expected priority 0, got %d", results[0].Priority)
		}
	})

	t.Run("filter by assignee", func(t *testing.T) {
		assignee := "alice"
		filter := types.IssueFilter{Assignee: &assignee}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 issue for alice, got %d", len(results))
		}
		if results[0].Assignee != "alice" {
			t.Errorf("Expected assignee alice, got %s", results[0].Assignee)
		}
	})

	t.Run("filter by issue type", func(t *testing.T) {
		issueType := types.TypeBug
		filter := types.IssueFilter{IssueType: &issueType}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 bug issue, got %d", len(results))
		}
		if results[0].IssueType != types.TypeBug {
			t.Errorf("Expected type %s, got %s", types.TypeBug, results[0].IssueType)
		}
	})

	t.Run("filter by label", func(t *testing.T) {
		filter := types.IssueFilter{Labels: []string{"critical"}}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 issue with critical label, got %d", len(results))
		}
	})

	t.Run("filter by title search", func(t *testing.T) {
		filter := types.IssueFilter{TitleSearch: "Bug"}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 issue matching 'Bug', got %d", len(results))
		}
	})

	t.Run("limit results", func(t *testing.T) {
		filter := types.IssueFilter{Limit: 2}
		results, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("Failed to search issues: %v", err)
		}

		if len(results) > 2 {
			t.Errorf("Expected at most 2 issues, got %d", len(results))
		}
	})

	t.Run("normalize labels", func(t *testing.T) {
		labels := []string{" bug ", "critical", "", "bug", "  feature  "}
		normalized := normalizeLabels(labels)

		expected := []string{"bug", "critical", "feature"}
		if len(normalized) != len(expected) {
			t.Errorf("Expected %d normalized labels, got %d", len(expected), len(normalized))
		}

		// Check deduplication and trimming
		seen := make(map[string]bool)
		for _, label := range normalized {
			if label == "" {
				t.Error("Found empty label after normalization")
			}
			if label != strings.TrimSpace(label) {
				t.Errorf("Label not trimmed: '%s'", label)
			}
			if seen[label] {
				t.Errorf("Duplicate label found: %s", label)
			}
			seen[label] = true
		}
	})
}
