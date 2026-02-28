//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

type labelTestHelper struct {
	s   *dolt.DoltStore
	ctx context.Context
	t   *testing.T
}

func (h *labelTestHelper) createIssue(title string, issueType types.IssueType, priority int) *types.Issue {
	issue := &types.Issue{
		Title:     title,
		Priority:  priority,
		IssueType: issueType,
		Status:    types.StatusOpen,
	}
	if err := h.s.CreateIssue(h.ctx, issue, "test-user"); err != nil {
		h.t.Fatalf("Failed to create issue: %v", err)
	}
	return issue
}

func (h *labelTestHelper) addLabel(issueID, label string) {
	if err := h.s.AddLabel(h.ctx, issueID, label, "test-user"); err != nil {
		h.t.Fatalf("Failed to add label '%s': %v", label, err)
	}
}

func (h *labelTestHelper) addLabels(issueID string, labels []string) {
	for _, label := range labels {
		h.addLabel(issueID, label)
	}
}

func (h *labelTestHelper) removeLabel(issueID, label string) {
	if err := h.s.RemoveLabel(h.ctx, issueID, label, "test-user"); err != nil {
		h.t.Fatalf("Failed to remove label '%s': %v", label, err)
	}
}

func (h *labelTestHelper) getLabels(issueID string) []string {
	labels, err := h.s.GetLabels(h.ctx, issueID)
	if err != nil {
		h.t.Fatalf("Failed to get labels: %v", err)
	}
	return labels
}

func (h *labelTestHelper) assertLabelCount(issueID string, expected int) {
	labels := h.getLabels(issueID)
	if len(labels) != expected {
		h.t.Errorf("Expected %d labels, got %d", expected, len(labels))
	}
}

func (h *labelTestHelper) assertHasLabel(issueID, expected string) {
	labels := h.getLabels(issueID)
	for _, l := range labels {
		if l == expected {
			return
		}
	}
	h.t.Errorf("Expected label '%s' not found", expected)
}

func (h *labelTestHelper) assertHasLabels(issueID string, expected []string) {
	labels := h.getLabels(issueID)
	labelMap := make(map[string]bool)
	for _, l := range labels {
		labelMap[l] = true
	}
	for _, exp := range expected {
		if !labelMap[exp] {
			h.t.Errorf("Expected label '%s' not found", exp)
		}
	}
}

func (h *labelTestHelper) assertNotHasLabel(issueID, label string) {
	labels := h.getLabels(issueID)
	for _, l := range labels {
		if l == label {
			h.t.Errorf("Did not expect label '%s' but found it", label)
		}
	}
}

func (h *labelTestHelper) assertLabelEvent(issueID string, eventType types.EventType, labelName string) {
	events, err := h.s.GetEvents(h.ctx, issueID, 100)
	if err != nil {
		h.t.Fatalf("Failed to get events: %v", err)
	}

	expectedComment := ""
	if eventType == types.EventLabelAdded {
		expectedComment = "Added label: " + labelName
	} else if eventType == types.EventLabelRemoved {
		expectedComment = "Removed label: " + labelName
	}

	for _, e := range events {
		if e.EventType == eventType && e.Comment != nil && *e.Comment == expectedComment {
			return
		}
	}
	h.t.Errorf("Expected to find event %s for label %s", eventType, labelName)
}

