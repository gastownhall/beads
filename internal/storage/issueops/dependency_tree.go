package issueops

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/types"
)

// GetDependencyTreeInTx returns a flattened dependency tree for visualization.
// It performs a recursive BFS traversal up to maxDepth, using GetIssueInTx and
// GetDependenciesInTx/GetDependentsInTx which handle wisp routing.
func GetDependencyTreeInTx(ctx context.Context, tx *sql.Tx, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	visited := make(map[string]bool)
	return buildDependencyTreeInTx(ctx, tx, issueID, 0, maxDepth, reverse, visited, "")
}

func buildDependencyTreeInTx(ctx context.Context, tx *sql.Tx, issueID string, depth, maxDepth int, reverse bool, visited map[string]bool, parentID string) ([]*types.TreeNode, error) {
	if depth >= maxDepth || visited[issueID] {
		return nil, nil
	}
	visited[issueID] = true

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return nil, err
	}

	// Use GetDependencies/GetDependents which handle wisp routing,
	// instead of querying the dependencies table directly (GH#2145).
	var related []*types.Issue
	if reverse {
		related, err = GetDependentsInTx(ctx, tx, issueID)
	} else {
		related, err = GetDependenciesInTx(ctx, tx, issueID)
	}
	if err != nil {
		return nil, err
	}

	node := &types.TreeNode{
		Issue:    *issue,
		Depth:    depth,
		ParentID: parentID,
	}

	// TreeNode doesn't have Children field - return flat list
	nodes := []*types.TreeNode{node}
	for _, rel := range related {
		children, err := buildDependencyTreeInTx(ctx, tx, rel.ID, depth+1, maxDepth, reverse, visited, issueID)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}

	return nodes, nil
}
