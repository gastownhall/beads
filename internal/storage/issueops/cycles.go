package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// DetectCyclesInTx finds dependency cycles across both the dependencies and
// wisp_dependencies tables. Returns slices of issues forming each cycle.
// Only considers "blocks" dependencies for cycle detection.
//
//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
func DetectCyclesInTx(ctx context.Context, tx *sql.Tx) ([][]*types.Issue, error) {
	// Build adjacency list from both dependency tables.
	graph := make(map[string][]string)

	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, depends_on_id, type
			FROM %s
		`, depTable))
		if err != nil {
			return nil, fmt.Errorf("detect cycles: query %s: %w", depTable, err)
		}
		for rows.Next() {
			var issueID, dependsOnID, depType string
			if err := rows.Scan(&issueID, &dependsOnID, &depType); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("detect cycles: scan %s: %w", depTable, err)
			}
			if types.DependencyType(depType) == types.DepBlocks {
				graph[issueID] = append(graph[issueID], dependsOnID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("detect cycles: rows %s: %w", depTable, err)
		}
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
