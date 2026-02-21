//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// Beads Template Tests (for bd template instantiate)
// =============================================================================

// TestExtractVariables tests the {{variable}} pattern extraction
func TestExtractVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "single variable", input: "Release {{version}}", expected: []string{"version"}},
		{name: "multiple variables", input: "Release {{version}} on {{date}}", expected: []string{"version", "date"}},
		{name: "no variables", input: "Just plain text", expected: nil},
		{name: "duplicate variables", input: "{{version}} and {{version}} again", expected: []string{"version"}},
		{name: "variable with underscore", input: "{{my_variable}}", expected: []string{"my_variable"}},
		{name: "variable with numbers", input: "{{var123}}", expected: []string{"var123"}},
		{name: "invalid variable format", input: "{{123invalid}}", expected: nil},
		{name: "empty braces", input: "{{}}", expected: nil},
		{name: "handlebars else keyword ignored", input: "{{ready}} then {{else}} or {{other}}", expected: []string{"ready", "other"}},
		{name: "handlebars this keyword ignored", input: "{{this}} and {{name}}", expected: []string{"name"}},
		{name: "multiple handlebars keywords ignored", input: "{{else}} {{this}} {{root}} {{index}} {{key}} {{first}} {{last}} {{actual_var}}", expected: []string{"actual_var"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractVariables(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("extractVariables(%q) = %v, want %v", tt.input, result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("extractVariables(%q)[%d] = %q, want %q", tt.input, i, v, tt.expected[i])
				}
			}
		})
	}
}

// TestSubstituteVariables tests the variable substitution
func TestSubstituteVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		vars     map[string]string
		expected string
	}{
		{name: "single variable", input: "Release {{version}}", vars: map[string]string{"version": "1.2.0"}, expected: "Release 1.2.0"},
		{name: "multiple variables", input: "Release {{version}} on {{date}}", vars: map[string]string{"version": "1.2.0", "date": "2024-01-15"}, expected: "Release 1.2.0 on 2024-01-15"},
		{name: "missing variable unchanged", input: "Release {{version}}", vars: map[string]string{}, expected: "Release {{version}}"},
		{name: "partial substitution", input: "{{found}} and {{missing}}", vars: map[string]string{"found": "yes"}, expected: "yes and {{missing}}"},
		{name: "no variables", input: "Just plain text", vars: map[string]string{"version": "1.0"}, expected: "Just plain text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := substituteVariables(tt.input, tt.vars)
			if result != tt.expected {
				t.Errorf("substituteVariables(%q, %v) = %q, want %q", tt.input, tt.vars, result, tt.expected)
			}
		})
	}
}

// templateTestHelper provides helpers for Beads template tests
type templateTestHelper struct {
	s   *dolt.DoltStore
	ctx context.Context
	t   *testing.T
}

func (h *templateTestHelper) createIssue(title, description string, issueType types.IssueType, priority int) *types.Issue {
	issue := &types.Issue{
		Title:       title,
		Description: description,
		Priority:    priority,
		IssueType:   issueType,
		Status:      types.StatusOpen,
	}
	if err := h.s.CreateIssue(h.ctx, issue, "test-user"); err != nil {
		h.t.Fatalf("Failed to create issue: %v", err)
	}
	return issue
}

func (h *templateTestHelper) addParentChild(childID, parentID string) {
	dep := &types.Dependency{
		IssueID:     childID,
		DependsOnID: parentID,
		Type:        types.DepParentChild,
	}
	if err := h.s.AddDependency(h.ctx, dep, "test-user"); err != nil {
		h.t.Fatalf("Failed to add parent-child dependency: %v", err)
	}
}

func (h *templateTestHelper) addLabel(issueID, label string) {
	if err := h.s.AddLabel(h.ctx, issueID, label, "test-user"); err != nil {
		h.t.Fatalf("Failed to add label: %v", err)
	}
}

