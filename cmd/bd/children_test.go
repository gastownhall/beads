//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestChildrenIncludesClosedIssues verifies that the children command's
// behavior of setting status=all works correctly by testing the underlying
// list filter behavior. When a parent is specified, closed children should
// be included in results (GH#2477).
func TestChildrenIncludesClosedIssues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)

	// Create parent epic
	parent := &types.Issue{
		Title:     "Parent Epic",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("Failed to create parent: %v", err)
	}

	// Create two child tasks
	child1 := &types.Issue{
		Title:     "Child 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}
	child2 := &types.Issue{
		Title:     "Child 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	for _, child := range []*types.Issue{child1, child2} {
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("Failed to create child: %v", err)
		}
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("Failed to add parent-child dep: %v", err)
		}
	}

	// Close both children
	for _, child := range []*types.Issue{child1, child2} {
		if err := s.CloseIssue(ctx, child.ID, "done", "test-actor", "test-session"); err != nil {
			t.Fatalf("Failed to close %s: %v", child.ID, err)
		}
	}

	t.Run("DefaultFilterExcludesClosedChildren", func(t *testing.T) {
		// Default list filter excludes closed issues — this is the bug behavior
		filter := types.IssueFilter{
			ParentID:      &parent.ID,
			ExcludeStatus: []types.Status{types.StatusClosed, types.StatusPinned},
		}
		issues, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}
		if len(issues) != 0 {
			t.Errorf("Default filter returned %d issues, want 0 (closed children should be excluded by default filter)", len(issues))
		}
	})

	t.Run("AllStatusFilterIncludesClosedChildren", func(t *testing.T) {
		// When status=all (no ExcludeStatus), closed children should appear
		filter := types.IssueFilter{
			ParentID: &parent.ID,
		}
		issues, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues failed: %v", err)
		}
		if len(issues) != 2 {
			t.Errorf("All-status filter returned %d issues, want 2 (closed children should be included)", len(issues))
		}
	})
}
