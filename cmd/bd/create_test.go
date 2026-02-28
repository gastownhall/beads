//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestCreateSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("BasicIssue", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Test Issue",
			Priority:  1,
			IssueType: types.TypeBug,
			Status:    types.StatusOpen,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		if len(issues) == 0 {
			t.Fatal("expected at least 1 issue, got 0")
		}

		// Find our issue
		var created *types.Issue
		for _, iss := range issues {
			if iss.Title == "Test Issue" {
				created = iss
				break
			}
		}
		if created == nil {
			t.Fatal("could not find created issue")
		}

		if created.Title != "Test Issue" {
			t.Errorf("expected title 'Test Issue', got %q", created.Title)
		}
		if created.Priority != 1 {
			t.Errorf("expected priority 1, got %d", created.Priority)
		}
		if created.IssueType != types.TypeBug {
			t.Errorf("expected type bug, got %s", created.IssueType)
		}
	})

	t.Run("WithDescription", func(t *testing.T) {
		issue := &types.Issue{
			Title:       "Issue with desc",
			Description: "This is a description",
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Find our issue
		var created *types.Issue
		for _, iss := range issues {
			if iss.Title == "Issue with desc" {
				created = iss
				break
			}
		}
		if created == nil {
			t.Fatal("could not find created issue")
		}

		if created.Description != "This is a description" {
			t.Errorf("expected description, got %q", created.Description)
		}
	})

	t.Run("WithDesignAndAcceptance", func(t *testing.T) {
		issue := &types.Issue{
			Title:              "Feature with design",
			Design:             "Use MVC pattern",
			AcceptanceCriteria: "All tests pass",
			IssueType:          types.TypeFeature,
			Priority:           2,
			Status:             types.StatusOpen,
			CreatedAt:          time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Find our issue
		var created *types.Issue
		for _, iss := range issues {
			if iss.Title == "Feature with design" {
				created = iss
				break
			}
		}
		if created == nil {
			t.Fatal("could not find created issue")
		}

		if created.Design != "Use MVC pattern" {
			t.Errorf("expected design, got %q", created.Design)
		}
		if created.AcceptanceCriteria != "All tests pass" {
			t.Errorf("expected acceptance criteria, got %q", created.AcceptanceCriteria)
		}
	})

	t.Run("WithLabels", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Issue with labels",
			Priority:  0,
			Status:    types.StatusOpen,
			IssueType: types.TypeBug,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Add labels
		if err := s.AddLabel(ctx, issue.ID, "bug", "test"); err != nil {
			t.Fatalf("failed to add bug label: %v", err)
		}
		if err := s.AddLabel(ctx, issue.ID, "critical", "test"); err != nil {
			t.Fatalf("failed to add critical label: %v", err)
		}

		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get labels: %v", err)
		}

		if len(labels) != 2 {
			t.Errorf("expected 2 labels, got %d", len(labels))
		}

		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}

		if !labelMap["bug"] || !labelMap["critical"] {
			t.Errorf("expected labels 'bug' and 'critical', got %v", labels)
		}
	})

	t.Run("WithDependencies", func(t *testing.T) {
		parent := &types.Issue{
			Title:     "Parent issue",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		child := &types.Issue{
			Title:     "Child issue",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Add dependency
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}

		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}

		deps, err := s.GetDependencies(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		if len(deps) == 0 {
			t.Fatal("expected at least 1 dependency, got 0")
		}

		// Find the dependency on parent
		found := false
		for _, d := range deps {
			if d.ID == parent.ID {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("expected dependency on %s, not found", parent.ID)
		}
	})

	t.Run("WithDiscoveredFromDependency", func(t *testing.T) {
		parent := &types.Issue{
			Title:     "Parent work",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		discovered := &types.Issue{
			Title:     "Found bug",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeBug,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, discovered, "test"); err != nil {
			t.Fatalf("failed to create discovered issue: %v", err)
		}

		// Add discovered-from dependency
		dep := &types.Dependency{
			IssueID:     discovered.ID,
			DependsOnID: parent.ID,
			Type:        types.DepDiscoveredFrom,
			CreatedAt:   time.Now(),
		}

		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}

		deps, err := s.GetDependencies(ctx, discovered.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		if len(deps) == 0 {
			t.Fatal("expected at least 1 dependency, got 0")
		}

		// Find the dependency on parent
		found := false
		for _, d := range deps {
			if d.ID == parent.ID {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("expected dependency on %s, not found", parent.ID)
		}
	})

	t.Run("WithExplicitID", func(t *testing.T) {
		issue := &types.Issue{
			ID:        "test-abc123",
			Title:     "Custom ID issue",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Find our issue
		found := false
		for _, iss := range issues {
			if iss.ID == "test-abc123" {
				found = true
				break
			}
		}

		if !found {
			t.Error("expected to find issue with ID 'test-abc123'")
		}
	})

	t.Run("WithAssignee", func(t *testing.T) {
		issue := &types.Issue{
			Title:     "Assigned issue",
			Assignee:  "alice",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Find our issue
		var created *types.Issue
		for _, iss := range issues {
			if iss.Title == "Assigned issue" {
				created = iss
				break
			}
		}
		if created == nil {
			t.Fatal("could not find created issue")
		}

		if created.Assignee != "alice" {
			t.Errorf("expected assignee 'alice', got %q", created.Assignee)
		}
	})

	t.Run("AllIssueTypes", func(t *testing.T) {
		issueTypes := []types.IssueType{
			types.TypeBug,
			types.TypeFeature,
			types.TypeTask,
			types.TypeEpic,
			types.TypeChore,
		}

		createdIDs := make(map[string]bool)
		for _, issueType := range issueTypes {
			issue := &types.Issue{
				Title:     "Test " + string(issueType),
				IssueType: issueType,
				Priority:  2,
				Status:    types.StatusOpen,
				CreatedAt: time.Now(),
			}

			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create issue type %s: %v", issueType, err)
			}
			createdIDs[issue.ID] = true
		}

		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Verify all 5 types were created
		foundCount := 0
		for _, iss := range issues {
			if createdIDs[iss.ID] {
				foundCount++
			}
		}

		if foundCount != 5 {
			t.Errorf("expected to find 5 created issues, found %d", foundCount)
		}
	})

	t.Run("MultipleDependencies", func(t *testing.T) {
		parent1 := &types.Issue{
			Title:     "Parent 1",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		parent2 := &types.Issue{
			Title:     "Parent 2",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		child := &types.Issue{
			Title:     "Child",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, parent1, "test"); err != nil {
			t.Fatalf("failed to create parent1: %v", err)
		}
		if err := s.CreateIssue(ctx, parent2, "test"); err != nil {
			t.Fatalf("failed to create parent2: %v", err)
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Add multiple dependencies
		dep1 := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent1.ID,
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}
		dep2 := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent2.ID,
			Type:        types.DepRelated,
			CreatedAt:   time.Now(),
		}

		if err := s.AddDependency(ctx, dep1, "test"); err != nil {
			t.Fatalf("failed to add dep1: %v", err)
		}
		if err := s.AddDependency(ctx, dep2, "test"); err != nil {
			t.Fatalf("failed to add dep2: %v", err)
		}

		deps, err := s.GetDependencies(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		if len(deps) < 2 {
			t.Fatalf("expected at least 2 dependencies, got %d", len(deps))
		}

		// Verify both parents are in dependencies
		foundParents := make(map[string]bool)
		for _, d := range deps {
			if d.ID == parent1.ID || d.ID == parent2.ID {
				foundParents[d.ID] = true
			}
		}

		if len(foundParents) != 2 {
			t.Errorf("expected to find both parent dependencies, found %d", len(foundParents))
		}
	})

	// GH#820: Tests for DueAt and DeferUntil fields
	t.Run("WithDueAt", func(t *testing.T) {
		// Create issue with due date
		dueTime := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour) // Due in 24 hours
		issue := &types.Issue{
			Title:     "Issue with due date",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			DueAt:     &dueTime,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue with due date: %v", err)
		}

		// Retrieve and verify
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.DueAt == nil {
			t.Fatal("expected DueAt to be set")
		}
		// Compare with 1-second tolerance for database round-trip
		diff := retrieved.DueAt.Sub(dueTime)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("DueAt mismatch: got %v, want %v", retrieved.DueAt, dueTime)
		}
	})

	t.Run("WithDeferUntil", func(t *testing.T) {
		// Create issue with defer_until
		deferTime := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour) // Defer for 2 hours
		issue := &types.Issue{
			Title:      "Issue with defer",
			Priority:   1,
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			DeferUntil: &deferTime,
			CreatedAt:  time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue with defer: %v", err)
		}

		// Retrieve and verify
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
		// Compare with 1-second tolerance for database round-trip
		diff := retrieved.DeferUntil.Sub(deferTime)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("DeferUntil mismatch: got %v, want %v", retrieved.DeferUntil, deferTime)
		}
	})

	t.Run("WithBothDueAndDefer", func(t *testing.T) {
		// Create issue with both due and defer
		dueTime := time.Now().Add(48 * time.Hour)   // Due in 48 hours
		deferTime := time.Now().Add(24 * time.Hour) // Defer for 24 hours
		issue := &types.Issue{
			Title:      "Issue with both due and defer",
			Priority:   1,
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			DueAt:      &dueTime,
			DeferUntil: &deferTime,
			CreatedAt:  time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Retrieve and verify both fields
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.DueAt == nil {
			t.Fatal("expected DueAt to be set")
		}
		if retrieved.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
	})

	t.Run("ParentChildLabelInheritance", func(t *testing.T) {
		// Create parent with labels
		parent := &types.Issue{
			Title:     "Epic parent with labels",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeEpic,
		}
		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}
		if err := s.AddLabel(ctx, parent.ID, "branch:feature-x", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}
		if err := s.AddLabel(ctx, parent.ID, "epic", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}

		// Create child via parent pattern (GetNextChildID + AddDependency)
		childID, err := s.GetNextChildID(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		child := &types.Issue{
			ID:        childID,
			Title:     "Child inherits labels",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepParentChild,
		}
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}

		// Simulate label inheritance: fetch parent labels and add to child
		parentLabels, err := s.GetLabels(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get parent labels: %v", err)
		}
		for _, l := range parentLabels {
			if err := s.AddLabel(ctx, child.ID, l, "test"); err != nil {
				t.Fatalf("failed to add inherited label %s: %v", l, err)
			}
		}

		// Verify child has both parent labels
		childLabels, err := s.GetLabels(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get child labels: %v", err)
		}
		labelMap := make(map[string]bool)
		for _, l := range childLabels {
			labelMap[l] = true
		}
		if !labelMap["branch:feature-x"] {
			t.Error("expected child to have label 'branch:feature-x'")
		}
		if !labelMap["epic"] {
			t.Error("expected child to have label 'epic'")
		}
		if len(childLabels) != 2 {
			t.Errorf("expected 2 labels, got %d: %v", len(childLabels), childLabels)
		}
	})

	t.Run("ParentChildLabelInheritanceMerge", func(t *testing.T) {
		// Parent has labels "a", "b". Child created with explicit "c".
		parent := &types.Issue{
			Title:     "Parent with a,b labels",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeEpic,
		}
		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}
		if err := s.AddLabel(ctx, parent.ID, "a", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}
		if err := s.AddLabel(ctx, parent.ID, "b", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}

		childID, err := s.GetNextChildID(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		child := &types.Issue{
			ID:        childID,
			Title:     "Child with explicit label c",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// User-specified labels
		userLabels := []string{"c", "a"} // "a" overlaps with parent

		// Simulate label inheritance merge (dedup)
		parentLabels, err := s.GetLabels(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get parent labels: %v", err)
		}
		merged := make(map[string]bool)
		for _, l := range userLabels {
			merged[l] = true
		}
		for _, l := range parentLabels {
			merged[l] = true
		}

		// Add all merged labels (AddLabel is idempotent)
		for l := range merged {
			if err := s.AddLabel(ctx, child.ID, l, "test"); err != nil {
				t.Fatalf("failed to add label %s: %v", l, err)
			}
		}

		childLabels, err := s.GetLabels(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get child labels: %v", err)
		}
		if len(childLabels) != 3 {
			t.Errorf("expected 3 labels (a, b, c), got %d: %v", len(childLabels), childLabels)
		}
		labelMap := make(map[string]bool)
		for _, l := range childLabels {
			labelMap[l] = true
		}
		for _, expected := range []string{"a", "b", "c"} {
			if !labelMap[expected] {
				t.Errorf("expected label %q not found", expected)
			}
		}
	})

	t.Run("ParentChildNoLabels", func(t *testing.T) {
		// Parent has no labels. Child should have no labels and no error.
		parent := &types.Issue{
			Title:     "Parent without labels",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeEpic,
		}
		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		childID, err := s.GetNextChildID(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		child := &types.Issue{
			ID:        childID,
			Title:     "Child of labelless parent",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Simulate label inheritance with no parent labels
		parentLabels, err := s.GetLabels(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get parent labels: %v", err)
		}
		if len(parentLabels) != 0 {
			t.Errorf("expected 0 parent labels, got %d", len(parentLabels))
		}

		childLabels, err := s.GetLabels(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get child labels: %v", err)
		}
		if len(childLabels) != 0 {
			t.Errorf("expected 0 child labels, got %d", len(childLabels))
		}
	})

	t.Run("ParentChildNoInheritLabels", func(t *testing.T) {
		// Simulate --no-inherit-labels: parent has labels, child gets only explicit ones
		parent := &types.Issue{
			Title:     "Parent with labels for no-inherit test",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeEpic,
		}
		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}
		if err := s.AddLabel(ctx, parent.ID, "branch:main", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}
		if err := s.AddLabel(ctx, parent.ID, "priority:high", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}

		childID, err := s.GetNextChildID(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		child := &types.Issue{
			ID:        childID,
			Title:     "Child without inherited labels",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := s.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// With --no-inherit-labels, only add explicit user labels
		userLabels := []string{"my-label"}
		for _, l := range userLabels {
			if err := s.AddLabel(ctx, child.ID, l, "test"); err != nil {
				t.Fatalf("failed to add label %s: %v", l, err)
			}
		}

		childLabels, err := s.GetLabels(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get child labels: %v", err)
		}
		if len(childLabels) != 1 {
			t.Errorf("expected 1 label, got %d: %v", len(childLabels), childLabels)
		}
		if len(childLabels) > 0 && childLabels[0] != "my-label" {
			t.Errorf("expected 'my-label', got %q", childLabels[0])
		}
		// Verify parent labels were NOT inherited
		labelMap := make(map[string]bool)
		for _, l := range childLabels {
			labelMap[l] = true
		}
		if labelMap["branch:main"] {
			t.Error("did not expect inherited label 'branch:main'")
		}
		if labelMap["priority:high"] {
			t.Error("did not expect inherited label 'priority:high'")
		}
	})

	t.Run("DiscoveredFromInheritsSourceRepo", func(t *testing.T) {
		// Create a parent issue with a custom source_repo
		parent := &types.Issue{
			Title:      "Parent issue",
			Priority:   1,
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			SourceRepo: "/path/to/custom/repo",
			CreatedAt:  time.Now(),
		}

		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create a discovered issue with discovered-from dependency
		// This should inherit the parent's source_repo
		discovered := &types.Issue{
			Title:     "Discovered bug",
			Priority:  1,
			Status:    types.StatusOpen,
			IssueType: types.TypeBug,
			CreatedAt: time.Now(),
		}

		// Simulate what happens in create.go when --deps discovered-from:parent is used
		// The source_repo should be inherited from the parent
		parentIssue, err := s.GetIssue(ctx, parent.ID)
		if err != nil {
			t.Fatalf("failed to get parent issue: %v", err)
		}
		if parentIssue.SourceRepo != "" {
			discovered.SourceRepo = parentIssue.SourceRepo
		}

		if err := s.CreateIssue(ctx, discovered, "test"); err != nil {
			t.Fatalf("failed to create discovered issue: %v", err)
		}

		// Add discovered-from dependency
		dep := &types.Dependency{
			IssueID:     discovered.ID,
			DependsOnID: parent.ID,
			Type:        types.DepDiscoveredFrom,
			CreatedAt:   time.Now(),
		}

		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}

		// Verify the discovered issue inherited the source_repo
		retrievedIssue, err := s.GetIssue(ctx, discovered.ID)
		if err != nil {
			t.Fatalf("failed to get discovered issue: %v", err)
		}

		if retrievedIssue.SourceRepo != parent.SourceRepo {
			t.Errorf("expected source_repo %q, got %q", parent.SourceRepo, retrievedIssue.SourceRepo)
		}

		if retrievedIssue.SourceRepo != "/path/to/custom/repo" {
			t.Errorf("expected source_repo '/path/to/custom/repo', got %q", retrievedIssue.SourceRepo)
		}
	})
}