// createIssueWithID creates an issue with a specific ID (for testing hierarchical IDs)
func (h *templateTestHelper) createIssueWithID(id, title, description string, issueType types.IssueType, priority int) *types.Issue {
	issue := &types.Issue{
		ID:          id,
		Title:       title,
		Description: description,
		Priority:    priority,
		IssueType:   issueType,
		Status:      types.StatusOpen,
	}
	if err := h.s.CreateIssue(h.ctx, issue, "test-user"); err != nil {
		h.t.Fatalf("Failed to create issue with ID %s: %v", id, err)
	}
	return issue
}

// TestTemplateSuite consolidates template loading, cloning, and variable extraction
// tests that share one DB to reduce Dolt store initialization overhead.
func TestTemplateSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	// --- LoadTemplateSubgraph tests ---

	t.Run("LoadTemplate_EpicWithNoChildren", func(t *testing.T) {
		epic := h.createIssue("Template Epic", "Description", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}
		if subgraph.Root.ID != epic.ID {
			t.Errorf("Root ID = %s, want %s", subgraph.Root.ID, epic.ID)
		}
		if len(subgraph.Issues) != 1 {
			t.Errorf("Issues count = %d, want 1", len(subgraph.Issues))
		}
	})

	t.Run("LoadTemplate_EpicWithChildren", func(t *testing.T) {
		epic := h.createIssue("Template {{name}}", "Epic for {{name}}", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssue("Task 1 for {{name}}", "", types.TypeTask, 2)
		child2 := h.createIssue("Task 2 for {{name}}", "", types.TypeTask, 2)
		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}
		if len(subgraph.Issues) != 3 {
			t.Errorf("Issues count = %d, want 3", len(subgraph.Issues))
		}
		vars := extractAllVariables(subgraph)
		if len(vars) != 1 || vars[0] != "name" {
			t.Errorf("Variables = %v, want [name]", vars)
		}
	})

	t.Run("LoadTemplate_NestedChildren", func(t *testing.T) {
		epic := h.createIssue("Nested Template", "", types.TypeEpic, 1)
		child := h.createIssue("Child Task", "", types.TypeTask, 2)
		grandchild := h.createIssue("Grandchild Task", "", types.TypeTask, 3)

		h.addParentChild(child.ID, epic.ID)
		h.addParentChild(grandchild.ID, child.ID)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}
		if len(subgraph.Issues) != 3 {
			t.Errorf("Issues count = %d, want 3 (epic + child + grandchild)", len(subgraph.Issues))
		}
	})

	// --- CloneSubgraph tests ---

	t.Run("Clone_SimpleTemplate", func(t *testing.T) {
		epic := h.createIssue("Release {{version}}", "Release notes for {{version}}", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		vars := map[string]string{"version": "2.0.0"}
		opts := CloneOptions{Vars: vars, Actor: "test-user"}
		result, err := cloneSubgraph(ctx, s, subgraph, opts)
		if err != nil {
			t.Fatalf("cloneSubgraph failed: %v", err)
		}

		if result.Created != 1 {
			t.Errorf("Created = %d, want 1", result.Created)
		}
		if result.NewEpicID == epic.ID {
			t.Error("NewEpicID should be different from template ID")
		}

		newEpic, err := s.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			t.Fatalf("Failed to get cloned issue: %v", err)
		}
		if newEpic.Title != "Release 2.0.0" {
			t.Errorf("Title = %q, want %q", newEpic.Title, "Release 2.0.0")
		}
		if newEpic.Description != "Release notes for 2.0.0" {
			t.Errorf("Description = %q, want %q", newEpic.Description, "Release notes for 2.0.0")
		}
	})

	t.Run("Clone_TemplateWithChildren", func(t *testing.T) {
		epic := h.createIssue("Deploy {{service}}", "", types.TypeEpic, 1)
		child1 := h.createIssue("Build {{service}}", "", types.TypeTask, 2)
		child2 := h.createIssue("Test {{service}}", "", types.TypeTask, 2)

		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		vars := map[string]string{"service": "api-gateway"}
		opts := CloneOptions{Vars: vars, Actor: "test-user"}
		result, err := cloneSubgraph(ctx, s, subgraph, opts)
		if err != nil {
			t.Fatalf("cloneSubgraph failed: %v", err)
		}
		if result.Created != 3 {
			t.Errorf("Created = %d, want 3", result.Created)
		}

		if _, ok := result.IDMapping[epic.ID]; !ok {
			t.Error("ID mapping missing epic")
		}
		if _, ok := result.IDMapping[child1.ID]; !ok {
			t.Error("ID mapping missing child1")
		}
		if _, ok := result.IDMapping[child2.ID]; !ok {
			t.Error("ID mapping missing child2")
		}

		newEpic, err := s.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			t.Fatalf("Failed to get cloned epic: %v", err)
		}
		if newEpic.Title != "Deploy api-gateway" {
			t.Errorf("Epic title = %q, want %q", newEpic.Title, "Deploy api-gateway")
		}

		deps, err := s.GetDependencyRecords(ctx, result.IDMapping[child1.ID])
		if err != nil {
			t.Fatalf("Failed to get dependencies: %v", err)
		}
		hasParentChild := false
		for _, dep := range deps {
			if dep.DependsOnID == result.NewEpicID && dep.Type == types.DepParentChild {
				hasParentChild = true
				break
			}
		}
		if !hasParentChild {
			t.Error("Cloned child should have parent-child dependency on cloned epic")
		}
	})

	t.Run("Clone_StartsWithOpenStatus", func(t *testing.T) {
		epic := h.createIssue("Template", "", types.TypeEpic, 1)
		err := s.UpdateIssue(ctx, epic.ID, map[string]interface{}{"status": "in_progress"}, "test-user")
		if err != nil {
			t.Fatalf("Failed to update status: %v", err)
		}

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		opts := CloneOptions{Actor: "test-user"}
		result, err := cloneSubgraph(ctx, s, subgraph, opts)
		if err != nil {
			t.Fatalf("cloneSubgraph failed: %v", err)
		}

		newEpic, err := s.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			t.Fatalf("Failed to get cloned issue: %v", err)
		}
		if newEpic.Status != types.StatusOpen {
			t.Errorf("Status = %s, want %s", newEpic.Status, types.StatusOpen)
		}
	})

	t.Run("Clone_AssigneeOverrideRootOnly", func(t *testing.T) {
		epic := h.createIssue("Root Epic", "", types.TypeEpic, 1)
		child := h.createIssue("Child Task", "", types.TypeTask, 2)
		h.addParentChild(child.ID, epic.ID)

		err := s.UpdateIssue(ctx, epic.ID, map[string]interface{}{"assignee": "template-owner"}, "test-user")
		if err != nil {
			t.Fatalf("Failed to set epic assignee: %v", err)
		}
		err = s.UpdateIssue(ctx, child.ID, map[string]interface{}{"assignee": "child-owner"}, "test-user")
		if err != nil {
			t.Fatalf("Failed to set child assignee: %v", err)
		}

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		opts := CloneOptions{Assignee: "new-assignee", Actor: "test-user"}
		result, err := cloneSubgraph(ctx, s, subgraph, opts)
		if err != nil {
			t.Fatalf("cloneSubgraph failed: %v", err)
		}

		newEpic, err := s.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			t.Fatalf("Failed to get cloned epic: %v", err)
		}
		if newEpic.Assignee != "new-assignee" {
			t.Errorf("Epic assignee = %q, want %q", newEpic.Assignee, "new-assignee")
		}

		newChildID := result.IDMapping[child.ID]
		newChild, err := s.GetIssue(ctx, newChildID)
		if err != nil {
			t.Fatalf("Failed to get cloned child: %v", err)
		}
		if newChild.Assignee != "child-owner" {
			t.Errorf("Child assignee = %q, want %q", newChild.Assignee, "child-owner")
		}
	})

	// --- ExtractAllVariables tests ---

	t.Run("ExtractAllVariables", func(t *testing.T) {
		epic := h.createIssue("Release {{version}}", "For {{product}}", types.TypeEpic, 1)
		child := h.createIssue("Deploy to {{environment}}", "", types.TypeTask, 2)
		h.addParentChild(child.ID, epic.ID)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		vars := extractAllVariables(subgraph)
		varMap := make(map[string]bool)
		for _, v := range vars {
			varMap[v] = true
		}
		if !varMap["version"] {
			t.Error("Missing variable: version")
		}
		if !varMap["product"] {
			t.Error("Missing variable: product")
		}
		if !varMap["environment"] {
			t.Error("Missing variable: environment")
		}
	})

	// --- LoadTemplateSubgraphWithManyChildren tests (bd-c8d5) ---

	t.Run("ManyChildren_4Children", func(t *testing.T) {
		epic := h.createIssue("Proto Workflow", "Workflow with 4 steps", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssue("load-context", "", types.TypeTask, 2)
		child2 := h.createIssue("implement", "", types.TypeTask, 2)
		child3 := h.createIssue("self-review", "", types.TypeTask, 2)
		child4 := h.createIssue("request-shutdown", "", types.TypeTask, 2)

		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)
		h.addParentChild(child3.ID, epic.ID)
		h.addParentChild(child4.ID, epic.ID)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		if len(subgraph.Issues) != 5 {
			t.Errorf("Issues count = %d, want 5 (epic + 4 children)", len(subgraph.Issues))
			for _, iss := range subgraph.Issues {
				t.Logf("  - %s: %s", iss.ID, iss.Title)
			}
		}

		childIDs := []string{child1.ID, child2.ID, child3.ID, child4.ID}
		for _, childID := range childIDs {
			if _, ok := subgraph.IssueMap[childID]; !ok {
				t.Errorf("Child %s not found in subgraph", childID)
			}
		}
	})

	t.Run("ManyChildren_CloneCreatesAll4", func(t *testing.T) {
		epic := h.createIssue("Polecat Work", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssue("load-context", "", types.TypeTask, 2)
		child2 := h.createIssue("implement", "", types.TypeTask, 2)
		child3 := h.createIssue("self-review", "", types.TypeTask, 2)
		child4 := h.createIssue("request-shutdown", "", types.TypeTask, 2)

		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)
		h.addParentChild(child3.ID, epic.ID)
		h.addParentChild(child4.ID, epic.ID)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		opts := CloneOptions{Actor: "test-user"}
		result, err := cloneSubgraph(ctx, s, subgraph, opts)
		if err != nil {
			t.Fatalf("cloneSubgraph failed: %v", err)
		}

		if result.Created != 5 {
			t.Errorf("Created = %d, want 5", result.Created)
		}
		for _, childID := range []string{child1.ID, child2.ID, child3.ID, child4.ID} {
			if _, ok := result.IDMapping[childID]; !ok {
				t.Errorf("Child %s not in ID mapping", childID)
			}
		}
	})

	t.Run("ManyChildren_HierarchicalIDs", func(t *testing.T) {
		epic := h.createIssueWithID("test-lwuu", "mol-polecat-work", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssueWithID("test-lwuu.1", "load-context", "", types.TypeTask, 2)
		child2 := h.createIssueWithID("test-lwuu.2", "implement", "", types.TypeTask, 2)
		child3 := h.createIssueWithID("test-lwuu.3", "self-review", "", types.TypeTask, 2)
		child8 := h.createIssueWithID("test-lwuu.8", "request-shutdown", "", types.TypeTask, 2)

		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)
		h.addParentChild(child3.ID, epic.ID)
		h.addParentChild(child8.ID, epic.ID)

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		if len(subgraph.Issues) != 5 {
			t.Errorf("Issues count = %d, want 5", len(subgraph.Issues))
			for _, iss := range subgraph.Issues {
				t.Logf("  - %s: %s", iss.ID, iss.Title)
			}
		}

		for _, childID := range []string{"test-lwuu.1", "test-lwuu.2", "test-lwuu.3", "test-lwuu.8"} {
			if _, ok := subgraph.IssueMap[childID]; !ok {
				t.Errorf("Child %s not found in subgraph", childID)
			}
		}
	})

	t.Run("ManyChildren_WrongDepTypeNotLoaded", func(t *testing.T) {
		epic := h.createIssue("Proto with mixed deps", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssue("load-context", "", types.TypeTask, 2)
		child2 := h.createIssue("implement", "", types.TypeTask, 2)
		child3 := h.createIssue("self-review", "", types.TypeTask, 2)
		child4 := h.createIssue("request-shutdown", "", types.TypeTask, 2)

		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)

		// child3 and child4 have "blocks" dependency (wrong type)
		for _, childID := range []string{child3.ID, child4.ID} {
			blocksDep := &types.Dependency{IssueID: childID, DependsOnID: epic.ID, Type: types.DepBlocks}
			if err := s.AddDependency(ctx, blocksDep, "test-user"); err != nil {
				t.Fatalf("Failed to add blocks dependency: %v", err)
			}
		}

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		if len(subgraph.Issues) != 3 {
			t.Errorf("Expected 3 issues (without hierarchical ID fallback), got %d", len(subgraph.Issues))
		}
	})

	t.Run("ManyChildren_HierarchicalWrongDepTypeLoaded", func(t *testing.T) {
		epic := h.createIssueWithID("test-pcat", "Proto with mixed deps", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssueWithID("test-pcat.1", "load-context", "", types.TypeTask, 2)
		child2 := h.createIssueWithID("test-pcat.2", "implement", "", types.TypeTask, 2)
		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)

		// child3 has NO dependency at all (broken data)
		_ = h.createIssueWithID("test-pcat.3", "self-review", "", types.TypeTask, 2)

		// child8 has wrong dependency type
		child8 := h.createIssueWithID("test-pcat.8", "request-shutdown", "", types.TypeTask, 2)
		blocksDep := &types.Dependency{IssueID: child8.ID, DependsOnID: epic.ID, Type: types.DepBlocks}
		if err := s.AddDependency(ctx, blocksDep, "test-user"); err != nil {
			t.Fatalf("Failed to add blocks dependency: %v", err)
		}

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		if len(subgraph.Issues) != 5 {
			t.Errorf("Expected 5 issues (root + 4 hierarchical children), got %d", len(subgraph.Issues))
		}
		for _, childID := range []string{"test-pcat.1", "test-pcat.2", "test-pcat.3", "test-pcat.8"} {
			if _, ok := subgraph.IssueMap[childID]; !ok {
				t.Errorf("Child %s not found in subgraph", childID)
			}
		}
	})
}

