package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestDependencySuite(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("DepAdd", func(t *testing.T) {
		// Create test issues
		issues := []*types.Issue{
			{
				ID:        "test-1",
				Title:     "Task 1",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-2",
				Title:     "Task 2",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
		}

		for _, issue := range issues {
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Add dependency
		dep := &types.Dependency{
			IssueID:     "test-1",
			DependsOnID: "test-2",
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}

		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed: %v", err)
		}

		// Verify dependency was added
		deps, err := s.GetDependencies(ctx, "test-1")
		if err != nil {
			t.Fatalf("GetDependencies failed: %v", err)
		}

		if len(deps) != 1 {
			t.Fatalf("Expected 1 dependency, got %d", len(deps))
		}

		if deps[0].ID != "test-2" {
			t.Errorf("Expected dependency on test-2, got %s", deps[0].ID)
		}
	})

	t.Run("DepTypes", func(t *testing.T) {
		// Create test issues
		for i := 1; i <= 4; i++ {
			issue := &types.Issue{
				ID:        fmt.Sprintf("test-types-%d", i),
				Title:     fmt.Sprintf("Task %d", i),
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			}
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Test different dependency types (without creating cycles)
		depTypes := []struct {
			depType types.DependencyType
			from    string
			to      string
		}{
			{types.DepBlocks, "test-types-2", "test-types-1"},
			{types.DepRelated, "test-types-3", "test-types-1"},
			{types.DepParentChild, "test-types-4", "test-types-1"},
			{types.DepDiscoveredFrom, "test-types-3", "test-types-2"},
		}

		for _, dt := range depTypes {
			dep := &types.Dependency{
				IssueID:     dt.from,
				DependsOnID: dt.to,
				Type:        dt.depType,
				CreatedAt:   time.Now(),
			}

			if err := s.AddDependency(ctx, dep, "test"); err != nil {
				t.Fatalf("AddDependency failed for type %s: %v", dt.depType, err)
			}
		}
	})

	t.Run("DepCycleDetection", func(t *testing.T) {
		// Create test issues
		for i := 1; i <= 3; i++ {
			issue := &types.Issue{
				ID:        fmt.Sprintf("test-cycle-%d", i),
				Title:     fmt.Sprintf("Task %d", i),
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			}
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Create a cycle: test-cycle-1 -> test-cycle-2 -> test-cycle-3 -> test-cycle-1
		// Add first two deps successfully
		deps := []struct {
			from string
			to   string
		}{
			{"test-cycle-1", "test-cycle-2"},
			{"test-cycle-2", "test-cycle-3"},
		}

		for _, d := range deps {
			dep := &types.Dependency{
				IssueID:     d.from,
				DependsOnID: d.to,
				Type:        types.DepBlocks,
				CreatedAt:   time.Now(),
			}
			if err := s.AddDependency(ctx, dep, "test"); err != nil {
				t.Fatalf("AddDependency failed: %v", err)
			}
		}

		// Try to add the third dep which would create a cycle - should fail
		cycleDep := &types.Dependency{
			IssueID:     "test-cycle-3",
			DependsOnID: "test-cycle-1",
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}
		if err := s.AddDependency(ctx, cycleDep, "test"); err == nil {
			t.Fatal("Expected AddDependency to fail when creating cycle, but it succeeded")
		}

		// Since cycle detection prevented the cycle, DetectCycles should find no cycles
		cycles, err := s.DetectCycles(ctx)
		if err != nil {
			t.Fatalf("DetectCycles failed: %v", err)
		}

		if len(cycles) != 0 {
			t.Error("Expected no cycles since cycle was prevented")
		}
	})

	t.Run("DepRemove", func(t *testing.T) {
		// Create test issues
		issues := []*types.Issue{
			{
				ID:        "test-remove-1",
				Title:     "Task 1",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
			{
				ID:        "test-remove-2",
				Title:     "Task 2",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
			},
		}

		for _, issue := range issues {
			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
		}

		// Add dependency
		dep := &types.Dependency{
			IssueID:     "test-remove-1",
			DependsOnID: "test-remove-2",
			Type:        types.DepBlocks,
			CreatedAt:   time.Now(),
		}

		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatal(err)
		}

		// Remove dependency
		if err := s.RemoveDependency(ctx, "test-remove-1", "test-remove-2", "test"); err != nil {
			t.Fatalf("RemoveDependency failed: %v", err)
		}

		// Verify dependency was removed
		deps, err := s.GetDependencies(ctx, "test-remove-1")
		if err != nil {
			t.Fatalf("GetDependencies failed: %v", err)
		}

		if len(deps) != 0 {
			t.Errorf("Expected 0 dependencies after removal, got %d", len(deps))
		}
	})
}

func TestDepCommandsInit(t *testing.T) {
	if depCmd == nil {
		t.Fatal("depCmd should be initialized")
	}

	if depCmd.Use != "dep" {
		t.Errorf("Expected Use='dep', got %q", depCmd.Use)
	}

	if depAddCmd == nil {
		t.Fatal("depAddCmd should be initialized")
	}

	if depRemoveCmd == nil {
		t.Fatal("depRemoveCmd should be initialized")
	}
}

func TestDepTreeFormatFlag(t *testing.T) {
	// Test that the --format flag exists on depTreeCmd
	flag := depTreeCmd.Flags().Lookup("format")
	if flag == nil {
		t.Fatal("depTreeCmd should have --format flag")
	}

	// Test default value is empty string
	if flag.DefValue != "" {
		t.Errorf("Expected default format='', got %q", flag.DefValue)
	}

	// Test usage text mentions mermaid
	if !strings.Contains(flag.Usage, "mermaid") {
		t.Errorf("Expected flag usage to mention 'mermaid', got %q", flag.Usage)
	}
}

func TestGetStatusEmoji(t *testing.T) {
	tests := []struct {
		status types.Status
		want   string
	}{
		{types.StatusOpen, "☐"},
		{types.StatusInProgress, "◧"},
		{types.StatusBlocked, "⚠"},
		{types.StatusClosed, "☑"},
		{types.Status("unknown"), "?"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := getStatusEmoji(tt.status)
			if got != tt.want {
				t.Errorf("getStatusEmoji(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestOutputMermaidTree(t *testing.T) {
	tests := []struct {
		name   string
		tree   []*types.TreeNode
		rootID string
		want   []string // Lines that must appear in output
	}{
		{
			name:   "empty tree",
			tree:   []*types.TreeNode{},
			rootID: "test-1",
			want: []string{
				"flowchart TD",
				`test-1["No dependencies"]`,
			},
		},
		{
			name: "single dependency",
			tree: []*types.TreeNode{
				{
					Issue:    types.Issue{ID: "test-1", Title: "Task 1", Status: types.StatusInProgress},
					Depth:    0,
					ParentID: "",
				},
				{
					Issue:    types.Issue{ID: "test-2", Title: "Task 2", Status: types.StatusClosed},
					Depth:    1,
					ParentID: "test-1",
				},
			},
			rootID: "test-1",
			want: []string{
				"flowchart TD",
				`test-1["◧ test-1: Task 1"]`,
				`test-2["☑ test-2: Task 2"]`,
				"test-1 --> test-2",
			},
		},
		{
			name: "multiple dependencies",
			tree: []*types.TreeNode{
				{
					Issue:    types.Issue{ID: "test-1", Title: "Main", Status: types.StatusOpen},
					Depth:    0,
					ParentID: "",
				},
				{
					Issue:    types.Issue{ID: "test-2", Title: "Sub 1", Status: types.StatusClosed},
					Depth:    1,
					ParentID: "test-1",
				},
				{
					Issue:    types.Issue{ID: "test-3", Title: "Sub 2", Status: types.StatusBlocked},
					Depth:    1,
					ParentID: "test-1",
				},
			},
			rootID: "test-1",
			want: []string{
				"flowchart TD",
				`test-1["☐ test-1: Main"]`,
				`test-2["☑ test-2: Sub 1"]`,
				`test-3["⚠ test-3: Sub 2"]`,
				"test-1 --> test-2",
				"test-1 --> test-3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			outputMermaidTree(tt.tree, tt.rootID)

			w.Close()
			os.Stdout = old

			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			// Verify all expected lines appear
			for _, line := range tt.want {
				if !strings.Contains(output, line) {
					t.Errorf("expected output to contain %q, got:\n%s", line, output)
				}
			}
		})
	}
}

func TestOutputMermaidTree_Siblings(t *testing.T) {
	// Test case: Siblings with children (reproduces issue with wrong parent inference)
	// Structure:
	//   BD-1 (root)
	//   ├── BD-2 (sibling 1)
	//   │   └── BD-4 (child of BD-2)
	//   └── BD-3 (sibling 2)
	//       └── BD-5 (child of BD-3)
	tree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "BD-1", Title: "Parent", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "BD-2", Title: "Sibling 1", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "BD-1",
		},
		{
			Issue:    types.Issue{ID: "BD-3", Title: "Sibling 2", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "BD-1",
		},
		{
			Issue:    types.Issue{ID: "BD-4", Title: "Child of Sibling 1", Status: types.StatusOpen},
			Depth:    2,
			ParentID: "BD-2",
		},
		{
			Issue:    types.Issue{ID: "BD-5", Title: "Child of Sibling 2", Status: types.StatusOpen},
			Depth:    2,
			ParentID: "BD-3",
		},
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	outputMermaidTree(tree, "BD-1")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify correct edges exist
	correctEdges := []string{
		"BD-1 --> BD-2",
		"BD-1 --> BD-3",
		"BD-2 --> BD-4",
		"BD-3 --> BD-5",
	}

	for _, edge := range correctEdges {
		if !strings.Contains(output, edge) {
			t.Errorf("expected edge %q to be present, got:\n%s", edge, output)
		}
	}

	// Verify incorrect edges do NOT exist (siblings shouldn't be connected)
	incorrectEdges := []string{
		"BD-2 --> BD-3",   // Siblings shouldn't be connected
		"BD-3 --> BD-4",   // BD-4's parent is BD-2, not BD-3
		"BD-4 --> BD-3",   // Wrong direction
		"BD-4 --> BD-5",   // These are cousins, not parent-child
	}

	for _, edge := range incorrectEdges {
		if strings.Contains(output, edge) {
			t.Errorf("incorrect edge %q should NOT be present, got:\n%s", edge, output)
		}
	}
}

func TestDepTreeDirectionFlag(t *testing.T) {
	// Test that the --direction flag exists on depTreeCmd
	flag := depTreeCmd.Flags().Lookup("direction")
	if flag == nil {
		t.Fatal("depTreeCmd should have --direction flag")
	}

	// Test default value is empty string (will default to "down")
	if flag.DefValue != "" {
		t.Errorf("Expected default direction='', got %q", flag.DefValue)
	}

	// Test usage text mentions valid options
	usage := flag.Usage
	if !strings.Contains(usage, "down") || !strings.Contains(usage, "up") || !strings.Contains(usage, "both") {
		t.Errorf("Expected flag usage to mention 'down', 'up', 'both', got %q", usage)
	}
}

func TestDepTreeStatusFlag(t *testing.T) {
	// Test that the --status flag exists on depTreeCmd
	flag := depTreeCmd.Flags().Lookup("status")
	if flag == nil {
		t.Fatal("depTreeCmd should have --status flag")
	}

	// Test default value is empty string
	if flag.DefValue != "" {
		t.Errorf("Expected default status='', got %q", flag.DefValue)
	}
}

func TestFilterTreeByStatus(t *testing.T) {
	tree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "BD-1", Title: "Parent", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "BD-2", Title: "Open Child", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "BD-1",
		},
		{
			Issue:    types.Issue{ID: "BD-3", Title: "Closed Child", Status: types.StatusClosed},
			Depth:    1,
			ParentID: "BD-1",
		},
		{
			Issue:    types.Issue{ID: "BD-4", Title: "Open Grandchild", Status: types.StatusOpen},
			Depth:    2,
			ParentID: "BD-3",
		},
	}

	t.Run("filter to open only", func(t *testing.T) {
		filtered := filterTreeByStatus(tree, types.StatusOpen)

		// Should include BD-1, BD-2, and BD-4 (matching)
		// Plus BD-3 as ancestor of BD-4
		ids := make(map[string]bool)
		for _, node := range filtered {
			ids[node.ID] = true
		}

		if !ids["BD-1"] {
			t.Error("Expected BD-1 (root open) in filtered tree")
		}
		if !ids["BD-2"] {
			t.Error("Expected BD-2 (open child) in filtered tree")
		}
		if !ids["BD-3"] {
			t.Error("Expected BD-3 (ancestor of open node) in filtered tree")
		}
		if !ids["BD-4"] {
			t.Error("Expected BD-4 (open grandchild) in filtered tree")
		}
	})

	t.Run("filter to closed only", func(t *testing.T) {
		filtered := filterTreeByStatus(tree, types.StatusClosed)

		ids := make(map[string]bool)
		for _, node := range filtered {
			ids[node.ID] = true
		}

		// Should include BD-3 (matching) and BD-1 (ancestor)
		if !ids["BD-1"] {
			t.Error("Expected BD-1 (ancestor) in filtered tree")
		}
		if !ids["BD-3"] {
			t.Error("Expected BD-3 (closed) in filtered tree")
		}
		if ids["BD-2"] {
			t.Error("BD-2 should not be in closed-filtered tree")
		}
		if ids["BD-4"] {
			t.Error("BD-4 should not be in closed-filtered tree")
		}
	})

	t.Run("filter to non-existent status", func(t *testing.T) {
		filtered := filterTreeByStatus(tree, types.StatusBlocked)
		if len(filtered) != 0 {
			t.Errorf("Expected empty tree when filtering to non-matching status, got %d nodes", len(filtered))
		}
	})

	t.Run("filter empty tree", func(t *testing.T) {
		filtered := filterTreeByStatus([]*types.TreeNode{}, types.StatusOpen)
		if len(filtered) != 0 {
			t.Errorf("Expected empty tree, got %d nodes", len(filtered))
		}
	})
}

func TestFormatTreeNode(t *testing.T) {
	tests := []struct {
		name     string
		node     *types.TreeNode
		contains []string
	}{
		{
			name: "open issue at depth 0 shows READY",
			node: &types.TreeNode{
				Issue: types.Issue{
					ID:       "BD-1",
					Title:    "Test Issue",
					Status:   types.StatusOpen,
					Priority: 2,
				},
				Depth: 0,
			},
			contains: []string{"BD-1", "Test Issue", "P2", "open", "[READY]"},
		},
		{
			name: "open issue at depth 1 does not show READY",
			node: &types.TreeNode{
				Issue: types.Issue{
					ID:       "BD-2",
					Title:    "Child Issue",
					Status:   types.StatusOpen,
					Priority: 1,
				},
				Depth: 1,
			},
			contains: []string{"BD-2", "Child Issue", "P1", "open"},
		},
		{
			name: "closed issue",
			node: &types.TreeNode{
				Issue: types.Issue{
					ID:       "BD-3",
					Title:    "Done Issue",
					Status:   types.StatusClosed,
					Priority: 3,
				},
				Depth: 0,
			},
			contains: []string{"BD-3", "Done Issue", "P3", "closed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatTreeNode(tt.node)
			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("formatTreeNode() = %q, want to contain %q", result, want)
				}
			}

			// For non-root open issues, verify READY is NOT shown
			if tt.node.Status == types.StatusOpen && tt.node.Depth > 0 {
				if strings.Contains(result, "[READY]") {
					t.Errorf("formatTreeNode() = %q, should NOT contain [READY] for depth > 0", result)
				}
			}
		})
	}
}

func TestRenderTreeOutput(t *testing.T) {
	// Test tree with proper connectors
	tree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "BD-1", Title: "Root", Status: types.StatusOpen, Priority: 1},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "BD-2", Title: "Child 1", Status: types.StatusOpen, Priority: 2},
			Depth:    1,
			ParentID: "BD-1",
		},
		{
			Issue:    types.Issue{ID: "BD-3", Title: "Child 2", Status: types.StatusClosed, Priority: 2},
			Depth:    1,
			ParentID: "BD-1",
		},
		{
			Issue:    types.Issue{ID: "BD-4", Title: "Grandchild", Status: types.StatusOpen, Priority: 3},
			Depth:    2,
			ParentID: "BD-2",
		},
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	renderTree(tree, 50, "down")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Check for tree connectors
	if !strings.Contains(output, "├──") && !strings.Contains(output, "└──") {
		t.Errorf("Expected tree connectors (├── or └──) in output, got:\n%s", output)
	}

	// Check that all nodes are present
	for _, node := range tree {
		if !strings.Contains(output, node.ID) {
			t.Errorf("Expected node %s in output, got:\n%s", node.ID, output)
		}
	}
}

