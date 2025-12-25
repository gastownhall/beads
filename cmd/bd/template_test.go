package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// Beads Template Tests (for bd template instantiate)
// =============================================================================

// TestExtractVariables tests the {{variable}} pattern extraction
func TestExtractVariables(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "single variable",
			input:    "Release {{version}}",
			expected: []string{"version"},
		},
		{
			name:     "multiple variables",
			input:    "Release {{version}} on {{date}}",
			expected: []string{"version", "date"},
		},
		{
			name:     "no variables",
			input:    "Just plain text",
			expected: nil,
		},
		{
			name:     "duplicate variables",
			input:    "{{version}} and {{version}} again",
			expected: []string{"version"},
		},
		{
			name:     "variable with underscore",
			input:    "{{my_variable}}",
			expected: []string{"my_variable"},
		},
		{
			name:     "variable with numbers",
			input:    "{{var123}}",
			expected: []string{"var123"},
		},
		{
			name:     "invalid variable format",
			input:    "{{123invalid}}",
			expected: nil,
		},
		{
			name:     "empty braces",
			input:    "{{}}",
			expected: nil,
		},
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
	tests := []struct {
		name     string
		input    string
		vars     map[string]string
		expected string
	}{
		{
			name:     "single variable",
			input:    "Release {{version}}",
			vars:     map[string]string{"version": "1.2.0"},
			expected: "Release 1.2.0",
		},
		{
			name:     "multiple variables",
			input:    "Release {{version}} on {{date}}",
			vars:     map[string]string{"version": "1.2.0", "date": "2024-01-15"},
			expected: "Release 1.2.0 on 2024-01-15",
		},
		{
			name:     "missing variable unchanged",
			input:    "Release {{version}}",
			vars:     map[string]string{},
			expected: "Release {{version}}",
		},
		{
			name:     "partial substitution",
			input:    "{{found}} and {{missing}}",
			vars:     map[string]string{"found": "yes"},
			expected: "yes and {{missing}}",
		},
		{
			name:     "no variables",
			input:    "Just plain text",
			vars:     map[string]string{"version": "1.0"},
			expected: "Just plain text",
		},
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
	s   *sqlite.SQLiteStorage
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

// TestLoadTemplateSubgraph tests loading a template epic with children
func TestLoadTemplateSubgraph(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-template-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	t.Run("load epic with no children", func(t *testing.T) {
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

	t.Run("load epic with children", func(t *testing.T) {
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

		// Check variables extracted
		vars := extractAllVariables(subgraph)
		if len(vars) != 1 || vars[0] != "name" {
			t.Errorf("Variables = %v, want [name]", vars)
		}
	})

	t.Run("load epic with nested children", func(t *testing.T) {
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
}

// TestCloneSubgraph tests cloning a template with variable substitution
func TestCloneSubgraph(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-clone-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	t.Run("clone simple template", func(t *testing.T) {
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

		// Verify the cloned issue
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

	t.Run("clone template with children", func(t *testing.T) {
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

		// Verify all IDs are different
		if _, ok := result.IDMapping[epic.ID]; !ok {
			t.Error("ID mapping missing epic")
		}
		if _, ok := result.IDMapping[child1.ID]; !ok {
			t.Error("ID mapping missing child1")
		}
		if _, ok := result.IDMapping[child2.ID]; !ok {
			t.Error("ID mapping missing child2")
		}

		// Verify cloned epic title
		newEpic, err := s.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			t.Fatalf("Failed to get cloned epic: %v", err)
		}
		if newEpic.Title != "Deploy api-gateway" {
			t.Errorf("Epic title = %q, want %q", newEpic.Title, "Deploy api-gateway")
		}

		// Verify dependencies were cloned
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

	t.Run("cloned issues start with open status", func(t *testing.T) {
		// Create template with in_progress status
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

	t.Run("assignee override applies to root epic only", func(t *testing.T) {
		epic := h.createIssue("Root Epic", "", types.TypeEpic, 1)
		child := h.createIssue("Child Task", "", types.TypeTask, 2)
		h.addParentChild(child.ID, epic.ID)

		// Set assignees on template
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

		// Clone with assignee override
		opts := CloneOptions{Assignee: "new-assignee", Actor: "test-user"}
		result, err := cloneSubgraph(ctx, s, subgraph, opts)
		if err != nil {
			t.Fatalf("cloneSubgraph failed: %v", err)
		}

		// Root epic should have override assignee
		newEpic, err := s.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			t.Fatalf("Failed to get cloned epic: %v", err)
		}
		if newEpic.Assignee != "new-assignee" {
			t.Errorf("Epic assignee = %q, want %q", newEpic.Assignee, "new-assignee")
		}

		// Child should keep template assignee
		newChildID := result.IDMapping[child.ID]
		newChild, err := s.GetIssue(ctx, newChildID)
		if err != nil {
			t.Fatalf("Failed to get cloned child: %v", err)
		}
		if newChild.Assignee != "child-owner" {
			t.Errorf("Child assignee = %q, want %q", newChild.Assignee, "child-owner")
		}
	})
}

// TestExtractAllVariables tests extracting variables from entire subgraph
func TestExtractAllVariables(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-extractall-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	epic := h.createIssue("Release {{version}}", "For {{product}}", types.TypeEpic, 1)
	child := h.createIssue("Deploy to {{environment}}", "", types.TypeTask, 2)
	h.addParentChild(child.ID, epic.ID)

	subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
	if err != nil {
		t.Fatalf("loadTemplateSubgraph failed: %v", err)
	}

	vars := extractAllVariables(subgraph)

	// Should find version, product, and environment
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

// TestResolveProtoIDOrTitle tests proto lookup by ID or title (bd-drcx)
func TestResolveProtoIDOrTitle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-proto-lookup-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	// Create some protos with distinct titles
	proto1 := h.createIssue("mol-polecat-work", "Polecat workflow", types.TypeEpic, 1)
	h.addLabel(proto1.ID, BeadsTemplateLabel)

	proto2 := h.createIssue("mol-version-bump", "Version bump workflow", types.TypeEpic, 1)
	h.addLabel(proto2.ID, BeadsTemplateLabel)

	proto3 := h.createIssue("mol-release", "Release workflow", types.TypeEpic, 1)
	h.addLabel(proto3.ID, BeadsTemplateLabel)

	// Create a non-proto issue with similar title
	nonProto := h.createIssue("mol-test", "Not a proto", types.TypeTask, 2)
	_ = nonProto

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
		// "mol-" matches all three protos
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
		// This ID exists but is not a proto (no template label)
		_, err := resolveProtoIDOrTitle(ctx, s, nonProto.ID)
		if err == nil {
			t.Fatal("Expected error for non-proto ID, got nil")
		}
	})
}

// TestLoadTemplateSubgraphWithManyChildren tests loading with 4+ children (bd-c8d5)
// This reproduces the bug where only 2 of 4 children were loaded.
func TestLoadTemplateSubgraphWithManyChildren(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-many-children-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testDB := filepath.Join(tmpDir, "test.db")
	s := newTestStore(t, testDB)
	defer s.Close()

	ctx := context.Background()
	h := &templateTestHelper{s: s, ctx: ctx, t: t}

	t.Run("load epic with 4 children", func(t *testing.T) {
		epic := h.createIssue("Proto Workflow", "Workflow with 4 steps", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		// Create 4 children with different titles (like the bug report)
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

		// Should have 5 issues: 1 root + 4 children
		if len(subgraph.Issues) != 5 {
			t.Errorf("Issues count = %d, want 5 (epic + 4 children)", len(subgraph.Issues))
			t.Logf("Found issues:")
			for _, iss := range subgraph.Issues {
				t.Logf("  - %s: %s", iss.ID, iss.Title)
			}
		}

		// Verify each child is in the subgraph
		childIDs := []string{child1.ID, child2.ID, child3.ID, child4.ID}
		for _, childID := range childIDs {
			if _, ok := subgraph.IssueMap[childID]; !ok {
				t.Errorf("Child %s not found in subgraph", childID)
			}
		}
	})

	t.Run("clone epic with 4 children creates all 4", func(t *testing.T) {
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

		// Should create 5 issues (1 root + 4 children)
		if result.Created != 5 {
			t.Errorf("Created = %d, want 5", result.Created)
		}

		// Verify all children were mapped
		for _, childID := range []string{child1.ID, child2.ID, child3.ID, child4.ID} {
			if _, ok := result.IDMapping[childID]; !ok {
				t.Errorf("Child %s not in ID mapping", childID)
			}
		}
	})

	t.Run("load epic with hierarchical child IDs - bd-c8d5 reproduction", func(t *testing.T) {
		// This replicates the exact scenario from bd-c8d5:
		// Proto gt-lwuu has children gt-lwuu.1, gt-lwuu.2, gt-lwuu.3, gt-lwuu.8
		// Only gt-lwuu.1 and gt-lwuu.2 were being loaded
		// Using test-xxx prefix to match test database configuration
		epic := h.createIssueWithID("test-lwuu", "mol-polecat-work", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		// Create children with hierarchical IDs (note the gap: .1, .2, .3, .8)
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

		// Should have 5 issues: 1 root + 4 children
		if len(subgraph.Issues) != 5 {
			t.Errorf("Issues count = %d, want 5", len(subgraph.Issues))
			t.Logf("Found issues:")
			for _, iss := range subgraph.Issues {
				t.Logf("  - %s: %s", iss.ID, iss.Title)
			}
		}

		// Verify all 4 children are loaded
		expectedChildren := []string{"test-lwuu.1", "test-lwuu.2", "test-lwuu.3", "test-lwuu.8"}
		for _, childID := range expectedChildren {
			if _, ok := subgraph.IssueMap[childID]; !ok {
				t.Errorf("Child %s not found in subgraph", childID)
			}
		}
	})

	t.Run("children with wrong dep type are not loaded - potential bug cause", func(t *testing.T) {
		// This tests the hypothesis that the bug is caused by children
		// having the wrong dependency type (e.g., "blocks" instead of "parent-child")
		epic := h.createIssue("Proto with mixed deps", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		child1 := h.createIssue("load-context", "", types.TypeTask, 2)
		child2 := h.createIssue("implement", "", types.TypeTask, 2)
		child3 := h.createIssue("self-review", "", types.TypeTask, 2)
		child4 := h.createIssue("request-shutdown", "", types.TypeTask, 2)

		// Only child1 and child2 have parent-child dependency
		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)

		// child3 and child4 have "blocks" dependency (wrong type)
		blocksDep := &types.Dependency{
			IssueID:     child3.ID,
			DependsOnID: epic.ID,
			Type:        types.DepBlocks,
		}
		if err := s.AddDependency(ctx, blocksDep, "test-user"); err != nil {
			t.Fatalf("Failed to add blocks dependency: %v", err)
		}
		blocksDep2 := &types.Dependency{
			IssueID:     child4.ID,
			DependsOnID: epic.ID,
			Type:        types.DepBlocks,
		}
		if err := s.AddDependency(ctx, blocksDep2, "test-user"); err != nil {
			t.Fatalf("Failed to add blocks dependency: %v", err)
		}

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		// With non-hierarchical IDs, only parent-child deps are loaded
		// This is expected - the hierarchical ID fallback doesn't apply
		t.Logf("Found %d issues (expecting 3 without hierarchical IDs):", len(subgraph.Issues))
		for _, iss := range subgraph.Issues {
			t.Logf("  - %s: %s", iss.ID, iss.Title)
		}

		if len(subgraph.Issues) != 3 {
			t.Errorf("Expected 3 issues (without hierarchical ID fallback), got %d", len(subgraph.Issues))
		}
	})

	t.Run("hierarchical children with wrong dep type ARE loaded - bd-c8d5 fix", func(t *testing.T) {
		// This tests the fix for bd-c8d5:
		// Hierarchical children (parent.N pattern) are loaded even if they have
		// wrong dependency types, using the ID pattern fallback.
		epic := h.createIssueWithID("test-pcat", "Proto with mixed deps", "", types.TypeEpic, 1)
		h.addLabel(epic.ID, BeadsTemplateLabel)

		// child1 and child2 have correct parent-child dependency
		child1 := h.createIssueWithID("test-pcat.1", "load-context", "", types.TypeTask, 2)
		child2 := h.createIssueWithID("test-pcat.2", "implement", "", types.TypeTask, 2)
		h.addParentChild(child1.ID, epic.ID)
		h.addParentChild(child2.ID, epic.ID)

		// child3 has NO dependency at all (broken data)
		_ = h.createIssueWithID("test-pcat.3", "self-review", "", types.TypeTask, 2)
		// No dependency added for child3!

		// child8 has wrong dependency type (blocks instead of parent-child)
		child8 := h.createIssueWithID("test-pcat.8", "request-shutdown", "", types.TypeTask, 2)
		blocksDep := &types.Dependency{
			IssueID:     child8.ID,
			DependsOnID: epic.ID,
			Type:        types.DepBlocks,
		}
		if err := s.AddDependency(ctx, blocksDep, "test-user"); err != nil {
			t.Fatalf("Failed to add blocks dependency: %v", err)
		}

		subgraph, err := loadTemplateSubgraph(ctx, s, epic.ID)
		if err != nil {
			t.Fatalf("loadTemplateSubgraph failed: %v", err)
		}

		t.Logf("Found %d issues:", len(subgraph.Issues))
		for _, iss := range subgraph.Issues {
			t.Logf("  - %s: %s", iss.ID, iss.Title)
		}

		// With the bd-c8d5 fix, all 5 issues should be loaded:
		// 1 root + 4 hierarchical children (found by ID pattern fallback)
		if len(subgraph.Issues) != 5 {
			t.Errorf("Expected 5 issues (root + 4 hierarchical children), got %d", len(subgraph.Issues))
		}

		// Verify all children are in the subgraph
		expectedChildren := []string{"test-pcat.1", "test-pcat.2", "test-pcat.3", "test-pcat.8"}
		for _, childID := range expectedChildren {
			if _, ok := subgraph.IssueMap[childID]; !ok {
				t.Errorf("Child %s not found in subgraph", childID)
			}
		}
	})
}
