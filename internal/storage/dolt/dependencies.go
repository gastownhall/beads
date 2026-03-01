package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// AddDependency adds a dependency between two issues.
// Uses an explicit transaction so writes persist when @@autocommit is OFF
// (e.g. Dolt server started with --no-auto-commit).
func (s *DoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	// Route to wisp_dependencies if the issue is an active wisp
	if s.isActiveWisp(ctx, dep.IssueID) {
		return s.addWispDependency(ctx, dep, actor)
	}

	// Pre-transaction: check if target is a wisp (must be done before opening tx
	// to avoid connection pool deadlock with embedded dolt — bd-w2w)
	targetIsWisp := false
	if !strings.HasPrefix(dep.DependsOnID, "external:") {
		targetIsWisp = s.isActiveWisp(ctx, dep.DependsOnID)
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
	// Check wisps table if target is an active wisp (bd-w2w)
	if !strings.HasPrefix(dep.DependsOnID, "external:") {
		var targetExists int
		targetTable := "issues"
		if targetIsWisp {
			targetTable = "wisps"
		}
		//nolint:gosec // G201: targetTable is hardcoded to "issues" or "wisps"
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, targetTable), dep.DependsOnID).Scan(&targetExists); err != nil {
			return fmt.Errorf("failed to check target issue existence: %w", err)
		}
		if targetExists == 0 {
			return fmt.Errorf("issue %s not found", dep.DependsOnID)
		}
	}

	// Cycle detection for blocking dependency types: check if adding this edge
	// would create a cycle by seeing if depends_on_id can already reach issue_id.
	// UNIONs both dependencies and wisp_dependencies to detect cross-table cycles
	// (e.g., permanent A -> wisp B -> permanent A). (bd-xe27)
	if dep.Type == types.DepBlocks {
		var reachable int
		if err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE reachable AS (
				SELECT ? AS node, 0 AS depth
				UNION ALL
				SELECT d.depends_on_id, r.depth + 1
				FROM reachable r
				JOIN (
					SELECT issue_id, depends_on_id FROM dependencies WHERE type = 'blocks'
					UNION ALL
					SELECT issue_id, depends_on_id FROM wisp_dependencies WHERE type = 'blocks'
				) d ON d.issue_id = r.node
				WHERE r.depth < 100
			)
			SELECT COUNT(*) FROM reachable WHERE node = ?
		`, dep.DependsOnID, dep.IssueID).Scan(&reachable); err != nil {
			return fmt.Errorf("failed to check for dependency cycle: %w", err)
		}
		if reachable > 0 {
			return fmt.Errorf("adding dependency would create a cycle")
		}
	}

	// Check for existing dependency between the same pair with a different type.
	// Previously this was an upsert (ON DUPLICATE KEY UPDATE type = VALUES(type))
	// which silently changed e.g. "blocks" to "caused-by", removing the blocking
	// relationship without warning.
	var existingType string
	err = tx.QueryRowContext(ctx, `
		SELECT type FROM dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		// Row exists
		if existingType == string(dep.Type) {
			// Same type — idempotent; update metadata in case it changed
			if _, err := tx.ExecContext(ctx, `
				UPDATE dependencies SET metadata = ? WHERE issue_id = ? AND depends_on_id = ?
			`, metadata, dep.IssueID, dep.DependsOnID); err != nil {
				return fmt.Errorf("failed to update dependency metadata: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("sql commit: %w", err)
			}
			// Record in Dolt version history (bd-2avi)
			if _, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
				"dependency: update metadata "+dep.IssueID+" -> "+dep.DependsOnID, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
				return fmt.Errorf("dolt commit: %w", err)
			}
			return nil
		}
		return fmt.Errorf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing dependency: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}

	s.invalidateBlockedIDsCache()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql commit: %w", err)
	}
	// Record in Dolt version history (bd-2avi)
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
		"dependency: add "+string(dep.Type)+" "+dep.IssueID+" -> "+dep.DependsOnID, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("dolt commit: %w", err)
	}
	return nil
}

// RemoveDependency removes a dependency between two issues.
// Uses an explicit transaction so writes persist when @@autocommit is OFF.
func (s *DoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	// Route to wisp_dependencies if the issue is an active wisp
	if s.isActiveWisp(ctx, issueID) {
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

	s.invalidateBlockedIDsCache()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql commit: %w", err)
	}
	// Record in Dolt version history (bd-2avi)
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
		"dependency: remove "+issueID+" -> "+dependsOnID, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("dolt commit: %w", err)
	}
	return nil
}

