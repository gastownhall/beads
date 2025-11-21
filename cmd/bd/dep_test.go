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
