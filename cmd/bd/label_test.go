package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestLabelCommands(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-label-*")
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

	t.Run("add label to issue", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Test Issue",
			Description: "Test description",
			Priority:    1,
			IssueType:   types.TypeBug,
			Status:      types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "bug", "test-user"); err != nil {
			t.Fatalf("Failed to add label: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(labels) != 1 {
			t.Errorf("Expected 1 label, got %d", len(labels))
		}
		if labels[0] != "bug" {
			t.Errorf("Expected label 'bug', got '%s'", labels[0])
		}
	})

	t.Run("add multiple labels", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Multi Label Issue",
			Description: "Test description",
			Priority:    1,
			IssueType:   types.TypeFeature,
			Status:      types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		labels := []string{"feature", "high-priority", "needs-review"}
		for _, label := range labels {
			if err := s.AddLabel(ctx, issue.ID, label, "test-user"); err != nil {
				t.Fatalf("Failed to add label '%s': %v", label, err)
			}
		}

		gotLabels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(gotLabels) != 3 {
			t.Errorf("Expected 3 labels, got %d", len(gotLabels))
		}

		labelMap := make(map[string]bool)
		for _, l := range gotLabels {
			labelMap[l] = true
		}

		for _, expected := range labels {
			if !labelMap[expected] {
				t.Errorf("Expected label '%s' not found", expected)
			}
		}
	})

	t.Run("add duplicate label is idempotent", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Duplicate Label Test",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "duplicate", "test-user"); err != nil {
			t.Fatalf("Failed to add label first time: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "duplicate", "test-user"); err != nil {
			t.Fatalf("Failed to add label second time: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(labels) != 1 {
			t.Errorf("Expected 1 label after duplicate add, got %d", len(labels))
		}
	})

	t.Run("remove label from issue", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Remove Label Test",
			Priority:  1,
			IssueType: types.TypeBug,
			Status:    types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "temporary", "test-user"); err != nil {
			t.Fatalf("Failed to add label: %v", err)
		}

		if err := s.RemoveLabel(ctx, issue.ID, "temporary", "test-user"); err != nil {
			t.Fatalf("Failed to remove label: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(labels) != 0 {
			t.Errorf("Expected 0 labels after removal, got %d", len(labels))
		}
	})

	t.Run("remove one of multiple labels", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Multi Remove Test",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		labels := []string{"label1", "label2", "label3"}
		for _, label := range labels {
			if err := s.AddLabel(ctx, issue.ID, label, "test-user"); err != nil {
				t.Fatalf("Failed to add label '%s': %v", label, err)
			}
		}

		if err := s.RemoveLabel(ctx, issue.ID, "label2", "test-user"); err != nil {
			t.Fatalf("Failed to remove label: %v", err)
		}

		gotLabels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(gotLabels) != 2 {
			t.Errorf("Expected 2 labels, got %d", len(gotLabels))
		}

		for _, l := range gotLabels {
			if l == "label2" {
				t.Error("Expected label2 to be removed, but it's still there")
			}
		}
	})

	t.Run("remove non-existent label is no-op", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Remove Non-Existent Test",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "exists", "test-user"); err != nil {
			t.Fatalf("Failed to add label: %v", err)
		}

		if err := s.RemoveLabel(ctx, issue.ID, "does-not-exist", "test-user"); err != nil {
			t.Fatalf("Failed to remove non-existent label: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(labels) != 1 {
			t.Errorf("Expected 1 label to remain, got %d", len(labels))
		}
	})

	t.Run("get labels for issue with no labels", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "No Labels Test",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels: %v", err)
		}

		if len(labels) != 0 {
			t.Errorf("Expected 0 labels, got %d", len(labels))
		}
	})

	t.Run("label operations create events", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Event Test",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "test-label", "test-user"); err != nil {
			t.Fatalf("Failed to add label: %v", err)
		}

		if err := s.RemoveLabel(ctx, issue.ID, "test-label", "test-user"); err != nil {
			t.Fatalf("Failed to remove label: %v", err)
		}

		events, err := s.GetEvents(ctx, issue.ID, 100)
		if err != nil {
			t.Fatalf("Failed to get events: %v", err)
		}

		foundAdd := false
		foundRemove := false
		for _, e := range events {
			if e.EventType == types.EventLabelAdded && e.Comment != nil && *e.Comment == "Added label: test-label" {
				foundAdd = true
			}
			if e.EventType == types.EventLabelRemoved && e.Comment != nil && *e.Comment == "Removed label: test-label" {
				foundRemove = true
			}
		}

		if !foundAdd {
			t.Error("Expected to find label_added event")
		}
		if !foundRemove {
			t.Error("Expected to find label_removed event")
		}
	})

	t.Run("labels persist after issue update", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Persistence Test",
			Description: "Original description",
			Priority:    1,
			IssueType:   types.TypeTask,
			Status:      types.StatusOpen,
		}

		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		if err := s.AddLabel(ctx, issue.ID, "persistent", "test-user"); err != nil {
			t.Fatalf("Failed to add label: %v", err)
		}

		updates := map[string]interface{}{
			"description": "Updated description",
			"priority":    2,
		}
		if err := s.UpdateIssue(ctx, issue.ID, updates, "test-user"); err != nil {
			t.Fatalf("Failed to update issue: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get labels after update: %v", err)
		}

		if len(labels) != 1 {
			t.Errorf("Expected 1 label after update, got %d", len(labels))
		}
		if labels[0] != "persistent" {
			t.Errorf("Expected label 'persistent', got '%s'", labels[0])
		}
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
			issue := &types.Issue{
				Title:     "Type Test: " + string(issueType),
				Priority:  1,
				IssueType: issueType,
				Status:    types.StatusOpen,
			}

			if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
				t.Fatalf("Failed to create %s issue: %v", issueType, err)
			}

			labelName := "type-" + string(issueType)
			if err := s.AddLabel(ctx, issue.ID, labelName, "test-user"); err != nil {
				t.Fatalf("Failed to add label to %s issue: %v", issueType, err)
			}

			labels, err := s.GetLabels(ctx, issue.ID)
			if err != nil {
				t.Fatalf("Failed to get labels for %s issue: %v", issueType, err)
			}

			if len(labels) != 1 || labels[0] != labelName {
				t.Errorf("Label mismatch for %s issue: expected [%s], got %v", issueType, labelName, labels)
			}
		}
	})
}
