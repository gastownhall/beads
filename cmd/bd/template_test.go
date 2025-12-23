package main

import (
	"context"
	"os"
	"path/filepath"
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
