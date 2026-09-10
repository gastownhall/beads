package issueops

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The pure half of the tree walk: the request vocabulary, the ancestor-keeping
// prune and the two-walk concatenation. Every rule these pin is one a
// conformance case would otherwise have to pay a database to observe, and none
// of them needs one -- which is what step 3 of engdocs/ADDING_AN_ISSUEOPS_ROLE.md
// asks for when a role's body has to live at transaction level.
//
// TestPruneTreeByStatus and the TestMergeBidirectionalTree_* family MOVED HERE
// from cmd/bd/dep_test.go with the functions they exercise.

func TestPruneTreeByStatus(t *testing.T) {
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
		filtered := PruneTreeByStatus(tree, types.StatusOpen)

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
		filtered := PruneTreeByStatus(tree, types.StatusClosed)

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
		filtered := PruneTreeByStatus(tree, types.StatusBlocked)
		if len(filtered) != 0 {
			t.Errorf("Expected empty tree when filtering to non-matching status, got %d nodes", len(filtered))
		}
	})

	t.Run("filter empty tree", func(t *testing.T) {
		filtered := PruneTreeByStatus([]*types.TreeNode{}, types.StatusOpen)
		if len(filtered) != 0 {
			t.Errorf("Expected empty tree, got %d nodes", len(filtered))
		}
	})
}

func TestMergeBidirectionalTree_Empty(t *testing.T) {
	// Test merging empty trees
	downTree := []*types.TreeNode{}
	upTree := []*types.TreeNode{}
	rootID := "test-root"

	result := MergeBidirectionalTree(downTree, upTree, rootID)

	if len(result) != 0 {
		t.Errorf("Expected empty result for empty trees, got %d nodes", len(result))
	}
}

