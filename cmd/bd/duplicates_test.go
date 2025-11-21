package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestFindDuplicateGroups(t *testing.T) {
	tests := []struct {
		name           string
		issues         []*types.Issue
		expectedGroups int
	}{
		{
			name: "no duplicates",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 2", Status: types.StatusOpen},
			},
			expectedGroups: 0,
		},
		{
			name: "simple duplicate",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 1", Status: types.StatusOpen},
			},
			expectedGroups: 1,
		},
		{
			name: "duplicate with different status ignored",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 1", Status: types.StatusClosed},
			},
			expectedGroups: 0,
		},
		{
			name: "multiple duplicates",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-3", Title: "Task 2", Status: types.StatusOpen},
				{ID: "bd-4", Title: "Task 2", Status: types.StatusOpen},
			},
			expectedGroups: 2,
		},
		{
			name: "different descriptions are duplicates if title matches",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Description: "Desc 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 1", Description: "Desc 2", Status: types.StatusOpen},
			},
			expectedGroups: 0, // Different descriptions = not duplicates
		},
		{
			name: "exact content match",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Description: "Desc 1", Design: "Design 1", AcceptanceCriteria: "AC 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 1", Description: "Desc 1", Design: "Design 1", AcceptanceCriteria: "AC 1", Status: types.StatusOpen},
			},
			expectedGroups: 1,
		},
		{
			name: "three-way duplicate",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-2", Title: "Task 1", Status: types.StatusOpen},
				{ID: "bd-3", Title: "Task 1", Status: types.StatusOpen},
			},
			expectedGroups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := findDuplicateGroups(tt.issues)
			if len(groups) != tt.expectedGroups {
				t.Errorf("findDuplicateGroups() returned %d groups, want %d", len(groups), tt.expectedGroups)
			}
		})
	}
}

func TestChooseMergeTarget(t *testing.T) {
	tests := []struct {
		name      string
		group     []*types.Issue
		refCounts map[string]int
		wantID    string
	}{
		{
			name: "choose by reference count",
			group: []*types.Issue{
				{ID: "bd-2", Title: "Task"},
				{ID: "bd-1", Title: "Task"},
			},
			refCounts: map[string]int{
				"bd-1": 5,
				"bd-2": 0,
			},
			wantID: "bd-1",
		},
		{
			name: "choose by lexicographic order if same references",
			group: []*types.Issue{
				{ID: "bd-2", Title: "Task"},
				{ID: "bd-1", Title: "Task"},
			},
			refCounts: map[string]int{
				"bd-1": 0,
				"bd-2": 0,
			},
			wantID: "bd-1",
		},
		{
			name: "prefer higher references even with larger ID",
			group: []*types.Issue{
				{ID: "bd-1", Title: "Task"},
				{ID: "bd-100", Title: "Task"},
			},
			refCounts: map[string]int{
				"bd-1":   1,
				"bd-100": 10,
			},
			wantID: "bd-100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := chooseMergeTarget(tt.group, tt.refCounts)
			if target.ID != tt.wantID {
				t.Errorf("chooseMergeTarget() = %v, want %v", target.ID, tt.wantID)
			}
		})
	}
}

func TestCountReferences(t *testing.T) {
	issues := []*types.Issue{
		{
			ID:          "bd-1",
			Description: "See bd-2 for details",
			Notes:       "Related to bd-3",
		},
		{
			ID:          "bd-2",
			Description: "Mentioned bd-1 twice: bd-1",
		},
		{
			ID:    "bd-3",
			Notes: "Nothing to see here",
		},
	}

	counts := countReferences(issues)

	expectedCounts := map[string]int{
		"bd-1": 2, // Referenced twice in bd-2
		"bd-2": 1, // Referenced once in bd-1
		"bd-3": 1, // Referenced once in bd-1
	}

	for id, expectedCount := range expectedCounts {
		if counts[id] != expectedCount {
			t.Errorf("countReferences()[%s] = %d, want %d", id, counts[id], expectedCount)
		}
	}
}

func TestDuplicateGroupsWithDifferentStatuses(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Task 1", Status: types.StatusOpen},
		{ID: "bd-2", Title: "Task 1", Status: types.StatusClosed},
		{ID: "bd-3", Title: "Task 1", Status: types.StatusOpen},
	}

	groups := findDuplicateGroups(issues)

	// Should have 1 group with bd-1 and bd-3 (both open)
	if len(groups) != 1 {
		t.Fatalf("Expected 1 group, got %d", len(groups))
	}

	if len(groups[0]) != 2 {
		t.Fatalf("Expected 2 issues in group, got %d", len(groups[0]))
	}

	// Verify bd-2 (closed) is not in the group
	for _, issue := range groups[0] {
		if issue.ID == "bd-2" {
			t.Errorf("bd-2 (closed) should not be in group with open issues")
		}
	}
}

func TestDuplicatesIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	testStore := newTestStore(t, tmpDir+"/.beads/beads.db")
	ctx := context.Background()

	// Create duplicate issues (let DB assign IDs)
	issues := []*types.Issue{
		{
			Title:       "Fix authentication bug",
			Description: "Users can't login",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeBug,
		},
		{
			Title:       "Fix authentication bug",
			Description: "Users can't login",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeBug,
		},
		{
			Title:       "Different task",
			Description: "Different description",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		},
	}

	for _, issue := range issues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Fetch all issues
	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	// Find duplicates
	groups := findDuplicateGroups(allIssues)

	if len(groups) != 1 {
		t.Fatalf("Expected 1 duplicate group, got %d", len(groups))
	}

	if len(groups[0]) != 2 {
		t.Fatalf("Expected 2 issues in group, got %d", len(groups[0]))
	}

	// Verify the duplicate group contains the two issues with "Fix authentication bug"
	dupCount := 0
	for _, issue := range groups[0] {
		if issue.Title == "Fix authentication bug" {
			dupCount++
		}
	}

	if dupCount != 2 {
		t.Errorf("Expected duplicate group to contain 2 'Fix authentication bug' issues, got %d", dupCount)
	}
}