// GetDependencies retrieves issues that this issue depends on
func (s *DoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	if s.isActiveWisp(ctx, issueID) {
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
	if s.isActiveWisp(ctx, issueID) {
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
	if s.isActiveWisp(ctx, issueID) {
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
		return nil, wrapQueryError("get dependencies with metadata: rows", err)
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
		return nil, fmt.Errorf("get dependencies with metadata: fetch issues: %w", err)
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
	if s.isActiveWisp(ctx, issueID) {
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
		return nil, wrapQueryError("get dependents with metadata: rows", err)
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
		return nil, fmt.Errorf("get dependents with metadata: fetch issues: %w", err)
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
	if s.isActiveWisp(ctx, issueID) {
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
			return nil, fmt.Errorf("get all dependency records: %w", err)
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
	ephIDs, doltIDs := s.partitionByWispStatus(ctx, issueIDs)
	if len(ephIDs) > 0 {
		result := make(map[string][]*types.Dependency)
		for _, id := range ephIDs {
			deps, err := s.getWispDependencyRecords(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("get dependency records: wisp %s: %w", id, err)
			}
			if len(deps) > 0 {
				result[id] = deps
			}
		}
		if len(doltIDs) > 0 {
			doltResult, err := s.getDependencyRecordsForIssuesDolt(ctx, doltIDs)
			if err != nil {
				return nil, fmt.Errorf("get dependency records: dolt: %w", err)
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
			return nil, fmt.Errorf("get dependency records for issues: %w", err)
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
// Parent-child dependencies are separated into parentMap (childID -> parentID) so callers
// can display them distinctly from blocking deps. (bd-hcxu)
//
// This replaces the expensive pattern of GetAllDependencyRecords + getClosedBlockerIDs
// which loaded the entire dependency table and did N+1 issue lookups. (bd-7di)
func (s *DoltStore) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (
	blockedByMap map[string][]string, // issueID -> list of IDs blocking it
	blocksMap map[string][]string, // issueID -> list of IDs it blocks
	parentMap map[string]string, // childID -> parentID (parent-child deps)
	err error,
) {
	blockedByMap = make(map[string][]string)
	blocksMap = make(map[string][]string)
	parentMap = make(map[string]string)

	if len(issueIDs) == 0 {
		return blockedByMap, blocksMap, parentMap, nil
	}

	// Partition and merge wisp and dolt IDs
	ephIDs, doltIDs := s.partitionByWispStatus(ctx, issueIDs)
	if len(ephIDs) > 0 {
		// For wisp IDs, query wisp_dependencies
		for _, ephID := range ephIDs {
			deps, depErr := s.getWispDependencyRecords(ctx, ephID)
			if depErr != nil {
				return nil, nil, nil, depErr
			}
			for _, dep := range deps {
				if dep.Type == types.DepParentChild {
					parentMap[ephID] = dep.DependsOnID
				} else if dep.Type == types.DepBlocks {
					blockedByMap[ephID] = append(blockedByMap[ephID], dep.DependsOnID)
				}
			}
		}
		if len(doltIDs) == 0 {
			return blockedByMap, blocksMap, parentMap, nil
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
		return nil, nil, nil, fmt.Errorf("failed to get blocked-by info: %w", qErr)
	}
	for rows.Next() {
		var issueID, blockerID, depType, blockerStatus string
		if scanErr := rows.Scan(&issueID, &blockerID, &depType, &blockerStatus); scanErr != nil {
			_ = rows.Close()
			return nil, nil, nil, wrapScanError("get blocking info: scan blocked-by", scanErr)
		}
		// Skip closed blockers — the dependency record is preserved, but a
		// closed blocker no longer blocks work.
		if types.Status(blockerStatus) == types.StatusClosed {
			continue
		}
		// Separate parent-child from blocking deps (bd-hcxu)
		if depType == "parent-child" {
			parentMap[issueID] = blockerID
		} else {
			blockedByMap[issueID] = append(blockedByMap[issueID], blockerID)
		}
	}
	_ = rows.Close()
	if rowErr := rows.Err(); rowErr != nil {
		return nil, nil, nil, wrapQueryError("get blocking info: blocked-by rows", rowErr)
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
		return nil, nil, nil, fmt.Errorf("failed to get blocks info: %w", qErr2)
	}
	for rows2.Next() {
		var blockerID, blockedID, depType, blockerStatus string
		if scanErr := rows2.Scan(&blockerID, &blockedID, &depType, &blockerStatus); scanErr != nil {
			_ = rows2.Close()
			return nil, nil, nil, wrapScanError("get blocking info: scan blocks", scanErr)
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
		return nil, nil, nil, wrapQueryError("get blocking info: blocks rows", rowErr2)
	}

	return blockedByMap, blocksMap, parentMap, nil
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
	return s.buildDependencyTree(ctx, issueID, 0, maxDepth, reverse, visited, "")
}

func (s *DoltStore) buildDependencyTree(ctx context.Context, issueID string, depth, maxDepth int, reverse bool, visited map[string]bool, parentID string) ([]*types.TreeNode, error) {
	if depth >= maxDepth || visited[issueID] {
		return nil, nil
	}
	visited[issueID] = true

	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}

	// Use GetDependencies/GetDependents which handle wisp routing,
	// instead of querying the dependencies table directly (GH#2145).
	var related []*types.Issue
	if reverse {
		related, err = s.GetDependents(ctx, issueID)
	} else {
		related, err = s.GetDependencies(ctx, issueID)
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
		children, err := s.buildDependencyTree(ctx, rel.ID, depth+1, maxDepth, reverse, visited, issueID)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}

	return nodes, nil
}

// DetectCycles finds circular dependencies.
// Queries both dependencies and wisp_dependencies tables to detect cross-table
// cycles (e.g., permanent A -> wisp B -> permanent A). (bd-xe27)
func (s *DoltStore) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	// Get all permanent dependencies
	deps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}

	// Get all wisp dependencies
	wispDeps, err := s.getAllWispDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}

	// Build adjacency list from both tables
	graph := make(map[string][]string)
	for issueID, records := range deps {
		for _, dep := range records {
			if dep.Type == types.DepBlocks {
				graph[issueID] = append(graph[issueID], dep.DependsOnID)
			}
		}
	}
	for issueID, records := range wispDeps {
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

// IsBlocked checks if an issue has open blockers.
// Uses computeBlockedIDs for authoritative blocked status, consistent with
// GetReadyWork. This covers all blocking dependency types (blocks, waits-for)
// with full gate evaluation semantics. (GH#1524)
func (s *DoltStore) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	// Use computeBlockedIDs as the single source of truth for blocked status.
	// This ensures the close guard is consistent with ready work calculation.
	_, err := s.computeBlockedIDs(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("failed to compute blocked IDs: %w", err)
	}

	s.cacheMu.Lock()
	isBlocked := s.blockedIDsCacheMap[issueID]
	s.cacheMu.Unlock()

	if !isBlocked {
		return false, nil, nil
	}

	// Issue is blocked — gather blocker IDs for display.
	// Check direct 'blocks' dependencies first.
	rows, err := s.queryContext(ctx, `
		SELECT d.depends_on_id
		FROM dependencies d
		JOIN issues i ON d.depends_on_id = i.id
		WHERE d.issue_id = ?
		  AND d.type = 'blocks'
		  AND i.status NOT IN ('closed', 'pinned')
	`, issueID)
	if err != nil {
		return false, nil, fmt.Errorf("failed to check blockers: %w", err)
	}

	var blockers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return false, nil, wrapScanError("is blocked: scan blocker", err)
		}
		blockers = append(blockers, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, nil, wrapQueryError("is blocked: blocker rows", err)
	}

	// If blocked by non-'blocks' dependency (e.g., waits-for gate),
	// include the waits-for spawner IDs so callers get a non-empty list.
	if len(blockers) == 0 {
		wfRows, err := s.queryContext(ctx, `
			SELECT depends_on_id FROM dependencies
			WHERE issue_id = ? AND type = 'waits-for'
		`, issueID)
		if err == nil {
			for wfRows.Next() {
				var id string
				if err := wfRows.Scan(&id); err != nil {
					break
				}
				blockers = append(blockers, id+" (waits-for)")
			}
			_ = wfRows.Close()
		}
	}

	return true, blockers, nil
}

// GetNewlyUnblockedByClose finds issues that become unblocked when an issue is closed.
//
// Rewritten from a single query with nested JOIN + correlated NOT EXISTS to two
// sequential queries to avoid Dolt query-planner issues with nested JOIN subqueries.
// See bd-o23 / hq-g4nxe for the SQL audit that identified this pattern.
func (s *DoltStore) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	// Step 1: Find open/blocked issues that depend on the closed issue.
	candidateRows, err := s.queryContext(ctx, `
		SELECT d.issue_id
		FROM dependencies d
		JOIN issues i ON d.issue_id = i.id
		WHERE d.depends_on_id = ?
		  AND d.type = 'blocks'
		  AND i.status NOT IN ('closed', 'pinned')
	`, closedIssueID)
	if err != nil {
		return nil, fmt.Errorf("failed to find blocked candidates: %w", err)
	}

	var candidateIDs []string
	for candidateRows.Next() {
		var id string
		if err := candidateRows.Scan(&id); err != nil {
			_ = candidateRows.Close()
			return nil, fmt.Errorf("failed to scan candidate: %w", err)
		}
		candidateIDs = append(candidateIDs, id)
	}
	_ = candidateRows.Close()
	if err := candidateRows.Err(); err != nil {
		return nil, wrapQueryError("get newly unblocked: candidate rows", err)
	}

	if len(candidateIDs) == 0 {
		return nil, nil
	}

	// Step 2: Among candidates, find those that still have OTHER open blockers.
	placeholders := make([]string, len(candidateIDs))
	args := make([]interface{}, len(candidateIDs))
	for i, id := range candidateIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")
	// Append the closedIssueID to exclude it from "other blockers"
	args = append(args, closedIssueID)

	// nolint:gosec // G201: inClause contains only ? placeholders, actual values passed via args
	stillBlockedQuery := fmt.Sprintf(`
		SELECT DISTINCT d2.issue_id
		FROM dependencies d2
		JOIN issues blocker ON d2.depends_on_id = blocker.id
		WHERE d2.issue_id IN (%s)
		  AND d2.type = 'blocks'
		  AND d2.depends_on_id != ?
		  AND blocker.status NOT IN ('closed', 'pinned')
	`, inClause)

	blockedRows, err := s.queryContext(ctx, stillBlockedQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to check remaining blockers: %w", err)
	}

	stillBlocked := make(map[string]bool)
	for blockedRows.Next() {
		var id string
		if err := blockedRows.Scan(&id); err != nil {
			_ = blockedRows.Close()
			return nil, fmt.Errorf("failed to scan still-blocked: %w", err)
		}
		stillBlocked[id] = true
	}
	_ = blockedRows.Close()
	if err := blockedRows.Err(); err != nil {
		return nil, wrapQueryError("get newly unblocked: blocked rows", err)
	}

	// Filter to only candidates with no remaining open blockers
	var unblockedIDs []string
	for _, id := range candidateIDs {
		if !stillBlocked[id] {
			unblockedIDs = append(unblockedIDs, id)
		}
	}

	if len(unblockedIDs) == 0 {
		return nil, nil
	}

	return s.GetIssuesByIDs(ctx, unblockedIDs)
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
		return nil, wrapQueryError("scan issue IDs: rows", err)
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
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("scan issue IDs: batch fetch: %w", err)
	}

	// Restore the caller's ORDER BY: GetIssuesByIDs uses WHERE id IN (...)
	// which returns rows in arbitrary order, losing the sort from the original
	// query (e.g., ORDER BY priority ASC, created_at DESC). Build an index
	// and reorder to match the original id slice. (GH#1880)
	issueByID := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		issueByID[issue.ID] = issue
	}
	ordered := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := issueByID[id]; ok {
			ordered = append(ordered, issue)
		}
	}
	return ordered, nil
}

// GetIssuesByIDs retrieves multiple issues by ID in a single query to avoid N+1 performance issues
func (s *DoltStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Partition IDs between wisps and issues tables
	ephIDs, doltIDs := s.partitionByWispStatus(ctx, ids)
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
				return nil, fmt.Errorf("get issues by IDs: dolt: %w", err)
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
			return nil, wrapScanError("get issues by IDs: scan issue", err)
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
			return nil, fmt.Errorf("scan dependency rows: %w", err)
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
