package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// DetectCyclesInTx finds dependency cycles across both the dependencies and
// wisp_dependencies tables. Returns slices of issues forming each cycle.
// Only considers "blocks" and "conditional-blocks" dependencies for cycle detection.
func DetectCyclesInTx(ctx context.Context, tx *sql.Tx) ([][]*types.Issue, error) {
	// Build adjacency list from both dependency tables.
	graph := make(map[string][]string)
	if err := AppendBlockingGraphInTx(ctx, tx, []string{"dependencies", "wisp_dependencies"}, graph); err != nil {
		return nil, err
	}

	// Find cycles using DFS.
	var cycles [][]*types.Issue
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := make([]string, 0)

	var dfs func(node string) bool
	dfs = func(node string) bool {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		for _, neighbor := range graph[node] {
			if !visited[neighbor] {
				if dfs(neighbor) {
					return true
				}
			} else if recStack[neighbor] {
				// Found cycle — extract it.
				cycleStart := -1
				for i, n := range path {
					if n == neighbor {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cyclePath := path[cycleStart:]
					var cycleIssues []*types.Issue
					for _, id := range cyclePath {
						issue, _ := GetIssueInTx(ctx, tx, id)
						if issue != nil {
							cycleIssues = append(cycleIssues, issue)
						}
					}
					if len(cycleIssues) > 0 {
						cycles = append(cycles, cycleIssues)
					}
				}
			}
		}

		path = path[:len(path)-1]
		recStack[node] = false
		return false
	}

	for node := range graph {
		if !visited[node] {
			dfs(node)
		}
	}

	return cycles, nil
}

// AppendBlockingGraphInTx adds the blocking-type ("blocks",
// "conditional-blocks") dependency edges from the given tables on tx into
// graph as adjacency lists. The caller may merge tables read from different
// transactions into one graph (dolt server mode keeps wisp writes on a
// separate ignored tx).
//
//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
func AppendBlockingGraphInTx(ctx context.Context, tx *sql.Tx, depTables []string, graph map[string][]string) error {
	for _, depTable := range depTables {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, %s AS depends_on_id, type
			FROM %s
		`, DepTargetExpr, depTable))
		if err != nil {
			return fmt.Errorf("blocking graph: query %s: %w", depTable, err)
		}
		for rows.Next() {
			var issueID, dependsOnID, depType string
			if err := rows.Scan(&issueID, &dependsOnID, &depType); err != nil {
				_ = rows.Close()
				return fmt.Errorf("blocking graph: scan %s: %w", depTable, err)
			}
			if types.DependencyType(depType) == types.DepBlocks || types.DependencyType(depType) == types.DepConditionalBlocks {
				graph[issueID] = append(graph[issueID], dependsOnID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("blocking graph: rows %s: %w", depTable, err)
		}
	}
	return nil
}

// CycleThroughEdgesInGraph reports a rendered blocking cycle that traverses
// one of the new edges (issueID -> dependsOnID pairs), or "" when no new edge
// lies on a cycle. An edge u -> v is on a cycle exactly when u is reachable
// from v, so this is precise where cycle enumeration is not: a DFS-based
// detector records one cycle per back edge and can report a pre-existing
// cycle through the same nodes instead of the one the new edge created
// (bd-578h9.9). The graph must already contain the new edges.
func CycleThroughEdgesInGraph(graph map[string][]string, edges [][2]string) string {
	for _, edge := range edges {
		source, target := edge[0], edge[1]
		if source == "" || target == "" {
			continue
		}
		if source == target {
			return source + " → " + source
		}
		path := reachPath(graph, target, source)
		if path == nil {
			continue
		}
		// path runs target ⇝ source inclusive; the new edge closes the cycle.
		ids := append([]string{source}, path...)
		return strings.Join(ids, " → ")
	}
	return ""
}

// reachPath returns a BFS path from start to goal in graph (inclusive of
// both), or nil when goal is unreachable. start == goal returns [start].
func reachPath(graph map[string][]string, start, goal string) []string {
	if start == goal {
		return []string{start}
	}
	parent := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, next := range graph[node] {
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = node
			if next == goal {
				path := []string{goal}
				for at := node; at != ""; at = parent[at] {
					path = append(path, at)
				}
				// Reverse: built goal-back-to-start.
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path
			}
			queue = append(queue, next)
		}
	}
	return nil
}