func TestMergeBidirectionalTree_OnlyDown(t *testing.T) {
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

	result := MergeBidirectionalTree(downTree, upTree, "test-root")

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

func TestMergeBidirectionalTree_WithDependents(t *testing.T) {
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

	result := MergeBidirectionalTree(downTree, upTree, "test-root")

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

func TestMergeBidirectionalTree_MultipleDepth(t *testing.T) {
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

	result := MergeBidirectionalTree(downTree, upTree, "root")

	// Should include all nodes from both trees (minus duplicate root)
	if len(result) < 5 {
		t.Errorf("Expected at least 5 nodes, got %d", len(result))
	}

	// Verify all IDs are present (except we might have root twice from both trees)
	expectedIDs := map[string]bool{
		"root":          false,
		"dep-1":         false,
		"dep-1-1":       false,
		"dependent-1":   false,
		"dependent-1-1": false,
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

func TestMergeBidirectionalTree_ExcludesRootFromUp(t *testing.T) {
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

	result := MergeBidirectionalTree(downTree, upTree, "root")

	// Should have exactly 1 node (root)
	if len(result) != 1 {
		t.Errorf("Expected 1 node (root only), got %d", len(result))
	}

	if result[0].ID != "root" {
		t.Errorf("Expected root node, got %s", result[0].ID)
	}
}

func TestMergeBidirectionalTree_PreservesDepth(t *testing.T) {
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

	result := MergeBidirectionalTree(downTree, upTree, "root")

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

// TestValidateWalkTreeRequest pins the request vocabulary issueops.TreeWalker
// documents, in the one place all three backends share.
func TestValidateWalkTreeRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     publicops.WalkTreeRequest
		want    publicops.TreeDirection
		wantErr bool
	}{
		{
			// treewalker.go: "The empty string means TreeDown, so a zero request
			// walks dependencies".
			name: "an unset direction defaults to down",
			req:  publicops.WalkTreeRequest{RootID: "bd-1", MaxDepth: 1},
			want: publicops.TreeDown,
		},
		{
			name: "up is accepted",
			req:  publicops.WalkTreeRequest{RootID: "bd-1", MaxDepth: 1, Direction: publicops.TreeUp},
			want: publicops.TreeUp,
		},
		{
			name: "both is accepted",
			req:  publicops.WalkTreeRequest{RootID: "bd-1", MaxDepth: 1, Direction: publicops.TreeBoth},
			want: publicops.TreeBoth,
		},
		{
			// treewalker.go: "Any other value is ErrValidation: the vocabulary is
			// CLOSED".
			name:    "a fourth direction is refused rather than read as down",
			req:     publicops.WalkTreeRequest{RootID: "bd-1", MaxDepth: 1, Direction: "sideways"},
			wantErr: true,
		},
		{
			name:    "an empty root is refused",
			req:     publicops.WalkTreeRequest{MaxDepth: 1},
			wantErr: true,
		},
		{
			// treewalker.go: "IT IS REQUIRED, and 0 is ErrValidation rather than
			// 'unbounded'".
			name:    "a zero depth is refused rather than meaning unbounded",
			req:     publicops.WalkTreeRequest{RootID: "bd-1"},
			wantErr: true,
		},
		{
			name:    "a negative depth is refused",
			req:     publicops.WalkTreeRequest{RootID: "bd-1", MaxDepth: -1},
			wantErr: true,
		},
		{
			name:    "a negative cap is refused",
			req:     publicops.WalkTreeRequest{RootID: "bd-1", MaxDepth: 1, MaxRows: -1},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateWalkTreeRequest(tt.req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateWalkTreeRequest(%+v) = %q, nil; want ErrValidation", tt.req, got)
				}
				if !errors.Is(err, publicops.ErrValidation) {
					t.Fatalf("ValidateWalkTreeRequest error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateWalkTreeRequest(%+v) error = %v", tt.req, err)
			}
			if got != tt.want {
				t.Errorf("ValidateWalkTreeRequest(%+v) direction = %q, want %q", tt.req, got, tt.want)
			}
		})
	}
}

// TestPruneTreeByStatusMatchingNothingReturnsNothing pins the sharp edge
// issueops.WalkTreeRequest.Status states out loud: the root survives only as
// somebody's ancestor, so a tree with no matching member comes back EMPTY rather
// than as a lone root.
func TestPruneTreeByStatusMatchingNothingReturnsNothing(t *testing.T) {
	tree := []*types.TreeNode{
		{Issue: types.Issue{ID: "root", Status: types.StatusOpen}},
		{Issue: types.Issue{ID: "child", Status: types.StatusOpen}, Depth: 1, ParentID: "root"},
	}
	if got := PruneTreeByStatus(tree, types.StatusClosed); len(got) != 0 {
		t.Fatalf("PruneTreeByStatus kept %d nodes, want 0: the root is kept only as an ancestor", len(got))
	}
	// An unrecognized status is not checked against the workspace vocabulary; it
	// simply matches nothing, which treewalker.go states rather than tightens.
	if got := PruneTreeByStatus(tree, types.Status("no-such-status")); len(got) != 0 {
		t.Fatalf("PruneTreeByStatus kept %d nodes for an unknown status, want 0", len(got))
	}
}

// TestPruneTreeByStatusKeepsAMatchBehindANonMatch pins the difference between a
// post-walk prune and a filter on the walk: a matching node whose parent does
// not match is still reached, and its parent is kept so the answer is still a
// tree.
func TestPruneTreeByStatusKeepsAMatchBehindANonMatch(t *testing.T) {
	tree := []*types.TreeNode{
		{Issue: types.Issue{ID: "root", Status: types.StatusOpen}},
		{Issue: types.Issue{ID: "mid", Status: types.StatusClosed}, Depth: 1, ParentID: "root"},
		{Issue: types.Issue{ID: "deep", Status: types.StatusOpen}, Depth: 2, ParentID: "mid"},
	}
	got := PruneTreeByStatus(tree, types.StatusOpen)
	var ids []string
	for _, node := range got {
		ids = append(ids, node.ID)
	}
	want := []string{"root", "mid", "deep"}
	if len(ids) != len(want) {
		t.Fatalf("PruneTreeByStatus = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("PruneTreeByStatus = %v, want %v (walk order preserved)", ids, want)
		}
	}
}

// TestMergeBidirectionalTreeCopiesTheUpNodes pins the aliasing rule: the up half
// is cloned, so a caller mutating the merged answer cannot reach into the slice
// the up walk returned.
func TestMergeBidirectionalTreeCopiesTheUpNodes(t *testing.T) {
	upNode := &types.TreeNode{Issue: types.Issue{ID: "dependent"}, Depth: 1, ParentID: "root"}
	downTree := []*types.TreeNode{{Issue: types.Issue{ID: "root"}}}
	upTree := []*types.TreeNode{{Issue: types.Issue{ID: "root"}}, upNode}

	merged := MergeBidirectionalTree(downTree, upTree, "root")
	if len(merged) != 2 {
		t.Fatalf("merged %d nodes, want 2 (root once, from the down walk)", len(merged))
	}
	if merged[0] == upNode {
		t.Fatal("the up half was aliased, not copied")
	}
	merged[0].Depth = 99
	if upNode.Depth != 1 {
		t.Errorf("mutating the merged answer reached the up walk's node: depth = %d", upNode.Depth)
	}
}
