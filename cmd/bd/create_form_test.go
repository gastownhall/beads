package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestParseFormInput(t *testing.T) {
	t.Run("BasicParsing", func(t *testing.T) {
		fv := parseCreateFormInput(&createFormRawInput{
			Title:       "Test Title",
			Description: "Test Description",
			IssueType:   "bug",
			Priority:    "1",
			Assignee:    "alice",
		})

		if fv.Title != "Test Title" {
			t.Errorf("expected title 'Test Title', got %q", fv.Title)
		}
		if fv.Description != "Test Description" {
			t.Errorf("expected description 'Test Description', got %q", fv.Description)
		}
		if fv.IssueType != "bug" {
			t.Errorf("expected issue type 'bug', got %q", fv.IssueType)
		}
		if fv.Priority != 1 {
			t.Errorf("expected priority 1, got %d", fv.Priority)
		}
		if fv.Assignee != "alice" {
			t.Errorf("expected assignee 'alice', got %q", fv.Assignee)
		}
	})

	t.Run("PriorityParsing", func(t *testing.T) {
		// Valid priority
		fv := parseCreateFormInput(&createFormRawInput{Title: "Title", IssueType: "task", Priority: "0"})
		if fv.Priority != 0 {
			t.Errorf("expected priority 0, got %d", fv.Priority)
		}

		// Invalid priority defaults to 2
		fv = parseCreateFormInput(&createFormRawInput{Title: "Title", IssueType: "task", Priority: "invalid"})
		if fv.Priority != 2 {
			t.Errorf("expected default priority 2 for invalid input, got %d", fv.Priority)
		}

		// Empty priority defaults to 2
		fv = parseCreateFormInput(&createFormRawInput{Title: "Title", IssueType: "task", Priority: ""})
		if fv.Priority != 2 {
			t.Errorf("expected default priority 2 for empty input, got %d", fv.Priority)
		}
	})

	t.Run("LabelsParsing", func(t *testing.T) {
		fv := parseCreateFormInput(&createFormRawInput{
			Title:     "Title",
			IssueType: "task",
			Priority:  "2",
			Labels:    "bug, critical, needs-review",
		})

		if len(fv.Labels) != 3 {
			t.Fatalf("expected 3 labels, got %d", len(fv.Labels))
		}

		expected := []string{"bug", "critical", "needs-review"}
		for i, label := range expected {
			if fv.Labels[i] != label {
				t.Errorf("expected label %q at index %d, got %q", label, i, fv.Labels[i])
			}
		}
	})

	t.Run("LabelsWithEmptyValues", func(t *testing.T) {
		fv := parseCreateFormInput(&createFormRawInput{
			Title:     "Title",
			IssueType: "task",
			Priority:  "2",
			Labels:    "bug, , critical, ",
		})

		if len(fv.Labels) != 2 {
			t.Fatalf("expected 2 non-empty labels, got %d: %v", len(fv.Labels), fv.Labels)
		}
	})

	t.Run("DependenciesParsing", func(t *testing.T) {
		fv := parseCreateFormInput(&createFormRawInput{
			Title:     "Title",
			IssueType: "task",
			Priority:  "2",
			Deps:      "discovered-from:bd-20, blocks:bd-15",
		})

		if len(fv.Dependencies) != 2 {
			t.Fatalf("expected 2 dependencies, got %d", len(fv.Dependencies))
		}

		expected := []string{"discovered-from:bd-20", "blocks:bd-15"}
		for i, dep := range expected {
			if fv.Dependencies[i] != dep {
				t.Errorf("expected dependency %q at index %d, got %q", dep, i, fv.Dependencies[i])
			}
		}
	})

	t.Run("AllFields", func(t *testing.T) {
		fv := parseCreateFormInput(&createFormRawInput{
			Title:       "Full Issue",
			Description: "Detailed description",
			IssueType:   "feature",
			Priority:    "1",
			Assignee:    "bob",
			Labels:      "frontend, urgent",
			Design:      "Use React hooks",
			Acceptance:  "Tests pass, UI works",
			ExternalRef: "gh-123",
			Deps:        "blocks:bd-1",
		})

		if fv.Title != "Full Issue" {
			t.Errorf("unexpected title: %q", fv.Title)
		}
		if fv.Description != "Detailed description" {
			t.Errorf("unexpected description: %q", fv.Description)
		}
		if fv.IssueType != "feature" {
			t.Errorf("unexpected issue type: %q", fv.IssueType)
		}
		if fv.Priority != 1 {
			t.Errorf("unexpected priority: %d", fv.Priority)
		}
		if fv.Assignee != "bob" {
			t.Errorf("unexpected assignee: %q", fv.Assignee)
		}
		if len(fv.Labels) != 2 {
			t.Errorf("unexpected labels count: %d", len(fv.Labels))
		}
		if fv.Design != "Use React hooks" {
			t.Errorf("unexpected design: %q", fv.Design)
		}
		if fv.AcceptanceCriteria != "Tests pass, UI works" {
			t.Errorf("unexpected acceptance criteria: %q", fv.AcceptanceCriteria)
		}
		if fv.ExternalRef != "gh-123" {
			t.Errorf("unexpected external ref: %q", fv.ExternalRef)
		}
		if len(fv.Dependencies) != 1 {
			t.Errorf("unexpected dependencies count: %d", len(fv.Dependencies))
		}
	})
}

