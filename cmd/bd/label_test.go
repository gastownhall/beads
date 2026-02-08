package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

type labelTestHelper struct {
	s   *sqlite.SQLiteStorage
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
