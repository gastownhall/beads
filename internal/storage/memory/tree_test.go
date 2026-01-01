package memory

import (
    "context"
    "testing"

    "github.com/steveyegge/beads/internal/types"
)

func TestGetDependencyTree_IncludesRoot(t *testing.T) {
    m := New("")
    
    // Create parent and child issues
    parent := &types.Issue{
        ID:       "bd-7zka",
        Title:    "Parent issue",
        Status:   types.StatusOpen,
        Priority: 3,
    }
    child := &types.Issue{
        ID:       "bd-7zka.2",
        Title:    "Child issue",
        Status:   types.StatusOpen,
        Priority: 3,
        Dependencies: []*types.Dependency{
            {IssueID: "bd-7zka.2", DependsOnID: "bd-7zka", Type: "blocks"},
        },
    }
    
    if err := m.LoadFromIssues([]*types.Issue{parent, child}); err != nil {
        t.Fatalf("LoadFromIssues failed: %v", err)
    }
    
    tree, err := m.GetDependencyTree(context.Background(), "bd-7zka.2", 50, false, false)
    if err != nil {
        t.Fatalf("GetDependencyTree failed: %v", err)
    }
    
    // Should have 2 nodes: root at depth 0, dependency at depth 1
    if len(tree) != 2 {
        t.Errorf("Expected 2 nodes, got %d", len(tree))
        for i, node := range tree {
            t.Logf("  [%d] ID=%s, Depth=%d, ParentID=%s", i, node.ID, node.Depth, node.ParentID)
        }
        return
    }
    
    // First node should be root at depth 0
    if tree[0].ID != "bd-7zka.2" {
        t.Errorf("Expected root ID 'bd-7zka.2', got '%s'", tree[0].ID)
    }
    if tree[0].Depth != 0 {
        t.Errorf("Expected root depth 0, got %d", tree[0].Depth)
    }
    
    // Second node should be dependency at depth 1
    if tree[1].ID != "bd-7zka" {
        t.Errorf("Expected dependency ID 'bd-7zka', got '%s'", tree[1].ID)
    }
    if tree[1].Depth != 1 {
        t.Errorf("Expected dependency depth 1, got %d", tree[1].Depth)
    }
    if tree[1].ParentID != "bd-7zka.2" {
        t.Errorf("Expected dependency ParentID 'bd-7zka.2', got '%s'", tree[1].ParentID)
    }
}