func TestMergeBidirectionalTrees_Empty(t *testing.T) {
	// Test merging empty trees
	downTree := []*types.TreeNode{}
	upTree := []*types.TreeNode{}
	rootID := "test-root"

	result := mergeBidirectionalTrees(downTree, upTree, rootID)

	if len(result) != 0 {
		t.Errorf("Expected empty result for empty trees, got %d nodes", len(result))
	}
}

func TestMergeBidirectionalTrees_OnlyDown(t *testing.T) {
	// Test with only down tree (dependencies)
	downTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "test-root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dep-1", Title: "Dependency 1", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "test-root",
		},
		{
			Issue:    types.Issue{ID: "dep-2", Title: "Dependency 2", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "test-root",
		},
	}
	upTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "test-root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
	}

	result := mergeBidirectionalTrees(downTree, upTree, "test-root")

	// Should have all nodes from down tree
	if len(result) != 3 {
		t.Errorf("Expected 3 nodes, got %d", len(result))
	}

	// Verify downTree nodes are present
	hasRoot := false
	hasDep1 := false
	hasDep2 := false
	for _, node := range result {
		if node.ID == "test-root" {
			hasRoot = true
		}
		if node.ID == "dep-1" {
			hasDep1 = true
		}
		if node.ID == "dep-2" {
			hasDep2 = true
		}
	}
	if !hasRoot || !hasDep1 || !hasDep2 {
		t.Error("Expected all down tree nodes in result")
	}
}

