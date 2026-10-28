package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// GetDependencyTreeInTx returns a flattened dependency tree for visualization.
// It performs a recursive depth-first pre-order traversal up to maxDepth, using
// GetIssueInTx and GetDependenciesWithMetadataInTx/GetDependentsWithMetadataInTx
// which handle wisp routing. showAllPaths is accepted and ignored: every issue
// is expanded once, under the first path that reached it, and each later path
// into it is answered with a Deduped stub row (see buildDependencyTreeInTx).
func GetDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	expanded := make(map[string]*types.Issue)
	onPath := make(map[string]bool)
	return buildDependencyTreeInTx(ctx, tx, issueID, 0, maxDepth, reverse, expanded, onPath, "", "")
}

// buildDependencyTreeInTx walks one issue and the tree edges under it,
// appending the nodes depth-first pre-order. Two sets tell the two kinds of
// revisit apart:
//
//   - onPath holds the ids on the current root-to-node path. A revisit from
//     there is a back-edge — a cycle — and adds no row, which is what keeps
//     the walk finite; the closing edge stays absent, as it always was.
//   - expanded holds the ids already expanded elsewhere in this walk, with
//     the issue each was read as. A revisit from there is a cross edge — a
//     diamond — and the edge is real, so it is answered with one leaf
//     TreeNode.Deduped stub carrying this path's ParentID, Depth and
//     EdgeFromParent. The stub reuses the issue already read and neither
//     reads nor descends.
//
// The root is on the path for the whole walk, so it is never stubbed: every
// edge back into it is a back-edge.
func buildDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, depth, maxDepth int, reverse bool, expanded map[string]*types.Issue, onPath map[string]bool, parentID string, edgeFromParent types.DependencyType) ([]*types.TreeNode, error) {
	if depth >= maxDepth || onPath[issueID] {
		return nil, nil
	}
	if seen, ok := expanded[issueID]; ok {
		return []*types.TreeNode{{Issue: *seen, Depth: depth, ParentID: parentID, EdgeFromParent: edgeFromParent, Deduped: true}}, nil
	}

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return nil, err
	}
	expanded[issueID] = issue
	onPath[issueID] = true
	defer delete(onPath, issueID)

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
		children, err := buildDependencyTreeInTx(ctx, tx, rel.ID, depth+1, maxDepth, reverse, expanded, onPath, issueID, rel.DependencyType)
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