func TestLabelCommands(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "bd-test-label-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &labelTestHelper{s: s, ctx: ctx, t: t}

	t.Run("add label to issue", func(t *testing.T) {
		issue := h.createIssue("Test Issue", types.TypeBug, 1)
		h.addLabel(issue.ID, "bug")
		h.assertLabelCount(issue.ID, 1)
		h.assertHasLabel(issue.ID, "bug")
	})

	t.Run("add multiple labels", func(t *testing.T) {
		issue := h.createIssue("Multi Label Issue", types.TypeFeature, 1)
		labels := []string{"feature", "high-priority", "needs-review"}
		h.addLabels(issue.ID, labels)
		h.assertLabelCount(issue.ID, 3)
		h.assertHasLabels(issue.ID, labels)
	})

	t.Run("add duplicate label is idempotent", func(t *testing.T) {
		issue := h.createIssue("Duplicate Label Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "duplicate")
		h.addLabel(issue.ID, "duplicate")
		h.assertLabelCount(issue.ID, 1)
	})

	t.Run("remove label from issue", func(t *testing.T) {
		issue := h.createIssue("Remove Label Test", types.TypeBug, 1)
		h.addLabel(issue.ID, "temporary")
		h.removeLabel(issue.ID, "temporary")
		h.assertLabelCount(issue.ID, 0)
	})

	t.Run("remove one of multiple labels", func(t *testing.T) {
		issue := h.createIssue("Multi Remove Test", types.TypeTask, 1)
		labels := []string{"label1", "label2", "label3"}
		h.addLabels(issue.ID, labels)
		h.removeLabel(issue.ID, "label2")
		h.assertLabelCount(issue.ID, 2)
		h.assertNotHasLabel(issue.ID, "label2")
	})

	t.Run("remove non-existent label is no-op", func(t *testing.T) {
		issue := h.createIssue("Remove Non-Existent Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "exists")
		h.removeLabel(issue.ID, "does-not-exist")
		h.assertLabelCount(issue.ID, 1)
	})

	t.Run("get labels for issue with no labels", func(t *testing.T) {
		issue := h.createIssue("No Labels Test", types.TypeTask, 1)
		h.assertLabelCount(issue.ID, 0)
	})

	t.Run("label operations create events", func(t *testing.T) {
		issue := h.createIssue("Event Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "test-label")
		h.removeLabel(issue.ID, "test-label")
		h.assertLabelEvent(issue.ID, types.EventLabelAdded, "test-label")
		h.assertLabelEvent(issue.ID, types.EventLabelRemoved, "test-label")
	})

	t.Run("labels persist after issue update", func(t *testing.T) {
		issue := h.createIssue("Persistence Test", types.TypeTask, 1)
		h.addLabel(issue.ID, "persistent")
		updates := map[string]interface{}{
			"description": "Updated description",
			"priority":    2,
		}
		if err := s.UpdateIssue(ctx, issue.ID, updates, "test-user"); err != nil {
			t.Fatalf("Failed to update issue: %v", err)
		}
		h.assertLabelCount(issue.ID, 1)
		h.assertHasLabel(issue.ID, "persistent")
	})

	t.Run("propagate label to children", func(t *testing.T) {
		parent := h.createIssue("Propagate Parent", types.TypeEpic, 1)
		h.addLabel(parent.ID, "branch:x")

		// Create 3 children with parent-child dependency
		children := make([]*types.Issue, 3)
		for i := 0; i < 3; i++ {
			childID, err := s.GetNextChildID(ctx, parent.ID)
			if err != nil {
				t.Fatalf("failed to get next child ID: %v", err)
			}
			child := &types.Issue{
				ID:        childID,
				Title:     fmt.Sprintf("Child %d", i),
				Priority:  2,
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
			}
			if err := s.CreateIssue(ctx, child, "test-user"); err != nil {
				t.Fatalf("failed to create child %d: %v", i, err)
			}
			dep := &types.Dependency{
				IssueID:     child.ID,
				DependsOnID: parent.ID,
				Type:        types.DepParentChild,
			}
			if err := s.AddDependency(ctx, dep, "test-user"); err != nil {
				t.Fatalf("failed to add parent-child dep: %v", err)
			}
			children[i] = child
		}

		// Propagate: find children via ParentID filter, add label
		parentID := parent.ID
		childIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{ParentID: &parentID})
		if err != nil {
			t.Fatalf("failed to search children: %v", err)
		}
		if len(childIssues) != 3 {
			t.Fatalf("expected 3 children, got %d", len(childIssues))
		}
		for _, ci := range childIssues {
			if err := s.AddLabel(ctx, ci.ID, "branch:x", "test-user"); err != nil {
				t.Fatalf("failed to propagate label: %v", err)
			}
		}

		// Verify all children have the label
		for _, child := range children {
			h.assertHasLabel(child.ID, "branch:x")
		}
	})

	t.Run("propagate skips children that already have label", func(t *testing.T) {
		parent := h.createIssue("Propagate Idempotent Parent", types.TypeEpic, 1)
		h.addLabel(parent.ID, "branch:y")

		// Create 2 children
		child1ID, err := s.GetNextChildID(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		child1 := &types.Issue{
			ID:        child1ID,
			Title:     "Child already labeled",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child1, "test-user"); err != nil {
			t.Fatalf("failed to create child1: %v", err)
		}
		dep1 := &types.Dependency{
			IssueID:     child1.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
		}
		if err := s.AddDependency(ctx, dep1, "test-user"); err != nil {
			t.Fatalf("failed to add dep: %v", err)
		}
		// Pre-label child1
		h.addLabel(child1.ID, "branch:y")

		child2ID, err := s.GetNextChildID(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		child2 := &types.Issue{
			ID:        child2ID,
			Title:     "Child not yet labeled",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child2, "test-user"); err != nil {
			t.Fatalf("failed to create child2: %v", err)
		}
		dep2 := &types.Dependency{
			IssueID:     child2.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
		}
		if err := s.AddDependency(ctx, dep2, "test-user"); err != nil {
			t.Fatalf("failed to add dep: %v", err)
		}

		// Propagate (AddLabel is idempotent)
		parentID := parent.ID
		childIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{ParentID: &parentID})
		if err != nil {
			t.Fatalf("failed to search children: %v", err)
		}
		for _, ci := range childIssues {
			if err := s.AddLabel(ctx, ci.ID, "branch:y", "test-user"); err != nil {
				t.Fatalf("failed to propagate label: %v", err)
			}
		}

		// Both should have label, child1 should still have only 1 instance
		h.assertHasLabel(child1.ID, "branch:y")
		h.assertLabelCount(child1.ID, 1)
		h.assertHasLabel(child2.ID, "branch:y")
		h.assertLabelCount(child2.ID, 1)
	})

	t.Run("propagate with no children is no-op", func(t *testing.T) {
		parent := h.createIssue("Propagate No Children", types.TypeEpic, 1)
		h.addLabel(parent.ID, "branch:z")

		parentID := parent.ID
		childIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{ParentID: &parentID})
		if err != nil {
			t.Fatalf("failed to search children: %v", err)
		}
		if len(childIssues) != 0 {
			t.Errorf("expected 0 children, got %d", len(childIssues))
		}
		// No error, just a no-op
	})

	t.Run("labels work with different issue types", func(t *testing.T) {
		issueTypes := []types.IssueType{
			types.TypeBug,
			types.TypeFeature,
			types.TypeTask,
			types.TypeEpic,
			types.TypeChore,
		}

		for _, issueType := range issueTypes {
			issue := h.createIssue("Type Test: "+string(issueType), issueType, 1)
			labelName := "type-" + string(issueType)
			h.addLabel(issue.ID, labelName)
			h.assertLabelCount(issue.ID, 1)
			h.assertHasLabel(issue.ID, labelName)
		}
	})
}