// TestResolveProtoIDOrTitle tests proto lookup by ID or title (bd-drcx).
// Kept separate from TestTemplateSuite because title-based search is affected by shared data.
func TestResolveProtoIDOrTitle(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	proto1 := h.createIssue("mol-polecat-work", "Polecat workflow", types.TypeEpic, 1)
	h.addLabel(proto1.ID, BeadsTemplateLabel)

	proto2 := h.createIssue("mol-version-bump", "Version bump workflow", types.TypeEpic, 1)
	h.addLabel(proto2.ID, BeadsTemplateLabel)

	proto3 := h.createIssue("mol-release", "Release workflow", types.TypeEpic, 1)
	h.addLabel(proto3.ID, BeadsTemplateLabel)

	nonProto := h.createIssue("mol-test", "Not a proto", types.TypeTask, 2)

	t.Run("resolve by exact ID", func(t *testing.T) {
		resolved, err := resolveProtoIDOrTitle(ctx, s, proto1.ID)
		if err != nil {
			t.Fatalf("Failed to resolve by ID: %v", err)
		}
		if resolved != proto1.ID {
			t.Errorf("Expected %s, got %s", proto1.ID, resolved)
		}
	})

	t.Run("resolve by exact title", func(t *testing.T) {
		resolved, err := resolveProtoIDOrTitle(ctx, s, "mol-polecat-work")
		if err != nil {
			t.Fatalf("Failed to resolve by title: %v", err)
		}
		if resolved != proto1.ID {
			t.Errorf("Expected %s, got %s", proto1.ID, resolved)
		}
	})

	t.Run("resolve by title case-insensitive", func(t *testing.T) {
		resolved, err := resolveProtoIDOrTitle(ctx, s, "MOL-POLECAT-WORK")
		if err != nil {
			t.Fatalf("Failed to resolve by title (case-insensitive): %v", err)
		}
		if resolved != proto1.ID {
			t.Errorf("Expected %s, got %s", proto1.ID, resolved)
		}
	})

	t.Run("resolve by unique partial title", func(t *testing.T) {
		resolved, err := resolveProtoIDOrTitle(ctx, s, "polecat")
		if err != nil {
			t.Fatalf("Failed to resolve by partial title: %v", err)
		}
		if resolved != proto1.ID {
			t.Errorf("Expected %s, got %s", proto1.ID, resolved)
		}
	})

	t.Run("ambiguous partial title returns error", func(t *testing.T) {
		_, err := resolveProtoIDOrTitle(ctx, s, "mol-")
		if err == nil {
			t.Fatal("Expected error for ambiguous title, got nil")
		}
		if !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("Expected 'ambiguous' in error, got: %v", err)
		}
	})

	t.Run("non-existent returns error", func(t *testing.T) {
		_, err := resolveProtoIDOrTitle(ctx, s, "nonexistent-proto")
		if err == nil {
			t.Fatal("Expected error for non-existent proto, got nil")
		}
		if !strings.Contains(err.Error(), "no proto found") {
			t.Errorf("Expected 'no proto found' in error, got: %v", err)
		}
	})

	t.Run("non-proto ID returns error", func(t *testing.T) {
		_, err := resolveProtoIDOrTitle(ctx, s, nonProto.ID)
		if err == nil {
			t.Fatal("Expected error for non-proto ID, got nil")
		}
	})
}

