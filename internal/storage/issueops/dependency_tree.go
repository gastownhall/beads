package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// GetDependencyTreeInTx returns a flattened dependency tree for visualization.
// It performs a recursive BFS traversal up to maxDepth, using GetIssueInTx and
// GetDependenciesInTx/GetDependentsInTx which handle wisp routing.
func GetDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	visited := make(map[string]bool)
	nodes, err := buildDependencyTreeInTx(ctx, tx, issueID, 0, maxDepth, reverse, visited, "", "")
	if err != nil || len(nodes) == 0 {
		return nodes, err
	}
	// Each node carries the stored, transitive is_blocked flag — the column
	// `bd ready` and `bd blocked` read — so a renderer never rebuilds readiness
	// from the direct edges it happens to see (GH#6630).
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	blocked, err := IsBlockedBatchInTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		node.IsBlocked = blocked[node.ID]
	}
	return nodes, nil
}

func buildDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, depth, maxDepth int, reverse bool, visited map[string]bool, parentID string, edgeFromParent types.DependencyType) ([]*types.TreeNode, error) {
	if depth >= maxDepth || visited[issueID] {
		return nil, nil
	}
	visited[issueID] = true

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return nil, err
	}

	// Use metadata-aware queries to get dependency type for tree annotation (GH#3565).
	var related []*types.IssueWithDependencyMetadata
	if reverse {
		related, err = GetDependentsWithMetadataInTx(ctx, tx, issueID)
	} else {
		related, err = GetDependenciesWithMetadataInTx(ctx, tx, issueID)
	}
	if err != nil {
		return nil, err
	}

	node := &types.TreeNode{
		Issue:          *issue,
		Depth:          depth,
		ParentID:       parentID,
		EdgeFromParent: edgeFromParent,
	}

	// TreeNode doesn't have Children field - return flat list
	nodes := []*types.TreeNode{node}
	for _, rel := range related {
		if !isDependencyTreeEdge(rel.DependencyType) {
			continue
		}
		children, err := buildDependencyTreeInTx(ctx, tx, rel.ID, depth+1, maxDepth, reverse, visited, issueID, rel.DependencyType)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}

	return nodes, nil
}

func isDependencyTreeEdge(depType types.DependencyType) bool {
	return depType != types.DepRelatesTo
}
