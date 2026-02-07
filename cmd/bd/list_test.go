package main

import (
	"context"
	"path/filepath"
	"slices"
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
			if len(results) > 0 {
				h.assertEqual("Bug Issue", results[0].Title, "label-filtered issue title")
			}
		})

		t.Run("filter by title search", func(t *testing.T) {
			results := h.search(types.IssueFilter{TitleSearch: "Bug"})
			h.assertCount(len(results), 1, "issues matching 'Bug'")
			if len(results) > 0 {
				h.assertEqual("Bug Issue", results[0].Title, "title-search result")
			}
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
	if err := s.CloseIssue(ctx, issue3.ID, "test-user", "Testing", ""); err != nil {
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

// TestStableTreeOrdering tests that tree display order is stable across multiple invocations
// This test specifically addresses the bug where --tree output was non-deterministic due to
// unstable ordering of root issues and children within the same priority level
func TestStableTreeOrdering(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	store := newTestStore(t, testDB)
	ctx := context.Background()

	// Helper to create issue with specific priority for testing sort stability
	createIssue := func(title string, priority int) *types.Issue {
		issue := &types.Issue{
			Title:     title,
			Priority:  priority,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", title, err)
		}
		return issue
	}

	// Helper to add parent-child dependency
	addParentChild := func(child, parent *types.Issue) {
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
			CreatedAt:   time.Now(),
			CreatedBy:   "test-user",
		}
		if err := store.AddDependency(ctx, dep, "test-user"); err != nil {
			t.Fatalf("Failed to add dependency %s -> %s: %v", child.ID, parent.ID, err)
		}
	}

	// Create a hierarchy with mixed priorities to test both primary and secondary sort:
	// - Multiple root issues with same priority (tests secondary sort by ID)
	// - Multiple children with same priority (tests children sorting stability)
	// - Mixed priorities (tests primary sort by priority)

	// Root issues with different priorities
	rootP1A := createIssue("Root P1 A", 1) // Should be first (lowest priority number)
	rootP1B := createIssue("Root P1 B", 1) // Should be second (same priority, sorted by ID)
	rootP2 := createIssue("Root P2", 2)    // Should be third
	rootP3 := createIssue("Root P3", 3)    // Should be last

	// Children with mixed priorities under rootP1A
	childP1 := createIssue("Child P1", 1)    // Should be first child
	childP2A := createIssue("Child P2 A", 2) // Should be second child
	childP2B := createIssue("Child P2 B", 2) // Should be third child (same priority, sorted by ID)
	childP3 := createIssue("Child P3", 3)    // Should be last child

	// Add parent-child relationships
	addParentChild(childP1, rootP1A)
	addParentChild(childP2A, rootP1A)
	addParentChild(childP2B, rootP1A)
	addParentChild(childP3, rootP1A)

	// Test that buildIssueTree produces stable ordering
	t.Run("stable_root_ordering", func(t *testing.T) {
		// Get only the test issues we created (filter by title pattern)
		testIssues := []*types.Issue{rootP1A, rootP1B, rootP2, rootP3}

		// Build tree multiple times and verify identical ordering
		var rootOrderings [][]string
		for i := 0; i < 5; i++ {
			roots, _ := buildIssueTree(testIssues)

			// Extract root IDs in order
			var rootIDs []string
			for _, root := range roots {
				rootIDs = append(rootIDs, root.ID)
			}
			rootOrderings = append(rootOrderings, rootIDs)
		}

		// Verify all runs produce identical root ordering
		expectedRootOrder := rootOrderings[0]
		for i := 1; i < len(rootOrderings); i++ {
			if !slicesEqual(expectedRootOrder, rootOrderings[i]) {
				t.Errorf("Root ordering differs between runs:\nRun 1: %v\nRun %d: %v",
					expectedRootOrder, i+1, rootOrderings[i])
			}
		}

		// Verify expected sort order (priority first, then ID)
		// Since IDs are auto-generated, we'll verify by comparing with sorted slice
		expectedRoots := []*types.Issue{rootP1A, rootP1B, rootP2, rootP3}
		slices.SortFunc(expectedRoots, compareIssuesByPriority)
		var expectedOrder []string
		for _, issue := range expectedRoots {
			expectedOrder = append(expectedOrder, issue.ID)
		}

		if !slicesEqual(expectedOrder, expectedRootOrder) {
			t.Errorf("Root ordering incorrect:\nExpected: %v\nActual: %v",
				expectedOrder, expectedRootOrder)
		}
	})

	t.Run("stable_children_ordering", func(t *testing.T) {
		// Get test issues including dependencies
		allTestIssues := []*types.Issue{rootP1A, rootP1B, rootP2, rootP3, childP1, childP2A, childP2B, childP3}

		// Load dependencies for tree building
		allDeps, err := store.GetAllDependencyRecords(ctx)
		if err != nil {
			t.Fatalf("Failed to get dependencies: %v", err)
		}

		// Build tree multiple times and verify identical children ordering
		var childOrderings [][]string
		for i := 0; i < 5; i++ {
			_, childrenMap := buildIssueTreeWithDeps(allTestIssues, allDeps)

			// Extract children IDs of rootP1A in order
			children := childrenMap[rootP1A.ID]
			var childIDs []string
			for _, child := range children {
				childIDs = append(childIDs, child.ID)
			}
			childOrderings = append(childOrderings, childIDs)
		}

		// Verify all runs produce identical children ordering
		if len(childOrderings) == 0 || len(childOrderings[0]) == 0 {
			t.Fatal("No children found for rootP1A")
		}

		expectedChildOrder := childOrderings[0]
		for i := 1; i < len(childOrderings); i++ {
			if !slicesEqual(expectedChildOrder, childOrderings[i]) {
				t.Errorf("Children ordering differs between runs:\nRun 1: %v\nRun %d: %v",
					expectedChildOrder, i+1, childOrderings[i])
			}
		}

		// Verify expected sort order by sorting the expected children and comparing
		expectedChildren := []*types.Issue{childP1, childP2A, childP2B, childP3}
		slices.SortFunc(expectedChildren, compareIssuesByPriority)
		var expectedOrder []string
		for _, child := range expectedChildren {
			expectedOrder = append(expectedOrder, child.ID)
		}

		if !slicesEqual(expectedOrder, expectedChildOrder) {
			t.Errorf("Children ordering incorrect:\nExpected: %v\nActual: %v",
				expectedOrder, expectedChildOrder)
		}
	})
}

// Helper function to compare string slices for equality
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFormatIssueLong(t *testing.T) {
	tests := []struct {
		name   string
		issue  *types.Issue
		labels []string
		want   string // substring to check for
	}{
		{
			name: "open issue",
			issue: &types.Issue{
				ID:        "test-123",
				Title:     "Test Issue",
				Priority:  1,
				IssueType: types.TypeBug,
				Status:    types.StatusOpen,
			},
			labels: nil,
			want:   "test-123",
		},
		{
			name: "closed issue",
			issue: &types.Issue{
				ID:        "test-456",
				Title:     "Closed Issue",
				Priority:  0,
				IssueType: types.TypeTask,
				Status:    types.StatusClosed,
			},
			labels: nil,
			want:   "test-456",
		},
		{
			name: "issue with assignee",
			issue: &types.Issue{
				ID:        "test-789",
				Title:     "Assigned Issue",
				Priority:  2,
				IssueType: types.TypeFeature,
				Status:    types.StatusInProgress,
				Assignee:  "alice",
			},
			labels: nil,
			want:   "Assignee: alice",
		},
		{
			name: "issue with labels",
			issue: &types.Issue{
				ID:        "test-abc",
				Title:     "Labeled Issue",
				Priority:  1,
				IssueType: types.TypeBug,
				Status:    types.StatusOpen,
			},
			labels: []string{"critical", "security"},
			want:   "Labels:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			formatIssueLong(&buf, tt.issue, tt.labels)
			result := buf.String()
			if !strings.Contains(result, tt.want) {
				t.Errorf("formatIssueLong() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

func TestFormatIssueCompact(t *testing.T) {
	tests := []struct {
		name   string
		issue  *types.Issue
		labels []string
		want   string
	}{
		{
			name: "basic issue",
			issue: &types.Issue{
				ID:        "test-123",
				Title:     "Test Issue",
				Priority:  1,
				IssueType: types.TypeBug,
				Status:    types.StatusOpen,
			},
			labels: nil,
			want:   "Test Issue",
		},
		{
			name: "issue with assignee",
			issue: &types.Issue{
				ID:        "test-456",
				Title:     "Assigned Issue",
				Priority:  2,
				IssueType: types.TypeTask,
				Status:    types.StatusInProgress,
				Assignee:  "bob",
			},
			labels: nil,
			want:   "@bob",
		},
		{
			name: "issue with labels",
			issue: &types.Issue{
				ID:        "test-789",
				Title:     "Labeled Issue",
				Priority:  0,
				IssueType: types.TypeFeature,
				Status:    types.StatusOpen,
			},
			labels: []string{"urgent"},
			want:   "[urgent]",
		},
		{
			name: "closed issue",
			issue: &types.Issue{
				ID:        "test-def",
				Title:     "Closed Issue",
				Priority:  3,
				IssueType: types.TypeTask,
				Status:    types.StatusClosed,
			},
			labels: nil,
			want:   "Closed Issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			formatIssueCompact(&buf, tt.issue, tt.labels, nil, nil)
			result := buf.String()
			if !strings.Contains(result, tt.want) {
				t.Errorf("formatIssueCompact() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

func TestBuildBlockingMaps(t *testing.T) {
	// Create test dependency records
	allDeps := map[string][]*types.Dependency{
		"issue-A": {
			{IssueID: "issue-A", DependsOnID: "issue-B", Type: types.DepBlocks},
		},
		"issue-C": {
			{IssueID: "issue-C", DependsOnID: "issue-A", Type: types.DepBlocks},
			{IssueID: "issue-C", DependsOnID: "issue-B", Type: types.DepRelated}, // Should be ignored (not blocking)
		},
	}

	blockedByMap, blocksMap := buildBlockingMaps(allDeps)

	// issue-A is blocked by issue-B
	if len(blockedByMap["issue-A"]) != 1 || blockedByMap["issue-A"][0] != "issue-B" {
		t.Errorf("issue-A blockedBy = %v, want [issue-B]", blockedByMap["issue-A"])
	}

	// issue-B blocks issue-A
	if len(blocksMap["issue-B"]) != 1 || blocksMap["issue-B"][0] != "issue-A" {
		t.Errorf("issue-B blocks = %v, want [issue-A]", blocksMap["issue-B"])
	}

	// issue-C is blocked by issue-A (related dep is ignored)
	if len(blockedByMap["issue-C"]) != 1 || blockedByMap["issue-C"][0] != "issue-A" {
		t.Errorf("issue-C blockedBy = %v, want [issue-A]", blockedByMap["issue-C"])
	}

	// issue-A also blocks issue-C
	if len(blocksMap["issue-A"]) != 1 || blocksMap["issue-A"][0] != "issue-C" {
		t.Errorf("issue-A blocks = %v, want [issue-C]", blocksMap["issue-A"])
	}
}

func TestFormatIssueCompactWithDependencies(t *testing.T) {
	tests := []struct {
		name      string
		issue     *types.Issue
		blockedBy []string
		blocks    []string
		want      string
	}{
		{
			name: "issue with blocked by",
			issue: &types.Issue{
				ID:        "test-123",
				Title:     "Blocked Issue",
				Priority:  1,
				IssueType: types.TypeTask,
				Status:    types.StatusOpen,
			},
			blockedBy: []string{"test-100"},
			blocks:    nil,
			want:      "(blocked by: test-100)",
		},
		{
			name: "issue with blocks",
			issue: &types.Issue{
				ID:        "test-456",
				Title:     "Blocking Issue",
				Priority:  1,
				IssueType: types.TypeTask,
				Status:    types.StatusOpen,
			},
			blockedBy: nil,
			blocks:    []string{"test-200", "test-300"},
			want:      "(blocks: test-200, test-300)",
		},
		{
			name: "issue with both",
			issue: &types.Issue{
				ID:        "test-789",
				Title:     "Middle Issue",
				Priority:  1,
				IssueType: types.TypeTask,
				Status:    types.StatusOpen,
			},
			blockedBy: []string{"test-100"},
			blocks:    []string{"test-200"},
			want:      "(blocked by: test-100, blocks: test-200)",
		},
		{
			name: "issue with no dependencies",
			issue: &types.Issue{
				ID:        "test-abc",
				Title:     "Independent Issue",
				Priority:  1,
				IssueType: types.TypeTask,
				Status:    types.StatusOpen,
			},
			blockedBy: nil,
			blocks:    nil,
			want:      "Independent Issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			formatIssueCompact(&buf, tt.issue, nil, tt.blockedBy, tt.blocks)
			result := buf.String()
			if !strings.Contains(result, tt.want) {
				t.Errorf("formatIssueCompact() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

func TestParseTimeFlag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Absolute formats
		{"RFC3339", "2023-01-15T10:30:00Z", false},
		{"Date only", "2023-01-15", false},
		// Compact duration formats (GH#820)
		{"Compact hours", "+6h", false},
		{"Compact days", "+1d", false},
		{"Compact weeks", "+2w", false},
		{"Compact negative", "-3d", false},
		// Natural language (GH#820)
		{"Natural tomorrow", "tomorrow", false},
		{"Natural next monday", "next monday", false},
		// Invalid formats
		{"Invalid format", "not-a-date", true},
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

// TestListTimeBasedFilters tests the time-based scheduling filters (GH#820)
func TestListTimeBasedFilters(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)
	nextWeek := now.Add(7 * 24 * time.Hour)

	// Create test issues with varied due_at and defer_until values
	issueNoSchedule := &types.Issue{
		Title:     "Issue without scheduling",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	issueDeferredFuture := &types.Issue{
		Title:      "Deferred until tomorrow",
		Priority:   2,
		IssueType:  types.TypeTask,
		Status:     types.StatusOpen,
		DeferUntil: &tomorrow,
	}
	issueDeferredPast := &types.Issue{
		Title:      "Was deferred until yesterday",
		Priority:   2,
		IssueType:  types.TypeTask,
		Status:     types.StatusOpen,
		DeferUntil: &yesterday,
	}
	issueDueNextWeek := &types.Issue{
		Title:     "Due next week",
		Priority:  1,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		DueAt:     &nextWeek,
	}
	issueOverdue := &types.Issue{
		Title:     "Overdue issue",
		Priority:  0,
		IssueType: types.TypeBug,
		Status:    types.StatusOpen,
		DueAt:     &yesterday,
	}
	issueOverdueClosed := &types.Issue{
		Title:     "Overdue but closed",
		Priority:  0,
		IssueType: types.TypeBug,
		Status:    types.StatusClosed,
		DueAt:     &yesterday,
		ClosedAt:  &now,
	}

	for _, issue := range []*types.Issue{
		issueNoSchedule, issueDeferredFuture, issueDeferredPast,
		issueDueNextWeek, issueOverdue, issueOverdueClosed,
	} {
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	t.Run("filter by deferred flag", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			Deferred: true,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find issues with defer_until set (future and past)
		if len(results) != 2 {
			t.Errorf("Expected 2 deferred issues, got %d", len(results))
		}
	})

	t.Run("filter by defer-after", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			DeferAfter: &now,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find issues deferred after now (tomorrow)
		if len(results) != 1 {
			t.Errorf("Expected 1 issue deferred after now, got %d", len(results))
		}
	})

	t.Run("filter by defer-before", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			DeferBefore: &now,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find issues deferred before now (yesterday)
		if len(results) != 1 {
			t.Errorf("Expected 1 issue deferred before now, got %d", len(results))
		}
	})

	t.Run("filter by due-after", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			DueAfter: &now,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find issues due after now (next week)
		if len(results) != 1 {
			t.Errorf("Expected 1 issue due after now, got %d", len(results))
		}
	})

	t.Run("filter by due-before", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			DueBefore: &now,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find issues due before now (overdue open + closed = 2)
		if len(results) != 2 {
			t.Errorf("Expected 2 issues due before now, got %d", len(results))
		}
	})

	t.Run("filter by overdue", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			Overdue: true,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find only the open overdue issue (not the closed one)
		if len(results) != 1 {
			t.Errorf("Expected 1 overdue issue, got %d", len(results))
		}
		if len(results) > 0 && results[0].ID != issueOverdue.ID {
			t.Errorf("Expected issue %s, got %s", issueOverdue.ID, results[0].ID)
		}
	})

	t.Run("combined filters defer and due", func(t *testing.T) {
		// Issue with both defer_until and due_at
		bothSet := &types.Issue{
			Title:      "Both deferred and due",
			Priority:   1,
			IssueType:  types.TypeTask,
			Status:     types.StatusOpen,
			DeferUntil: &tomorrow,
			DueAt:      &nextWeek,
		}
		if err := s.CreateIssue(ctx, bothSet, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		results, err := s.SearchIssues(ctx, "", types.IssueFilter{
			Deferred: true,
			DueAfter: &now,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should find the issue with both defer_until set and due_at > now
		if len(results) != 1 {
			t.Errorf("Expected 1 issue with both filters, got %d", len(results))
		}
	})
}

// TestHierarchicalChildren tests the --tree --parent functionality for showing all descendants
func TestHierarchicalChildren(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	store := newTestStore(t, testDB)
	ctx := context.Background()

	// Helper to create issue
	createIssue := func(title string, issueType types.IssueType) *types.Issue {
		issue := &types.Issue{
			Title:     title,
			Priority:  2,
			IssueType: issueType,
			Status:    types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue %s: %v", title, err)
		}
		return issue
	}

	// Helper to add dependency
	addDep := func(child, parent *types.Issue) {
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
			CreatedAt:   time.Now(),
			CreatedBy:   "test-user",
		}
		if err := store.AddDependency(ctx, dep, "test-user"); err != nil {
			t.Fatalf("Failed to add dependency %s -> %s: %v", child.ID, parent.ID, err)
		}
	}

	// Create test hierarchy: Parent -> Child1 (-> Grandchild1.1, Grandchild1.2) + Child2 (-> Grandchild2.1)
	parent := createIssue("Parent Epic", types.TypeEpic)
	child1 := createIssue("Child 1", types.TypeTask)
	child2 := createIssue("Child 2", types.TypeTask)
	grandchild11 := createIssue("Grandchild 1.1", types.TypeTask)
	grandchild12 := createIssue("Grandchild 1.2", types.TypeTask)
	grandchild21 := createIssue("Grandchild 2.1", types.TypeTask)

	addDep(child1, parent)
	addDep(child2, parent)
	addDep(grandchild11, child1)
	addDep(grandchild12, child1)
	addDep(grandchild21, child2)

	// Test full hierarchy (should return all 6 issues)
	t.Run("full_hierarchy", func(t *testing.T) {
		issues, err := getHierarchicalChildren(ctx, store, "", 0, parent.ID)
		if err != nil {
			t.Fatalf("getHierarchicalChildren failed: %v", err)
		}
		if len(issues) != 6 {
			t.Errorf("Expected 6 issues in hierarchy, got %d", len(issues))
		}
	})

	// Test child subset (should return child1 + its 2 grandchildren = 3 total)
	t.Run("child_subset", func(t *testing.T) {
		issues, err := getHierarchicalChildren(ctx, store, "", 0, child1.ID)
		if err != nil {
			t.Fatalf("getHierarchicalChildren for child1 failed: %v", err)
		}
		if len(issues) != 3 {
			t.Errorf("Expected 3 issues in child1 hierarchy, got %d", len(issues))
		}
	})

	// Test leaf node (should return only itself)
	t.Run("leaf_node", func(t *testing.T) {
		issues, err := getHierarchicalChildren(ctx, store, "", 0, grandchild11.ID)
		if err != nil {
			t.Fatalf("getHierarchicalChildren for leaf failed: %v", err)
		}
		if len(issues) != 1 || issues[0].ID != grandchild11.ID {
			t.Errorf("Expected 1 issue (leaf), got %d", len(issues))
		}
	})

	// Test error case - non-existent parent
	t.Run("nonexistent_parent", func(t *testing.T) {
		_, err := getHierarchicalChildren(ctx, store, "", 0, "nonexistent-id")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Error("Expected 'not found' error for nonexistent parent")
		}
	})
}
