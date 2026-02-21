package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// AddDependency adds a dependency between two issues.
// Uses an explicit transaction so writes persist when @@autocommit is OFF
// (e.g. Dolt server started with --no-auto-commit).
func (s *DoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	// Route to wisp_dependencies if the issue is ephemeral
	if IsEphemeralID(dep.IssueID) {
		return s.addWispDependency(ctx, dep, actor)
	}

	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Validate that the source issue exists
	var issueExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, dep.IssueID).Scan(&issueExists); err != nil {
		return fmt.Errorf("failed to check issue existence: %w", err)
	}
	if issueExists == 0 {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}

	// Validate that the target issue exists (skip for external cross-rig references)
	if !strings.HasPrefix(dep.DependsOnID, "external:") {
		var targetExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, dep.DependsOnID).Scan(&targetExists); err != nil {
			return fmt.Errorf("failed to check target issue existence: %w", err)
		}
		if targetExists == 0 {
			return fmt.Errorf("issue %s not found", dep.DependsOnID)
		}
	}

	// Cycle detection for blocking dependency types: check if adding this edge
	// would create a cycle by seeing if depends_on_id can already reach issue_id.
	if dep.Type == types.DepBlocks {
		var reachable int
		if err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE reachable AS (
				SELECT ? AS node, 0 AS depth
				UNION ALL
				SELECT d.depends_on_id, r.depth + 1
				FROM reachable r
				JOIN dependencies d ON d.issue_id = r.node
				WHERE d.type = 'blocks'
				  AND r.depth < 100
			)
			SELECT COUNT(*) FROM reachable WHERE node = ?
		`, dep.DependsOnID, dep.IssueID).Scan(&reachable); err != nil {
			return fmt.Errorf("failed to check for dependency cycle: %w", err)
		}
		if reachable > 0 {
			return fmt.Errorf("adding dependency would create a cycle")
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
		ON DUPLICATE KEY UPDATE type = VALUES(type), metadata = VALUES(metadata)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}

	return tx.Commit()
}

// RemoveDependency removes a dependency between two issues.
// Uses an explicit transaction so writes persist when @@autocommit is OFF.
func (s *DoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	// Route to wisp_dependencies if the issue is ephemeral
	if IsEphemeralID(issueID) {
		return s.removeWispDependency(ctx, issueID, dependsOnID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID); err != nil {
		return fmt.Errorf("failed to remove dependency: %w", err)
	}

	return tx.Commit()
}

// GetDependencies retrieves issues that this issue depends on
func (s *DoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	// Route to wisp_dependencies if the issue is ephemeral
	if IsEphemeralID(issueID) {
		return s.getWispDependencies(ctx, issueID)
	}

	rows, err := s.queryContext(ctx, `
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
	// Route to wisp_dependencies if the issue is ephemeral
	if IsEphemeralID(issueID) {
		return s.getWispDependents(ctx, issueID)
	}

	rows, err := s.queryContext(ctx, `
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
	// Route ephemeral IDs to wisp_dependencies
	if IsEphemeralID(issueID) {
		return s.getWispDependenciesWithMetadata(ctx, issueID)
	}

	rows, err := s.queryContext(ctx, `
		SELECT d.depends_on_id, d.type, d.created_at, d.created_by, d.metadata, d.thread_id
		FROM dependencies d
		WHERE d.issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies with metadata: %w", err)
	}

	// Collect dep metadata first, then close rows before fetching issues.
	// This avoids connection pool deadlock when MaxOpenConns=1 (embedded dolt).
	type depMeta struct {
		depID, depType string
	}
	var deps []depMeta
	for rows.Next() {
		var depID, depType, createdBy string
		var createdAt sql.NullTime
		var metadata, threadID sql.NullString

		if err := rows.Scan(&depID, &depType, &createdAt, &createdBy, &metadata, &threadID); err != nil {
			_ = rows.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("failed to scan dependency: %w", err)
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // Best effort cleanup on error path
		return nil, err
	}
	_ = rows.Close() // Redundant close for safety (rows already iterated)

	if len(deps) == 0 {
		return nil, nil
	}

	// Batch-fetch all issues after rows are closed (connection released)
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependentsWithMetadata returns dependents with metadata
func (s *DoltStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	// Route ephemeral IDs to wisp_dependencies
	if IsEphemeralID(issueID) {
		return s.getWispDependentsWithMetadata(ctx, issueID)
	}

	rows, err := s.queryContext(ctx, `
		SELECT d.issue_id, d.type, d.created_at, d.created_by, d.metadata, d.thread_id
		FROM dependencies d
		WHERE d.depends_on_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependents with metadata: %w", err)
	}

	// Collect dep metadata first, then close rows before fetching issues.
	// This avoids connection pool deadlock when MaxOpenConns=1 (embedded dolt).
	type depMeta struct {
		depID, depType string
	}
	var deps []depMeta
	for rows.Next() {
		var depID, depType, createdBy string
		var createdAt sql.NullTime
		var metadata, threadID sql.NullString

		if err := rows.Scan(&depID, &depType, &createdAt, &createdBy, &metadata, &threadID); err != nil {
			_ = rows.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("failed to scan dependent: %w", err)
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // Best effort cleanup on error path
		return nil, err
	}
	_ = rows.Close() // Redundant close for safety (rows already iterated)

	if len(deps) == 0 {
		return nil, nil
	}

	// Batch-fetch all issues after rows are closed (connection released)
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependencyRecords returns raw dependency records for an issue
func (s *DoltStore) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	// Route to wisp_dependencies if the issue is ephemeral
	if IsEphemeralID(issueID) {
		return s.getWispDependencyRecords(ctx, issueID)
	}

	rows, err := s.queryContext(ctx, `
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
	rows, err := s.queryContext(ctx, `
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

// GetDependencyRecordsForIssues returns dependency records for specific issues
func (s *DoltStore) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Dependency), nil
	}

	// Partition and merge from wisps and issues tables
	ephIDs, doltIDs := partitionIDs(issueIDs)
	if len(ephIDs) > 0 {
		result := make(map[string][]*types.Dependency)
		for _, id := range ephIDs {
			deps, err := s.getWispDependencyRecords(ctx, id)
			if err != nil {
				return nil, err
			}
			if len(deps) > 0 {
				result[id] = deps
			}
		}
		if len(doltIDs) > 0 {
			doltResult, err := s.getDependencyRecordsForIssuesDolt(ctx, doltIDs)
			if err != nil {
				return nil, err
			}
			for k, v := range doltResult {
				result[k] = v
			}
		}
		return result, nil
	}

	return s.getDependencyRecordsForIssuesDolt(ctx, issueIDs)
}

func (s *DoltStore) getDependencyRecordsForIssuesDolt(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// nolint:gosec // G201: inClause contains only ? placeholders, actual values passed via args
	query := fmt.Sprintf(`
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		WHERE issue_id IN (%s)
		ORDER BY issue_id
	`, inClause)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency records for issues: %w", err)
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

// GetBlockingInfoForIssues returns blocking dependency records relevant to a set of issue IDs.
// It fetches both directions:
//   - Dependencies where issue_id is in the set ("this issue is blocked by X")
//   - Dependencies where depends_on_id is in the set ("this issue blocks Y")
//
// It also returns a set of closed blocker IDs for filtering stale blocked-by annotations.
// This replaces the expensive pattern of GetAllDependencyRecords + getClosedBlockerIDs
// which loaded the entire dependency table and did N+1 issue lookups. (bd-7di)
func (s *DoltStore) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (
	blockedByMap map[string][]string, // issueID -> list of IDs blocking it
	blocksMap map[string][]string, // issueID -> list of IDs it blocks
	err error,
) {
	blockedByMap = make(map[string][]string)
	blocksMap = make(map[string][]string)

	if len(issueIDs) == 0 {
		return blockedByMap, blocksMap, nil
	}

	// Partition and merge wisp and dolt IDs
	ephIDs, doltIDs := partitionIDs(issueIDs)
	if len(ephIDs) > 0 {
		// For wisp IDs, query wisp_dependencies with a LEFT JOIN to wisps
		for _, ephID := range ephIDs {
			deps, depErr := s.getWispDependencyRecords(ctx, ephID)
			if depErr != nil {
				return nil, nil, depErr
			}
			for _, dep := range deps {
				if dep.Type == types.DepBlocks || dep.Type == types.DepParentChild {
					blockedByMap[ephID] = append(blockedByMap[ephID], dep.DependsOnID)
				}
			}
		}
		if len(doltIDs) == 0 {
			return blockedByMap, blocksMap, nil
		}
		issueIDs = doltIDs
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Query 1: Get "blocked by" relationships — deps where issue_id is in our set
	// and the dependency type affects ready work (blocks, parent-child).
	// nolint:gosec // G201: inClause contains only ? placeholders, actual values passed via args
	blockedByQuery := fmt.Sprintf(`
		SELECT d.issue_id, d.depends_on_id, d.type, COALESCE(i.status, '') AS blocker_status
		FROM dependencies d
		LEFT JOIN issues i ON i.id = d.depends_on_id
		WHERE d.issue_id IN (%s) AND d.type IN ('blocks', 'parent-child')
	`, inClause)

	rows, qErr := s.queryContext(ctx, blockedByQuery, args...)
	if qErr != nil {
		return nil, nil, fmt.Errorf("failed to get blocked-by info: %w", qErr)
	}
	for rows.Next() {
		var issueID, blockerID, depType, blockerStatus string
		if scanErr := rows.Scan(&issueID, &blockerID, &depType, &blockerStatus); scanErr != nil {
			_ = rows.Close()
			return nil, nil, scanErr
		}
		// Skip closed blockers — the dependency record is preserved, but a
		// closed blocker no longer blocks work.
		if types.Status(blockerStatus) == types.StatusClosed {
			continue
		}
		blockedByMap[issueID] = append(blockedByMap[issueID], blockerID)
	}
	_ = rows.Close()
	if rowErr := rows.Err(); rowErr != nil {
		return nil, nil, rowErr
	}

	// Query 2: Get "blocks" relationships — deps where depends_on_id is in our set
	// (shows what the displayed issues block).
	// nolint:gosec // G201: inClause contains only ? placeholders, actual values passed via args
	blocksQuery := fmt.Sprintf(`
		SELECT d.depends_on_id, d.issue_id, d.type, COALESCE(i.status, '') AS blocker_status
		FROM dependencies d
		LEFT JOIN issues i ON i.id = d.depends_on_id
		WHERE d.depends_on_id IN (%s) AND d.type IN ('blocks', 'parent-child')
	`, inClause)

	rows2, qErr2 := s.queryContext(ctx, blocksQuery, args...)
	if qErr2 != nil {
		return nil, nil, fmt.Errorf("failed to get blocks info: %w", qErr2)
	}
	for rows2.Next() {
		var blockerID, blockedID, depType, blockerStatus string
		if scanErr := rows2.Scan(&blockerID, &blockedID, &depType, &blockerStatus); scanErr != nil {
			_ = rows2.Close()
			return nil, nil, scanErr
		}
		// Skip if the blocker (our displayed issue) is closed
		if types.Status(blockerStatus) == types.StatusClosed {
			continue
		}
		// Skip parent-child in "blocks" map (those are structural, not blocking)
		if depType == "parent-child" {
			continue
		}
		blocksMap[blockerID] = append(blocksMap[blockerID], blockedID)
	}
	_ = rows2.Close()
	if rowErr2 := rows2.Err(); rowErr2 != nil {
		return nil, nil, rowErr2
	}

	return blockedByMap, blocksMap, nil
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

	depRows, err := s.queryContext(ctx, depQuery, args...)
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

	blockingRows, err := s.queryContext(ctx, blockingQuery, args...)
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

	rows, err := s.queryContext(ctx, query, issueID)
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
						issue, _ := s.GetIssue(ctx, id) // Best effort: nil issue handled by caller
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
	rows, err := s.queryContext(ctx, `
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
	rows, err := s.queryContext(ctx, `
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
	// First, collect all IDs
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan issue id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Close rows before the nested GetIssuesByIDs query.
	// MySQL server mode (go-sql-driver/mysql) can't handle multiple active
	// result sets on one connection - the first must be closed before starting
	// a new query, otherwise "driver: bad connection" errors occur.
	// Closing here is safe because sql.Rows.Close() is idempotent.
	_ = rows.Close() // Redundant close for safety (rows already iterated)

	if len(ids) == 0 {
		return nil, nil
	}

	// Fetch all issues in a single batch query
	return s.GetIssuesByIDs(ctx, ids)
}

// GetIssuesByIDs retrieves multiple issues by ID in a single query to avoid N+1 performance issues
func (s *DoltStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Partition IDs between wisps and issues tables
	ephIDs, doltIDs := partitionIDs(ids)
	if len(ephIDs) > 0 {
		var allIssues []*types.Issue
		wispIssues, err := s.getWispsByIDs(ctx, ephIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to get wisp issues: %w", err)
		}
		allIssues = append(allIssues, wispIssues...)
		if len(doltIDs) > 0 {
			doltIssues, err := s.getIssuesByIDsDolt(ctx, doltIDs)
			if err != nil {
				return nil, err
			}
			allIssues = append(allIssues, doltIssues...)
		}
		return allIssues, nil
	}

	return s.getIssuesByIDsDolt(ctx, ids)
}

func (s *DoltStore) getIssuesByIDsDolt(ctx context.Context, ids []string) ([]*types.Issue, error) {
	// Build IN clause
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	// nolint:gosec // G201: placeholders contains only ? markers, actual values passed via args
	query := fmt.Sprintf(`
		SELECT `+issueSelectColumns+`
		FROM issues
		WHERE id IN (%s)
	`, strings.Join(placeholders, ","))

	queryRows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get issues by IDs: %w", err)
	}
	defer queryRows.Close()

	var issues []*types.Issue
	for queryRows.Next() {
		issue, err := scanIssueFrom(queryRows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}

	return issues, queryRows.Err()
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
	if metadata.Valid {
		dep.Metadata = metadata.String
	}
	if threadID.Valid {
		dep.ThreadID = threadID.String
	}

	return &dep, nil
}