func TestMergeBidirectionalTrees_WithDependents(t *testing.T) {
	// Test with both dependencies and dependents
	downTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "test-root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dep-1", Title: "Dependency 1", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "test-root",
		},
	}
	upTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "test-root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dependent-1", Title: "Dependent 1", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "test-root",
		},
	}

	result := mergeBidirectionalTrees(downTree, upTree, "test-root")

	// Should have dependent first, then down tree nodes (3 total, root appears once)
	// Pattern: dependent node(s), then root + dependencies
	if len(result) < 3 {
		t.Errorf("Expected at least 3 nodes, got %d", len(result))
	}

	// Find dependent-1 and dep-1 in result
	foundDependentID := false
	foundDepID := false
	for _, node := range result {
		if node.ID == "dependent-1" {
			foundDependentID = true
		}
		if node.ID == "dep-1" {
			foundDepID = true
		}
	}

	if !foundDependentID {
		t.Error("Expected dependent-1 in merged result")
	}
	if !foundDepID {
		t.Error("Expected dep-1 in merged result")
	}
}

func TestMergeBidirectionalTrees_MultipleDepth(t *testing.T) {
	// Test with multi-level hierarchies
	downTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dep-1", Title: "Dep 1", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "root",
		},
		{
			Issue:    types.Issue{ID: "dep-1-1", Title: "Dep 1.1", Status: types.StatusOpen},
			Depth:    2,
			ParentID: "dep-1",
		},
	}
	upTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dependent-1", Title: "Dependent 1", Status: types.StatusOpen},
			Depth:    1,
			ParentID: "root",
		},
		{
			Issue:    types.Issue{ID: "dependent-1-1", Title: "Dependent 1.1", Status: types.StatusOpen},
			Depth:    2,
			ParentID: "dependent-1",
		},
	}

	result := mergeBidirectionalTrees(downTree, upTree, "root")

	// Should include all nodes from both trees (minus duplicate root)
	if len(result) < 5 {
		t.Errorf("Expected at least 5 nodes, got %d", len(result))
	}

	// Verify all IDs are present (except we might have root twice from both trees)
	expectedIDs := map[string]bool{
		"root":            false,
		"dep-1":           false,
		"dep-1-1":         false,
		"dependent-1":     false,
		"dependent-1-1":   false,
	}

	for _, node := range result {
		if _, exists := expectedIDs[node.ID]; exists {
			expectedIDs[node.ID] = true
		}
	}

	for id, found := range expectedIDs {
		if !found {
			t.Errorf("Expected ID %s in merged result", id)
		}
	}
}