// TestExtractRequiredVariables_IgnoresUndeclaredVars tests that handlebars in
// description text that are NOT defined in VarDefs are ignored (gt-ky9loa).
func TestExtractRequiredVariables_IgnoresUndeclaredVars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		issues       []*types.Issue
		varDefs      map[string]formula.VarDef
		wantRequired []string
	}{
		{
			name:         "declared var without default is required",
			issues:       []*types.Issue{{Title: "Deploy {{component}}", Description: "Deploy the component"}},
			varDefs:      map[string]formula.VarDef{"component": {Required: true}},
			wantRequired: []string{"component"},
		},
		{
			name:         "declared var with default is not required",
			issues:       []*types.Issue{{Title: "Deploy {{component}}", Description: "Deploy the component"}},
			varDefs:      map[string]formula.VarDef{"component": {Default: formula.StringPtr("api")}},
			wantRequired: []string{},
		},
		{
			name: "undeclared var in description is ignored when VarDefs exists",
			issues: []*types.Issue{{
				Title:       "Generate report",
				Description: "Output format:\n**Ready**: {{ready_count}}\n**Done**: {{done_count}}",
			}},
			varDefs:      map[string]formula.VarDef{},
			wantRequired: []string{},
		},
		{
			name: "mix of declared and undeclared vars",
			issues: []*types.Issue{{
				Title:       "Deploy {{component}} to {{env}}",
				Description: "Shows: {{status_count}} items processed",
			}},
			varDefs: map[string]formula.VarDef{
				"component": {Required: true},
				"env":       {Default: formula.StringPtr("prod")},
			},
			wantRequired: []string{"component"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subgraph := &TemplateSubgraph{Issues: tt.issues, VarDefs: tt.varDefs}
			got := extractRequiredVariables(subgraph)

			gotMap := make(map[string]bool)
			for _, v := range got {
				gotMap[v] = true
			}
			wantMap := make(map[string]bool)
			for _, v := range tt.wantRequired {
				wantMap[v] = true
			}

			if len(got) != len(tt.wantRequired) {
				t.Errorf("extractRequiredVariables() = %v, want %v", got, tt.wantRequired)
				return
			}
			for _, v := range tt.wantRequired {
				if !gotMap[v] {
					t.Errorf("extractRequiredVariables() missing expected var %q, got %v", v, got)
				}
			}
			for _, v := range got {
				if !wantMap[v] {
					t.Errorf("extractRequiredVariables() has unexpected var %q, want %v", v, tt.wantRequired)
				}
			}
		})
	}
}