func TestCreateIssueFromFormValues(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("BasicIssue", func(t *testing.T) {
		fv := &createFormValues{
			Title:     "Test Form Issue",
			Priority:  1,
			IssueType: "bug",
		}

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		if issue.Title != "Test Form Issue" {
			t.Errorf("expected title 'Test Form Issue', got %q", issue.Title)
		}
		if issue.Priority != 1 {
			t.Errorf("expected priority 1, got %d", issue.Priority)
		}
		if issue.IssueType != types.TypeBug {
			t.Errorf("expected type bug, got %s", issue.IssueType)
		}
		if issue.Status != types.StatusOpen {
			t.Errorf("expected status open, got %s", issue.Status)
		}
	})

	t.Run("WithDescription", func(t *testing.T) {
		fv := &createFormValues{
			Title:       "Issue with description",
			Description: "This is a detailed description",
			Priority:    2,
			IssueType:   "task",
		}

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		if issue.Description != "This is a detailed description" {
			t.Errorf("expected description, got %q", issue.Description)
		}
	})

	t.Run("WithDesignAndAcceptance", func(t *testing.T) {
		fv := &createFormValues{
			Title:              "Feature with design",
			Design:             "Use MVC pattern",
			AcceptanceCriteria: "All tests pass",
			IssueType:          "feature",
			Priority:           2,
		}

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		if issue.Design != "Use MVC pattern" {
			t.Errorf("expected design, got %q", issue.Design)
		}
		if issue.AcceptanceCriteria != "All tests pass" {
			t.Errorf("expected acceptance criteria, got %q", issue.AcceptanceCriteria)
		}
	})

	t.Run("WithAssignee", func(t *testing.T) {
		fv := &createFormValues{
			Title:     "Assigned issue",
			Assignee:  "alice",
			Priority:  1,
			IssueType: "task",
		}

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		if issue.Assignee != "alice" {
			t.Errorf("expected assignee 'alice', got %q", issue.Assignee)
		}
	})

	t.Run("WithExternalRef", func(t *testing.T) {
		fv := &createFormValues{
			Title:       "Issue with external ref",
			ExternalRef: "gh-123",
			Priority:    2,
			IssueType:   "bug",
		}

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		if issue.ExternalRef == nil {
			t.Fatal("expected external ref to be set")
		}
		if *issue.ExternalRef != "gh-123" {
			t.Errorf("expected external ref 'gh-123', got %q", *issue.ExternalRef)
		}
	})

	t.Run("WithLabels", func(t *testing.T) {
		fv := &createFormValues{
			Title:     "Issue with labels",
			Priority:  0,
			IssueType: "bug",
			Labels:    []string{"bug", "critical"},
		}

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
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
		// Create a parent issue first
		parentFv := &createFormValues{
			Title:     "Parent issue for deps",
			Priority:  1,
			IssueType: "task",
		}
		parent, err := CreateIssueFromFormValues(ctx, s, parentFv, "test")
		if err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create child with dependency
		childFv := &createFormValues{
			Title:        "Child issue",
			Priority:     1,
			IssueType:    "task",
			Dependencies: []string{parent.ID}, // Default blocks type
		}
		child, err := CreateIssueFromFormValues(ctx, s, childFv, "test")
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		deps, err := s.GetDependencies(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		if len(deps) == 0 {
			t.Fatal("expected at least 1 dependency, got 0")
		}

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

	t.Run("WithTypedDependencies", func(t *testing.T) {
		// Create a parent issue
		parentFv := &createFormValues{
			Title:     "Related parent",
			Priority:  1,
			IssueType: "task",
		}
		parent, err := CreateIssueFromFormValues(ctx, s, parentFv, "test")
		if err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create child with typed dependency
		childFv := &createFormValues{
			Title:        "Child with typed dep",
			Priority:     1,
			IssueType:    "bug",
			Dependencies: []string{"discovered-from:" + parent.ID},
		}
		child, err := CreateIssueFromFormValues(ctx, s, childFv, "test")
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		deps, err := s.GetDependencies(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		if len(deps) == 0 {
			t.Fatal("expected at least 1 dependency, got 0")
		}

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

	t.Run("AllIssueTypes", func(t *testing.T) {
		issueTypes := []string{"bug", "feature", "task", "epic", "chore"}
		expectedTypes := []types.IssueType{
			types.TypeBug,
			types.TypeFeature,
			types.TypeTask,
			types.TypeEpic,
			types.TypeChore,
		}

		for i, issueType := range issueTypes {
			fv := &createFormValues{
				Title:     "Test " + issueType,
				IssueType: issueType,
				Priority:  2,
			}

			issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
			if err != nil {
				t.Fatalf("failed to create issue type %s: %v", issueType, err)
			}

			if issue.IssueType != expectedTypes[i] {
				t.Errorf("expected type %s, got %s", expectedTypes[i], issue.IssueType)
			}
		}
	})

	t.Run("MultipleDependencies", func(t *testing.T) {
		// Create two parent issues
		parent1Fv := &createFormValues{
			Title:     "Multi-dep Parent 1",
			Priority:  1,
			IssueType: "task",
		}
		parent1, err := CreateIssueFromFormValues(ctx, s, parent1Fv, "test")
		if err != nil {
			t.Fatalf("failed to create parent1: %v", err)
		}

		parent2Fv := &createFormValues{
			Title:     "Multi-dep Parent 2",
			Priority:  1,
			IssueType: "task",
		}
		parent2, err := CreateIssueFromFormValues(ctx, s, parent2Fv, "test")
		if err != nil {
			t.Fatalf("failed to create parent2: %v", err)
		}

		// Create child with multiple dependencies
		childFv := &createFormValues{
			Title:        "Multi-dep Child",
			Priority:     1,
			IssueType:    "task",
			Dependencies: []string{"blocks:" + parent1.ID, "related:" + parent2.ID},
		}
		child, err := CreateIssueFromFormValues(ctx, s, childFv, "test")
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		deps, err := s.GetDependencies(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		if len(deps) < 2 {
			t.Fatalf("expected at least 2 dependencies, got %d", len(deps))
		}

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

	t.Run("DiscoveredFromInheritsSourceRepo", func(t *testing.T) {
		// Create a parent issue with a custom source_repo
		parent := &types.Issue{
			Title:      "Parent with source repo",
			Priority:   1,
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			SourceRepo: "/path/to/custom/repo",
		}

		if err := s.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create a discovered issue with discovered-from dependency
		childFv := &createFormValues{
			Title:        "Discovered bug",
			Priority:     1,
			IssueType:    "bug",
			Dependencies: []string{"discovered-from:" + parent.ID},
		}
		child, err := CreateIssueFromFormValues(ctx, s, childFv, "test")
		if err != nil {
			t.Fatalf("failed to create discovered issue: %v", err)
		}

		// Verify the discovered issue inherited the source_repo
		retrievedIssue, err := s.GetIssue(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get discovered issue: %v", err)
		}

		if retrievedIssue.SourceRepo != parent.SourceRepo {
			t.Errorf("expected source_repo %q, got %q", parent.SourceRepo, retrievedIssue.SourceRepo)
		}
	})

	t.Run("AllPriorities", func(t *testing.T) {
		for priority := 0; priority <= 4; priority++ {
			fv := &createFormValues{
				Title:     "Priority test",
				IssueType: "task",
				Priority:  priority,
			}

			issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
			if err != nil {
				t.Fatalf("failed to create issue with priority %d: %v", priority, err)
			}

			if issue.Priority != priority {
				t.Errorf("expected priority %d, got %d", priority, issue.Priority)
			}
		}
	})
}

func TestFormValuesIntegration(t *testing.T) {
	// Test the full flow: parseCreateFormInput -> CreateIssueFromFormValues
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("FullFlow", func(t *testing.T) {
		// Simulate form input
		fv := parseCreateFormInput(&createFormRawInput{
			Title:       "Integration Test Issue",
			Description: "Testing the full flow from form to storage",
			IssueType:   "feature",
			Priority:    "1",
			Assignee:    "test-user",
			Labels:      "integration, test",
			Design:      "Design notes here",
			Acceptance:  "Should work end to end",
			ExternalRef: "gh-999",
		})

		issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Verify issue was stored
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to retrieve issue: %v", err)
		}

		if retrieved.Title != "Integration Test Issue" {
			t.Errorf("unexpected title: %q", retrieved.Title)
		}
		if retrieved.Description != "Testing the full flow from form to storage" {
			t.Errorf("unexpected description: %q", retrieved.Description)
		}
		if retrieved.IssueType != types.TypeFeature {
			t.Errorf("unexpected type: %s", retrieved.IssueType)
		}
		if retrieved.Priority != 1 {
			t.Errorf("unexpected priority: %d", retrieved.Priority)
		}
		if retrieved.Assignee != "test-user" {
			t.Errorf("unexpected assignee: %q", retrieved.Assignee)
		}
		if retrieved.Design != "Design notes here" {
			t.Errorf("unexpected design: %q", retrieved.Design)
		}
		if retrieved.AcceptanceCriteria != "Should work end to end" {
			t.Errorf("unexpected acceptance criteria: %q", retrieved.AcceptanceCriteria)
		}
		if retrieved.ExternalRef == nil || *retrieved.ExternalRef != "gh-999" {
			t.Errorf("unexpected external ref: %v", retrieved.ExternalRef)
		}

		// Check labels
		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get labels: %v", err)
		}
		if len(labels) != 2 {
			t.Errorf("expected 2 labels, got %d", len(labels))
		}
	})
}