func TestMergeBidirectionalTrees_ExcludesRootFromUp(t *testing.T) {
	// Test that root is excluded from upTree
	downTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
	}
	upTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
	}

	result := mergeBidirectionalTrees(downTree, upTree, "root")

	// Should have exactly 1 node (root)
	if len(result) != 1 {
		t.Errorf("Expected 1 node (root only), got %d", len(result))
	}

	if result[0].ID != "root" {
		t.Errorf("Expected root node, got %s", result[0].ID)
	}
}

func TestMergeBidirectionalTrees_PreservesDepth(t *testing.T) {
	// Test that depth values are preserved from original trees
	downTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dep-1", Title: "Dep 1", Status: types.StatusOpen},
			Depth:    5, // Non-standard depth to verify preservation
			ParentID: "root",
		},
	}
	upTree := []*types.TreeNode{
		{
			Issue:    types.Issue{ID: "root", Title: "Root", Status: types.StatusOpen},
			Depth:    0,
			ParentID: "",
		},
		{
			Issue:    types.Issue{ID: "dependent-1", Title: "Dependent 1", Status: types.StatusOpen},
			Depth:    3, // Different depth
			ParentID: "root",
		},
	}

	result := mergeBidirectionalTrees(downTree, upTree, "root")

	// Find nodes and verify their depths are preserved
	for _, node := range result {
		if node.ID == "dep-1" && node.Depth != 5 {
			t.Errorf("Expected dep-1 depth=5, got %d", node.Depth)
		}
		if node.ID == "dependent-1" && node.Depth != 3 {
			t.Errorf("Expected dependent-1 depth=3, got %d", node.Depth)
		}
	}
}

