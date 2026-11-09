//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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
		issueTypes := []string{"bug", "feature", "task", "epic", "chore", "decision"}
		expectedTypes := []types.IssueType{
			types.TypeBug,
			types.TypeFeature,
			types.TypeTask,
			types.TypeEpic,
			types.TypeChore,
			types.TypeDecision,
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

	// GH#5560 (bd-fom9ed): two dependency types on one target are refused
	// with the shared collision guidance before the form reads the parent,
	// reserves a child ID or writes an issue or an edge, so a refused create
	// consumes no child number and leaves no edge behind. An unknown type or
	// a typed entry with no target is refused the same way, with the parser's
	// own diagnostic.
	t.Run("InvalidDependenciesFailBeforeAnyWrite", func(t *testing.T) {
		parent := createFormIssueForTest(t, ctx, s, &createFormValues{Title: "Refusal parent", IssueType: "epic"})
		target := createFormIssueForTest(t, ctx, s, &createFormValues{Title: "Refusal target"})
		for _, tc := range []struct {
			deps    []string
			wantErr string
			wantGH  bool // the shared GH#4626 collision guidance
		}{
			{deps: []string{"blocked-by:" + target.ID, "related:" + target.ID}, wantErr: "cannot attach both", wantGH: true},
			{deps: []string{"related:" + target.ID, "blocks:" + target.ID}, wantErr: "cannot attach both", wantGH: true},
			{deps: []string{"blocks:" + target.ID, "related:" + target.ID}, wantErr: "cannot attach both", wantGH: true},
			{deps: []string{"discovered-from:" + target.ID, "blocked-by:" + target.ID}, wantErr: "cannot attach both", wantGH: true},
			{deps: []string{"frobs:" + target.ID}, wantErr: `unknown dependency type "frobs"`},
			{deps: []string{"owned-by:" + target.ID}, wantErr: `unknown dependency type "owned-by"`},
			{deps: []string{"blocks:"}, wantErr: `dependency "blocks:" names no issue`},
		} {
			fv := &createFormValues{Title: "Refused child", Priority: 2, IssueType: "task", ParentID: parent.ID, Dependencies: tc.deps}
			issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
			if err == nil {
				t.Errorf("CreateIssueFromFormValues(deps %q) created %s, want a refusal", tc.deps, issue.ID)
				continue
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("CreateIssueFromFormValues(deps %q) error %q, want %q", tc.deps, err, tc.wantErr)
			}
			if tc.wantGH && !strings.Contains(err.Error(), "GH#4626") {
				t.Errorf("CreateIssueFromFormValues(deps %q) error %q, want the shared GH#4626 guidance", tc.deps, err)
			}
		}
		if dependents, err := s.GetDependents(ctx, target.ID); err != nil {
			t.Fatalf("GetDependents %s: %v", target.ID, err)
		} else if len(dependents) != 0 {
			t.Errorf("refused creates left %d edges to %s", len(dependents), target.ID)
		}
		child := createFormIssueForTest(t, ctx, s, &createFormValues{Title: "Accepted child", ParentID: parent.ID})
		if want := parent.ID + ".1"; child.ID != want {
			t.Errorf("first accepted child of %s is %s, want %s: a refused create reserved a child ID", parent.ID, child.ID, want)
		}
	})

	// GH#5560 (bd-fom9ed): every blocking spelling the form takes makes the
	// new issue depend on its target with the canonical, gating blocks type.
	// The form's blocks:<id> keeps its historical direction, unlike --deps'.
	t.Run("BlockingSpellingsGateTheNewIssue", func(t *testing.T) {
		for _, spelling := range []string{"", "blocks:", "blocked-by:", "depends-on:"} {
			target := createFormIssueForTest(t, ctx, s, &createFormValues{Title: "Gate target " + spelling})
			child := createFormIssueForTest(t, ctx, s, &createFormValues{
				Title:        "Gated by " + spelling,
				Dependencies: []string{spelling + target.ID},
			})
			assertFormBlocksEdge(t, ctx, s, child.ID, target.ID)
		}
	})

	// GH#5560 (bd-fom9ed): spellings that resolve to the same edge collapse
	// to it instead of colliding as two types on one target.
	t.Run("EquivalentSpellingsCollapse", func(t *testing.T) {
		target := createFormIssueForTest(t, ctx, s, &createFormValues{Title: "Collapse target"})
		child := createFormIssueForTest(t, ctx, s, &createFormValues{
			Title:        "Collapse child",
			Dependencies: []string{"blocked-by:" + target.ID, "depends-on:" + target.ID, "blocks:" + target.ID, target.ID},
		})
		assertFormBlocksEdge(t, ctx, s, child.ID, target.ID)
	})

	// GH#5560 (bd-fom9ed): each equivalent pair on its own stores one blocks
	// edge, so no single pairing can collide as two types on one target.
	t.Run("EquivalentPairsCollapse", func(t *testing.T) {
		for _, pair := range [][2]string{
			{"blocked-by:", "depends-on:"},
			{"blocks:", "depends-on:"},
			{"", "blocked-by:"},
		} {
			target := createFormIssueForTest(t, ctx, s, &createFormValues{Title: "Pair target " + pair[0] + pair[1]})
			child := createFormIssueForTest(t, ctx, s, &createFormValues{
				Title:        "Pair child " + pair[0] + pair[1],
				Dependencies: []string{pair[0] + target.ID, pair[1] + target.ID},
			})
			assertFormBlocksEdge(t, ctx, s, child.ID, target.ID)
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

func TestCreateIssueFromFormValues_WithParent(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("ParentChildCreation", func(t *testing.T) {
		// Create parent (epic) first
		parentFv := &createFormValues{
			Title:     "Epic parent issue",
			Priority:  1,
			IssueType: "epic",
		}
		parent, err := CreateIssueFromFormValues(ctx, s, parentFv, "test")
		if err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create child via --parent
		childFv := &createFormValues{
			Title:     "Child bug under epic",
			Priority:  2,
			IssueType: "bug",
			ParentID:  parent.ID,
		}
		child, err := CreateIssueFromFormValues(ctx, s, childFv, "test")
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Child ID should be hierarchical (parent.1)
		if !strings.HasPrefix(child.ID, parent.ID+".") {
			t.Errorf("child ID %q should start with parent ID %q + '.'", child.ID, parent.ID)
		}

		// Verify parent-child dependency was created
		deps, err := s.GetDependencies(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}

		found := false
		for _, d := range deps {
			if d.ID == parent.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected parent-child dependency on %s, not found", parent.ID)
		}
	})

	t.Run("ParentNotFound", func(t *testing.T) {
		fv := &createFormValues{
			Title:     "Orphan child",
			Priority:  2,
			IssueType: "task",
			ParentID:  "nonexistent-id",
		}
		_, err := CreateIssueFromFormValues(ctx, s, fv, "test")
		if err == nil {
			t.Fatal("expected error for nonexistent parent, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got: %v", err)
		}
	})

	t.Run("ParentLabelInheritance", func(t *testing.T) {
		// Create parent with labels
		parentFv := &createFormValues{
			Title:     "Labeled parent",
			Priority:  1,
			IssueType: "epic",
			Labels:    []string{"team-a", "urgent"},
		}
		parent, err := CreateIssueFromFormValues(ctx, s, parentFv, "test")
		if err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create child with --parent and its own label
		childFv := &createFormValues{
			Title:     "Child inherits labels",
			Priority:  2,
			IssueType: "task",
			ParentID:  parent.ID,
			Labels:    []string{"child-only"},
		}
		child, err := CreateIssueFromFormValues(ctx, s, childFv, "test")
		if err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Child should have its own label plus inherited parent labels
		labels, err := s.GetLabels(ctx, child.ID)
		if err != nil {
			t.Fatalf("failed to get child labels: %v", err)
		}

		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}

		if !labelMap["child-only"] {
			t.Error("expected child's own label 'child-only'")
		}
		if !labelMap["team-a"] {
			t.Error("expected inherited label 'team-a'")
		}
		if !labelMap["urgent"] {
			t.Error("expected inherited label 'urgent'")
		}
		if len(labels) != 3 {
			t.Errorf("expected 3 labels (1 own + 2 inherited), got %d: %v", len(labels), labels)
		}
	})

	t.Run("MultipleChildrenUnderSameParent", func(t *testing.T) {
		// Create parent
		parentFv := &createFormValues{
			Title:     "Multi-child parent",
			Priority:  1,
			IssueType: "epic",
		}
		parent, err := CreateIssueFromFormValues(ctx, s, parentFv, "test")
		if err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		// Create two children
		child1Fv := &createFormValues{
			Title:     "First child",
			Priority:  2,
			IssueType: "task",
			ParentID:  parent.ID,
		}
		child1, err := CreateIssueFromFormValues(ctx, s, child1Fv, "test")
		if err != nil {
			t.Fatalf("failed to create child1: %v", err)
		}

		child2Fv := &createFormValues{
			Title:     "Second child",
			Priority:  2,
			IssueType: "task",
			ParentID:  parent.ID,
		}
		child2, err := CreateIssueFromFormValues(ctx, s, child2Fv, "test")
		if err != nil {
			t.Fatalf("failed to create child2: %v", err)
		}

		// Children should have distinct IDs
		if child1.ID == child2.ID {
			t.Errorf("children should have distinct IDs, both got %q", child1.ID)
		}

		// Both should be under the parent
		if !strings.HasPrefix(child1.ID, parent.ID+".") {
			t.Errorf("child1 ID %q should start with %q", child1.ID, parent.ID+".")
		}
		if !strings.HasPrefix(child2.ID, parent.ID+".") {
			t.Errorf("child2 ID %q should start with %q", child2.ID, parent.ID+".")
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

// createFormIssueForTest creates an issue through CreateIssueFromFormValues,
// defaulting the fields the dependency tests do not care about.
func createFormIssueForTest(t *testing.T, ctx context.Context, s storage.DoltStorage, fv *createFormValues) *types.Issue {
	t.Helper()
	if fv.IssueType == "" {
		fv.IssueType = "task"
	}
	if fv.Priority == 0 {
		fv.Priority = 2
	}
	issue, err := CreateIssueFromFormValues(ctx, s, fv, "test")
	if err != nil {
		t.Fatalf("CreateIssueFromFormValues(%q, deps %q): %v", fv.Title, fv.Dependencies, err)
	}
	return issue
}

// assertFormBlocksEdge checks that from has exactly one dependency, a blocks
// edge on to, that no edge runs the other way, and that the edge keeps from
// out of ready work while to is open.
func assertFormBlocksEdge(t *testing.T, ctx context.Context, s storage.DoltStorage, from, to string) {
	t.Helper()
	recs, err := s.GetDependencyRecords(ctx, from)
	if err != nil {
		t.Fatalf("GetDependencyRecords %s: %v", from, err)
	}
	if len(recs) != 1 || recs[0].DependsOnID != to || recs[0].Type != types.DepBlocks {
		for _, r := range recs {
			t.Logf("%s -> %s (%s)", r.IssueID, r.DependsOnID, r.Type)
		}
		t.Errorf("%s has %d dependencies, want one blocks edge on %s", from, len(recs), to)
	}
	back, err := s.GetDependencyRecords(ctx, to)
	if err != nil {
		t.Fatalf("GetDependencyRecords %s: %v", to, err)
	}
	for _, r := range back {
		t.Errorf("reversed edge written: %s -> %s (%s)", r.IssueID, r.DependsOnID, r.Type)
	}
	ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, issue := range ready {
		if issue.ID == from {
			t.Errorf("%s is ready while its blocker %s is open", from, to)
		}
	}
}
