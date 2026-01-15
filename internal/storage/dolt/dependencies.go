package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// AddDependency adds a dependency between two issues
func (s *DoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
		ON DUPLICATE KEY UPDATE type = VALUES(type), metadata = VALUES(metadata)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID)
	if err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}
	return nil
}

// RemoveDependency removes a dependency between two issues
func (s *DoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID)
	if err != nil {
		return fmt.Errorf("failed to remove dependency: %w", err)
	}
	return nil
}

// GetDependencies retrieves issues that this issue depends on
func (s *DoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id FROM issues i
		JOIN dependencies d ON i.id = d.depends_on_id
		WHERE d.issue_id = ?
		ORDER BY i.priority ASC, i.created_at DESC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// GetDependents retrieves issues that depend on this issue
func (s *DoltStore) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		WHERE d.depends_on_id = ?
		ORDER BY i.priority ASC, i.created_at DESC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependents: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// GetDependenciesWithMetadata returns dependencies with metadata
func (s *DoltStore) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.depends_on_id, d.type, d.created_at, d.created_by, d.metadata, d.thread_id
		FROM dependencies d
		WHERE d.issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies with metadata: %w", err)
	}
	defer rows.Close()

	var results []*types.IssueWithDependencyMetadata
	for rows.Next() {
		var depID, depType, createdBy string
		var createdAt sql.NullTime
		var metadata, threadID sql.NullString

		if err := rows.Scan(&depID, &depType, &createdAt, &createdBy, &metadata, &threadID); err != nil {
			return nil, fmt.Errorf("failed to scan dependency: %w", err)
		}

		issue, err := s.GetIssue(ctx, depID)
		if err != nil {
			return nil, err
		}
		if issue == nil {
			continue
		}

		result := &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(depType),
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// GetDependentsWithMetadata returns dependents with metadata
func (s *DoltStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.issue_id, d.type, d.created_at, d.created_by, d.metadata, d.thread_id
		FROM dependencies d
		WHERE d.depends_on_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependents with metadata: %w", err)
	}
	defer rows.Close()

	var results []*types.IssueWithDependencyMetadata
	for rows.Next() {
		var depID, depType, createdBy string
		var createdAt sql.NullTime
		var metadata, threadID sql.NullString

		if err := rows.Scan(&depID, &depType, &createdAt, &createdBy, &metadata, &threadID); err != nil {
			return nil, fmt.Errorf("failed to scan dependent: %w", err)
		}

		issue, err := s.GetIssue(ctx, depID)
		if err != nil {
			return nil, err
		}
		if issue == nil {
			continue
		}

		result := &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(depType),
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// GetDependencyRecords returns raw dependency records for an issue
func (s *DoltStore) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency records: %w", err)
	}
	defer rows.Close()

	return scanDependencyRows(rows)
}

// GetAllDependencyRecords returns all dependency records
func (s *DoltStore) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		ORDER BY issue_id
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get all dependency records: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*types.Dependency)
	for rows.Next() {
		dep, err := scanDependencyRow(rows)
		if err != nil {
			return nil, err
		}
		result[dep.IssueID] = append(result[dep.IssueID], dep)
	}
	return result, rows.Err()
}

// GetDependencyCounts returns dependency counts for multiple issues
func (s *DoltStore) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*types.DependencyCounts), nil
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Query for dependencies (blockers)
	// nolint:gosec // G201: inClause contains only ? placeholders, actual values passed via args
	depQuery := fmt.Sprintf(`
		SELECT issue_id, COUNT(*) as cnt
		FROM dependencies
		WHERE issue_id IN (%s) AND type = 'blocks'
		GROUP BY issue_id
	`, inClause)

	depRows, err := s.db.QueryContext(ctx, depQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency counts: %w", err)
	}
	defer depRows.Close()

	result := make(map[string]*types.DependencyCounts)
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{}
	}

	for depRows.Next() {
		var id string
		var cnt int
		if err := depRows.Scan(&id, &cnt); err != nil {
			return nil, fmt.Errorf("failed to scan dep count: %w", err)
		}
		if c, ok := result[id]; ok {
			c.DependencyCount = cnt
		}
	}

	// Query for dependents (blocking)
	// nolint:gosec // G201: inClause contains only ? placeholders, actual values passed via args
	blockingQuery := fmt.Sprintf(`
		SELECT depends_on_id, COUNT(*) as cnt
		FROM dependencies
		WHERE depends_on_id IN (%s) AND type = 'blocks'
		GROUP BY depends_on_id
	`, inClause)

	blockingRows, err := s.db.QueryContext(ctx, blockingQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocking counts: %w", err)
	}
	defer blockingRows.Close()

	for blockingRows.Next() {
		var id string
		var cnt int
		if err := blockingRows.Scan(&id, &cnt); err != nil {
			return nil, fmt.Errorf("failed to scan blocking count: %w", err)
		}
		if c, ok := result[id]; ok {
			c.DependentCount = cnt
		}
	}

	return result, nil
}