// Tests for child→parent dependency detection (bd-nim5)
func TestIsChildOf(t *testing.T) {
	tests := []struct {
		name     string
		childID  string
		parentID string
		want     bool
	}{
		// Positive cases: should be detected as child
		{
			name:     "direct child",
			childID:  "bd-abc.1",
			parentID: "bd-abc",
			want:     true,
		},
		{
			name:     "grandchild",
			childID:  "bd-abc.1.2",
			parentID: "bd-abc",
			want:     true,
		},
		{
			name:     "nested grandchild direct parent",
			childID:  "bd-abc.1.2",
			parentID: "bd-abc.1",
			want:     true,
		},
		{
			name:     "deeply nested child",
			childID:  "bd-abc.1.2.3",
			parentID: "bd-abc",
			want:     true,
		},

		// Negative cases: should NOT be detected as child
		{
			name:     "same ID",
			childID:  "bd-abc",
			parentID: "bd-abc",
			want:     false,
		},
		{
			name:     "not a child - unrelated IDs",
			childID:  "bd-xyz",
			parentID: "bd-abc",
			want:     false,
		},
		{
			name:     "not a child - sibling",
			childID:  "bd-abc.2",
			parentID: "bd-abc.1",
			want:     false,
		},
		{
			name:     "reversed - parent is not child of child",
			childID:  "bd-abc",
			parentID: "bd-abc.1",
			want:     false,
		},
		{
			name:     "prefix but not hierarchical",
			childID:  "bd-abcd",
			parentID: "bd-abc",
			want:     false,
		},
		{
			name:     "not hierarchical ID",
			childID:  "bd-abc",
			parentID: "bd-xyz",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isChildOf(tt.childID, tt.parentID)
			if got != tt.want {
				t.Errorf("isChildOf(%q, %q) = %v, want %v", tt.childID, tt.parentID, got, tt.want)
			}
		})
	}
}
