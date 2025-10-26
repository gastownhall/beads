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

// listTestHelper provides test setup and assertion methods
type listTestHelper struct {
	t      *testing.T
	ctx    context.Context
	store  *sqlite.SQLiteStorage
	issues []*types.Issue
}

func newListTestHelper(t *testing.T, store *sqlite.SQLiteStorage) *listTestHelper {
	return &listTestHelper{t: t, ctx: context.Background(), store: store}
}

func (h *listTestHelper) createTestIssues() {
	now := time.Now()
	h.issues = []*types.Issue{
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
			Assignee:    testUserAlice,
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
	for _, issue := range h.issues {
		if err := h.store.CreateIssue(h.ctx, issue, "test-user"); err != nil {
			h.t.Fatalf("Failed to create issue: %v", err)
		}
	}
}

func (h *listTestHelper) addLabel(id, label string) {
	if err := h.store.AddLabel(h.ctx, id, label, "test-user"); err != nil {
		h.t.Fatalf("Failed to add label: %v", err)
	}
}

func (h *listTestHelper) search(filter types.IssueFilter) []*types.Issue {
	results, err := h.store.SearchIssues(h.ctx, "", filter)
	if err != nil {
		h.t.Fatalf("Failed to search issues: %v", err)
	}
	return results
}

func (h *listTestHelper) assertCount(count, expected int, desc string) {
	if count != expected {
		h.t.Errorf("Expected %d %s, got %d", expected, desc, count)
	}
}

func (h *listTestHelper) assertEqual(expected, actual interface{}, field string) {
	if expected != actual {
		h.t.Errorf("Expected %s %v, got %v", field, expected, actual)
	}
}

func (h *listTestHelper) assertAtMost(count, maxCount int, desc string) {
	if count > maxCount {
		h.t.Errorf("Expected at most %d %s, got %d", maxCount, desc, count)
	}
}

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

	h := newListTestHelper(t, s)
	h.createTestIssues()
	h.addLabel(h.issues[0].ID, "critical")

	t.Run("list all issues", func(t *testing.T) {
		results := h.search(types.IssueFilter{})
		h.assertCount(len(results), 3, "issues")
	})

	t.Run("filter by status", func(t *testing.T) {
		status := types.StatusOpen
		results := h.search(types.IssueFilter{Status: &status})
		h.assertCount(len(results), 1, "open issues")
		h.assertEqual(types.StatusOpen, results[0].Status, "status")
	})

	t.Run("filter by priority", func(t *testing.T) {
		priority := 0
		results := h.search(types.IssueFilter{Priority: &priority})
		h.assertCount(len(results), 1, "P0 issues")
		h.assertEqual(0, results[0].Priority, "priority")
	})

	t.Run("filter by assignee", func(t *testing.T) {
		assignee := testUserAlice
		results := h.search(types.IssueFilter{Assignee: &assignee})
		h.assertCount(len(results), 1, "issues for alice")
		h.assertEqual(testUserAlice, results[0].Assignee, "assignee")
	})

	t.Run("filter by issue type", func(t *testing.T) {
		issueType := types.TypeBug
		results := h.search(types.IssueFilter{IssueType: &issueType})
		h.assertCount(len(results), 1, "bug issues")
		h.assertEqual(types.TypeBug, results[0].IssueType, "type")
	})

	t.Run("filter by label", func(t *testing.T) {
		results := h.search(types.IssueFilter{Labels: []string{"critical"}})
		h.assertCount(len(results), 1, "issues with critical label")
	})

	t.Run("filter by title search", func(t *testing.T) {
		results := h.search(types.IssueFilter{TitleSearch: "Bug"})
		h.assertCount(len(results), 1, "issues matching 'Bug'")
	})

	t.Run("limit results", func(t *testing.T) {
		results := h.search(types.IssueFilter{Limit: 2})
		h.assertAtMost(len(results), 2, "issues")
	})

	t.Run("normalize labels", func(t *testing.T) {
		labels := []string{" bug ", "critical", "", "bug", "  feature  "}
		normalized := normalizeLabels(labels)
		expected := []string{"bug", "critical", "feature"}
		h.assertCount(len(normalized), len(expected), "normalized labels")

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
