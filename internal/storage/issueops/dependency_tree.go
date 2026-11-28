package issueops

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

// GetDependencyTreeInTx returns a flattened dependency tree for visualization,
// in depth-first pre-order, up to maxDepth levels.
//
// The reads go level by level: each level costs one adjacency read per
// dependency plane, one issue read and one label read, each batched by
// queryBatchSize, with more for a far end found only in wisps. Walking node by
// node cost four to seven statements per node. The depth-first walk then runs
// over what was read and sends nothing. Edges are not read for a node whose
// shortest path from the root reaches the last level maxDepth allows. A node the
// walk first reaches on that level may still have had its edges read through a
// shorter path; the walk does not follow them.
//
// A node's edges are walked in order of the far end's id. Each issue is
// expanded at most once. A later edge into an issue already
// expanded (a diamond's second parent) is kept as a leaf row with Deduped set,
// reusing the issue already read; an edge back into the current path (a cycle)
// ends the descent with no row. An edge whose far end exists in neither table
// adds no row. showAllPaths is accepted and ignored.
func GetDependencyTreeInTx(ctx context.Context, tx DBTX, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	if maxDepth <= 0 {
		return nil, nil
	}
	root, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return nil, err
	}
	graph, err := readDependencyTreeGraphInTx(ctx, tx, issueID, root, maxDepth, reverse)
	if err != nil {
		return nil, err
	}
	walk := dependencyTreeWalk{
		graph:    graph,
		maxDepth: maxDepth,
		expanded: make(map[string]bool),
		onPath:   make(map[string]bool),
	}
	return walk.visit(issueID, 0, "", ""), nil
}

// dependencyTreeGraph is every issue and tree edge a walk can reach within its
// depth bound. An issue's entry in edges is absent when its edges were not read
// because the bound stops the walk there.
type dependencyTreeGraph struct {
	issues map[string]*types.Issue
	edges  map[string][]anchorEdge
}

// readDependencyTreeGraphInTx reads the graph breadth-first from root, so an
// issue is read at the shallowest depth any path reaches it. The walk may reach
// it first by a longer path, but only ever at that depth or deeper, so whatever
// the walk needs from it is already here. It can read a few issues the walk then
// does not return, when a depth limit cuts the only path the walk took to them;
// a failure reading one of those fails the whole read.
func readDependencyTreeGraphInTx(ctx context.Context, tx DBTX, rootID string, root *types.Issue, maxDepth int, reverse bool) (*dependencyTreeGraph, error) {
	graph := &dependencyTreeGraph{
		issues: map[string]*types.Issue{rootID: root},
		edges:  make(map[string][]anchorEdge),
	}
	frontier := []string{rootID}
	// absent holds the far ends found in neither table, so a later level that
	// references one does not read it again. The reads share one transaction.
	absent := make(map[string]bool)
	for depth := 0; depth+1 < maxDepth && len(frontier) > 0; depth++ {
		edges, err := anchorsEdgesInTx(ctx, tx, frontier, reverse)
		if err != nil {
			return nil, err
		}
		var unread []string
		queued := make(map[string]bool)
		for _, id := range frontier {
			kept := make([]anchorEdge, 0, len(edges[id]))
			for _, edge := range edges[id] {
				if !isDependencyTreeEdge(types.DependencyType(edge.depType)) {
					continue
				}
				kept = append(kept, edge)
				if _, read := graph.issues[edge.farID]; !read && !queued[edge.farID] && !absent[edge.farID] {
					queued[edge.farID] = true
					unread = append(unread, edge.farID)
				}
			}
			graph.edges[id] = kept
		}
		issues, err := dependencyTreeIssuesInTx(ctx, tx, unread)
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, id := range unread {
			if issue, ok := issues[id]; ok {
				graph.issues[id] = issue
				frontier = append(frontier, id)
			} else {
				absent[id] = true
			}
		}
	}
	return graph, nil
}

