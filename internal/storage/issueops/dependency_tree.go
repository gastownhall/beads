package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// GetDependencyTreeInTx returns a flattened dependency tree for visualization.
// It performs a recursive depth-first, pre-order traversal up to maxDepth, using
// GetIssueInTx and GetDependenciesInTx/GetDependentsInTx which handle wisp
// routing.
//
// Each issue is expanded at most once. A later edge into an issue already
// expanded (a diamond's second parent) is kept as a leaf row with Deduped set,
// reusing the issue already read; an edge back into the current path (a cycle)
// ends the descent with no row. showAllPaths is accepted and ignored.
//
// After the walk, stampTreeBlockedInTx sets every node's IsBlocked from the
// stored transitive flag, the same projection `bd ready` and `bd blocked` read.
func GetDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	expanded := make(map[string]*types.Issue)
	onPath := make(map[string]bool)
	nodes, err := buildDependencyTreeInTx(ctx, tx, issueID, 0, maxDepth, reverse, expanded, onPath, "", "")
	if err != nil {
		return nil, err
	}
	if err := stampTreeBlockedInTx(ctx, tx, issueID, nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// stampTreeBlockedInTx sets each node's IsBlocked from the stored transitive
// is_blocked flag, read in the same transaction as the walk (IsBlockedBatchInTx).
// The walk hydrates issues without the flag, and the renderer's visible-edge
// scan cannot see blocking a depth or status prune hid or that a node inherited
// through its parent chain (#6630), so the flag is read here rather than at the
// renderer, where the served and JSON answers would stay unpopulated.
//
// EVERY OCCURRENCE IS STAMPED, a Deduped stub included: the stub copies the
// issue before this runs, so stamping the expanded node alone would leave the
// copy behind. A read failure propagates with the root's id rather than
// silently stamping false; an empty walk reads nothing.
func stampTreeBlockedInTx(ctx context.Context, tx DBTX, rootID string, nodes []*types.TreeNode) error {
	if len(nodes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node == nil || seen[node.ID] {
			continue
		}
		seen[node.ID] = true
		ids = append(ids, node.ID)
	}
	blocked, err := IsBlockedBatchInTx(ctx, tx, ids)
	if err != nil {
		return fmt.Errorf("dependency tree %s: read is_blocked: %w", rootID, err)
	}
	for _, node := range nodes {
		if node == nil {
			continue
		}
		node.Issue.IsBlocked = blocked[node.ID]
	}
	return nil
}

func buildDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, depth, maxDepth int, reverse bool, expanded map[string]*types.Issue, onPath map[string]bool, parentID string, edgeFromParent types.DependencyType) ([]*types.TreeNode, error) {
	if depth >= maxDepth || onPath[issueID] {
		return nil, nil
	}
	if seen, ok := expanded[issueID]; ok {
		return []*types.TreeNode{{
			Issue:          *seen,
			Depth:          depth,
			ParentID:       parentID,
			EdgeFromParent: edgeFromParent,
			Deduped:        true,
		}}, nil
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