// GetDependencyTree returns a dependency tree for visualization
func (s *DoltStore) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	// Simple implementation - can be optimized with CTE
	visited := make(map[string]bool)
	return s.buildDependencyTree(ctx, issueID, 0, maxDepth, reverse, visited)
}

func (s *DoltStore) buildDependencyTree(ctx context.Context, issueID string, depth, maxDepth int, reverse bool, visited map[string]bool) ([]*types.TreeNode, error) {
	if depth >= maxDepth || visited[issueID] {
		return nil, nil
	}
	visited[issueID] = true

	issue, err := s.GetIssue(ctx, issueID)
	if err != nil || issue == nil {
		return nil, err
	}

	var childIDs []string
	var query string
	if reverse {
		query = "SELECT issue_id FROM dependencies WHERE depends_on_id = ?"
	} else {
		query = "SELECT depends_on_id FROM dependencies WHERE issue_id = ?"
	}

	rows, err := s.db.QueryContext(ctx, query, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		childIDs = append(childIDs, id)
	}

	node := &types.TreeNode{
		Issue: *issue,
		Depth: depth,
	}

	// TreeNode doesn't have Children field - return flat list
	nodes := []*types.TreeNode{node}
	for _, childID := range childIDs {
		children, err := s.buildDependencyTree(ctx, childID, depth+1, maxDepth, reverse, visited)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}

	return nodes, nil
}

// DetectCycles finds circular dependencies
func (s *DoltStore) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	// Get all dependencies
	deps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}

	// Build adjacency list
	graph := make(map[string][]string)
	for issueID, records := range deps {
		for _, dep := range records {
			if dep.Type == types.DepBlocks {
				graph[issueID] = append(graph[issueID], dep.DependsOnID)
			}
		}
	}

	// Find cycles using DFS
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
				// Found cycle - extract it
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
						issue, _ := s.GetIssue(ctx, id)
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

// IsBlocked checks if an issue has open blockers
func (s *DoltStore) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.depends_on_id
		FROM dependencies d
		JOIN issues i ON d.depends_on_id = i.id
		WHERE d.issue_id = ?
		  AND d.type = 'blocks'
		  AND i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
	`, issueID)
	if err != nil {
		return false, nil, fmt.Errorf("failed to check blockers: %w", err)
	}
	defer rows.Close()

	var blockers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return false, nil, err
		}
		blockers = append(blockers, id)
	}

	return len(blockers) > 0, blockers, rows.Err()
}

// GetNewlyUnblockedByClose finds issues that become unblocked when an issue is closed
func (s *DoltStore) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	// Find issues that were blocked only by the closed issue
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT d.issue_id
		FROM dependencies d
		JOIN issues i ON d.issue_id = i.id
		WHERE d.depends_on_id = ?
		  AND d.type = 'blocks'
		  AND i.status IN ('open', 'blocked')
		  AND NOT EXISTS (
			SELECT 1 FROM dependencies d2
			JOIN issues blocker ON d2.depends_on_id = blocker.id
			WHERE d2.issue_id = d.issue_id
			  AND d2.type = 'blocks'
			  AND d2.depends_on_id != ?
			  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  )
	`, closedIssueID, closedIssueID)
	if err != nil {
		return nil, fmt.Errorf("failed to find newly unblocked: %w", err)
	}
	defer rows.Close()

	return s.scanIssueIDs(ctx, rows)
}

// Helper functions

func (s *DoltStore) scanIssueIDs(ctx context.Context, rows *sql.Rows) ([]*types.Issue, error) {
	var issues []*types.Issue
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan issue id: %w", err)
		}
		issue, err := s.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues, rows.Err()
}

func scanDependencyRows(rows *sql.Rows) ([]*types.Dependency, error) {
	var deps []*types.Dependency
	for rows.Next() {
		dep, err := scanDependencyRow(rows)
		if err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func scanDependencyRow(rows *sql.Rows) (*types.Dependency, error) {
	var dep types.Dependency
	var createdAt sql.NullTime
	var metadata, threadID sql.NullString

	if err := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type, &createdAt, &dep.CreatedBy, &metadata, &threadID); err != nil {
		return nil, fmt.Errorf("failed to scan dependency: %w", err)
	}

	if createdAt.Valid {
		dep.CreatedAt = createdAt.Time
	}
	if threadID.Valid {
		dep.ThreadID = threadID.String
	}

	return &dep, nil
}