// dependencyTreeIssuesInTx reads ids with their labels, each from the table
// GetIssueInTx would answer from: issues first, and wisps only for an id issues
// does not hold. An id in neither table is absent from the result.
func dependencyTreeIssuesInTx(ctx context.Context, tx DBTX, ids []string) (map[string]*types.Issue, error) {
	found := make(map[string]*types.Issue, len(ids))
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids, map[string]struct{}{})
	if err != nil {
		return nil, fmt.Errorf("get dependency tree: fetch issues: %w", err)
	}
	for _, issue := range issues {
		found[issue.ID] = issue
	}

	var missing []string
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	wispSet, err := WispIDSetInTx(ctx, tx, missing)
	if err != nil {
		return nil, fmt.Errorf("get dependency tree: find wisps: %w", err)
	}
	var wispIDs []string
	for _, id := range missing {
		if _, ok := wispSet[id]; ok {
			wispIDs = append(wispIDs, id)
		}
	}
	wisps, err := GetIssuesByIDsInTx(ctx, tx, wispIDs, wispSet)
	if err != nil {
		return nil, fmt.Errorf("get dependency tree: fetch wisps: %w", err)
	}
	for _, wisp := range wisps {
		found[wisp.ID] = wisp
	}
	return found, nil
}

// anchorsEdgesInTx is anchorEdgesInTx for many anchors in one statement per
// batch and plane, keyed by anchor: each anchor's far ends are returned once,
// with the type of the first row seen, a durable row before a wisp copy. A
// missing wisp_dependencies table means no wisp-plane edges, as it does there.
//
// Each anchor's edges are sorted by far end. The statement has no ORDER BY, and
// the order of its rows depends on the plan, which differs between one id and
// several; the walk's output follows this order, since an issue reached twice
// is expanded under whichever edge comes first.
//
//nolint:gosec // G201: table and column names are hardcoded; ids ride ? placeholders.
func anchorsEdgesInTx(ctx context.Context, tx DBTX, anchors []string, inbound bool) (map[string][]anchorEdge, error) {
	nearCol, farCol, verb := "issue_id", DepTargetExpr, "dependencies"
	if inbound {
		nearCol, farCol, verb = DepTargetExpr, "issue_id", "dependents"
	}

	edges := make(map[string][]anchorEdge, len(anchors))
	seen := make(map[[2]string]struct{})
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
	batches:
		for start := 0; start < len(anchors); start += queryBatchSize {
			end := min(start+queryBatchSize, len(anchors))
			placeholders, args := buildSQLInClause(anchors[start:end])
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT %s, %s, type FROM %s WHERE %s IN (%s)`,
				nearCol, farCol, depTable, nearCol, placeholders), args...)
			if err != nil {
				if optionalBlockedTable(depTable) && dberrors.IsMissingTable(err, depTable) {
					break batches
				}
				return nil, fmt.Errorf("get %s from %s: %w", verb, depTable, err)
			}
			for rows.Next() {
				var anchor string
				var edge anchorEdge
				if scanErr := rows.Scan(&anchor, &edge.farID, &edge.depType); scanErr != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("get %s: scan: %w", verb, scanErr)
				}
				key := [2]string{anchor, edge.farID}
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				edges[anchor] = append(edges[anchor], edge)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("get %s: rows from %s: %w", verb, depTable, err)
			}
		}
	}
	for _, anchorEdges := range edges {
		sort.Slice(anchorEdges, func(i, j int) bool { return anchorEdges[i].farID < anchorEdges[j].farID })
	}
	return edges, nil
}

// dependencyTreeWalk is the depth-first walk over a graph already read.
type dependencyTreeWalk struct {
	graph    *dependencyTreeGraph
	maxDepth int
	expanded map[string]bool
	onPath   map[string]bool
}

func (w *dependencyTreeWalk) visit(issueID string, depth int, parentID string, edgeFromParent types.DependencyType) []*types.TreeNode {
	if depth >= w.maxDepth || w.onPath[issueID] {
		return nil
	}
	issue := w.graph.issues[issueID]
	if w.expanded[issueID] {
		return []*types.TreeNode{{
			Issue:          *issue,
			Depth:          depth,
			ParentID:       parentID,
			EdgeFromParent: edgeFromParent,
			Deduped:        true,
		}}
	}
	w.expanded[issueID] = true

	// TreeNode doesn't have Children field - return flat list
	nodes := []*types.TreeNode{{
		Issue:          *issue,
		Depth:          depth,
		ParentID:       parentID,
		EdgeFromParent: edgeFromParent,
	}}

	w.onPath[issueID] = true
	defer delete(w.onPath, issueID)
	for _, edge := range w.graph.edges[issueID] {
		if _, ok := w.graph.issues[edge.farID]; !ok {
			continue
		}
		nodes = append(nodes, w.visit(edge.farID, depth+1, issueID, types.DependencyType(edge.depType))...)
	}
	return nodes
}

func isDependencyTreeEdge(depType types.DependencyType) bool {
	return depType != types.DepRelatesTo
}
