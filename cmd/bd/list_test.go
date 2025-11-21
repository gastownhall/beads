package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
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

func TestListCommandSuite(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	t.Run("ListCommand", func(t *testing.T) {
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
			normalized := util.NormalizeLabels(labels)
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

	t.Run("output dot format", func(t *testing.T) {
			// Add a dependency to make the graph more interesting
			dep := &types.Dependency{
				IssueID:     h.issues[0].ID,
				DependsOnID: h.issues[1].ID,
				Type:        types.DepBlocks,
			}
			if err := h.store.AddDependency(h.ctx, dep, "test-user"); err != nil {
				t.Fatalf("Failed to add dependency: %v", err)
			}

			err := outputDotFormat(h.ctx, h.store, h.issues)
			if err != nil {
				t.Errorf("outputDotFormat failed: %v", err)
			}
		})

	t.Run("output formatted list dot", func(t *testing.T) {
			err := outputFormattedList(h.ctx, h.store, h.issues, "dot")
			if err != nil {
				t.Errorf("outputFormattedList with dot format failed: %v", err)
			}
		})

	t.Run("output formatted list digraph preset", func(t *testing.T) {
			// Dependency already added in previous test, just use it
			err := outputFormattedList(h.ctx, h.store, h.issues, "digraph")
			if err != nil {
				t.Errorf("outputFormattedList with digraph format failed: %v", err)
			}
		})

	t.Run("output formatted list custom template", func(t *testing.T) {
			err := outputFormattedList(h.ctx, h.store, h.issues, "{{.ID}} {{.Title}}")
			if err != nil {
				t.Errorf("outputFormattedList with custom template failed: %v", err)
			}
		})

	t.Run("output formatted list invalid template", func(t *testing.T) {
			err := outputFormattedList(h.ctx, h.store, h.issues, "{{.ID")
			if err == nil {
				t.Error("Expected error for invalid template")
			}
		})
	})
}

func TestListQueryCapabilitiesSuite(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	twoDaysAgo := now.Add(-48 * time.Hour)

	// Create test issues with varied attributes
	issue1 := &types.Issue{
		Title:       "Authentication Bug",
		Description: "Login fails with special characters",
		Notes:       "Needs urgent fix",
		Priority:    0,
		IssueType:   types.TypeBug,
		Status:      types.StatusOpen,
		Assignee:    "alice",
	}
	issue2 := &types.Issue{
		Title:       "Add OAuth Support",
		Description: "", // Empty description
		Priority:    2,
		IssueType:   types.TypeFeature,
		Status:      types.StatusInProgress,
		// No assignee
	}
	issue3 := &types.Issue{
		Title:       "Update Documentation",
		Description: "Update README with new features",
		Notes:       "Include OAuth setup",
		Priority:    3,
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Assignee:    "bob",
	}

	for _, issue := range []*types.Issue{issue1, issue2, issue3} {
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Close issue3 to set closed_at timestamp
	if err := s.CloseIssue(ctx, issue3.ID, "test-user", "Testing"); err != nil {
		t.Fatalf("Failed to close issue3: %v", err)
	}

	// Add labels
	s.AddLabel(ctx, issue1.ID, "critical", "test-user")
	s.AddLabel(ctx, issue1.ID, "security", "test-user")
	s.AddLabel(ctx, issue3.ID, "docs", "test-user")

	t.Run("pattern matching - title contains", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			TitleContains: "Auth",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("Expected 2 results with 'Auth' in title, got %d", len(results))
		}
	})

	t.Run("pattern matching - description contains", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			DescriptionContains: "special characters",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("Expected 1 result, got %d", len(results))
		}
		if len(results) > 0 && results[0].ID != issue1.ID {
			t.Errorf("Expected issue1, got %s", results[0].ID)
		}
	})

	t.Run("pattern matching - notes contains", func(t *testing.T) {
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				NotesContains: "OAuth",
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 result, got %d", len(results))
			}
			if len(results) > 0 && results[0].ID != issue3.ID {
				t.Errorf("Expected issue3, got %s", results[0].ID)
			}
		})

	t.Run("empty description check", func(t *testing.T) {
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				EmptyDescription: true,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 issue with empty description, got %d", len(results))
			}
			if len(results) > 0 && results[0].ID != issue2.ID {
				t.Errorf("Expected issue2, got %s", results[0].ID)
			}
		})

	t.Run("no assignee check", func(t *testing.T) {
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				NoAssignee: true,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 issue with no assignee, got %d", len(results))
			}
			if len(results) > 0 && results[0].ID != issue2.ID {
				t.Errorf("Expected issue2, got %s", results[0].ID)
			}
		})

	t.Run("no labels check", func(t *testing.T) {
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				NoLabels: true,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 issue with no labels, got %d", len(results))
			}
			if len(results) > 0 && results[0].ID != issue2.ID {
				t.Errorf("Expected issue2, got %s", results[0].ID)
			}
		})

	t.Run("priority range - min", func(t *testing.T) {
			minPrio := 2
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				PriorityMin: &minPrio,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 2 {
				t.Errorf("Expected 2 issues with priority >= 2, got %d", len(results))
			}
		})

	t.Run("priority range - max", func(t *testing.T) {
			maxPrio := 1
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				PriorityMax: &maxPrio,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 issue with priority <= 1, got %d", len(results))
			}
		})

	t.Run("priority range - min and max", func(t *testing.T) {
			minPrio := 1
			maxPrio := 2
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				PriorityMin: &minPrio,
				PriorityMax: &maxPrio,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 issue with priority between 1-2, got %d", len(results))
			}
		})

	t.Run("date range - created after", func(t *testing.T) {
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				CreatedAfter: &twoDaysAgo,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			// All issues created recently
			if len(results) != 3 {
				t.Errorf("Expected 3 issues created after two days ago, got %d", len(results))
			}
		})

	t.Run("date range - updated before", func(t *testing.T) {
			futureTime := now.Add(24 * time.Hour)
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				UpdatedBefore: &futureTime,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			// All issues updated before tomorrow
			if len(results) != 3 {
				t.Errorf("Expected 3 issues, got %d", len(results))
			}
		})

	t.Run("date range - closed after", func(t *testing.T) {
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				ClosedAfter: &yesterday,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 1 {
				t.Errorf("Expected 1 closed issue, got %d", len(results))
			}
		})

	t.Run("combined filters", func(t *testing.T) {
			minPrio := 0
			maxPrio := 2
			results, err := s.SearchIssues(ctx, "", types.IssueFilter{
				TitleContains: "Auth",
				PriorityMin:   &minPrio,
				PriorityMax:   &maxPrio,
			})
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
			if len(results) != 2 {
				t.Errorf("Expected 2 results matching combined filters, got %d", len(results))
			}
		})
}

func TestParseTimeFlag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"RFC3339", "2023-01-15T10:30:00Z", false},
		{"Date only", "2023-01-15", false},
		{"DateTime without zone", "2023-01-15T10:30:00", false},
		{"DateTime with space", "2023-01-15 10:30:00", false},
		{"Invalid format", "January 15, 2023", true},
		{"Empty string", "", true},
	}

	for _, tt := range tests {
	t.Run(tt.name, func(t *testing.T) {
			_, err := parseTimeFlag(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseTimeFlag(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
