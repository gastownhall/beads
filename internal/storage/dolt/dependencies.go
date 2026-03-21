package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// isCrossPrefixDep returns true if the two bead IDs have different prefixes,
// meaning the target lives in a different rig's database.
func isCrossPrefixDep(sourceID, targetID string) bool {
	return types.ExtractPrefix(sourceID) != types.ExtractPrefix(targetID)
}

// AddDependency adds a dependency between two issues.
// Delegates SQL work to issueops.AddDependencyInTx; handles Dolt versioning
// and cache invalidation.
func (s *DoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	// Route to wisp_dependencies if the source is an active wisp.
	if s.isActiveWisp(ctx, dep.IssueID) {
		return s.addWispDependency(ctx, dep, actor)
	}

	// Pre-transaction: check if target is a wisp (must be done before opening tx
	// to avoid connection pool deadlock with embedded dolt — bd-w2w).
	isCrossPrefix := isCrossPrefixDep(dep.IssueID, dep.DependsOnID)
	targetTable := "issues"
	if !strings.HasPrefix(dep.DependsOnID, "external:") && !isCrossPrefix {
		if s.isActiveWisp(ctx, dep.DependsOnID) {
			targetTable = "wisps"
		}
	}

	if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		opts := issueops.AddDependencyOpts{
			SourceTable:   "issues",
			TargetTable:   targetTable,
			WriteTable:    "dependencies",
			IsCrossPrefix: isCrossPrefix,
		}
		if err := issueops.AddDependencyInTx(ctx, tx, dep, actor, opts); err != nil {
			return err
		}
		s.invalidateBlockedIDsCache()
		return nil
	}); err != nil {
		return err
	}
	// GH#2455: Use explicit DOLT_ADD to avoid sweeping up stale config changes.
	return s.doltAddAndCommit(ctx, []string{"dependencies"}, "dependency: add "+string(dep.Type)+" "+dep.IssueID+" -> "+dep.DependsOnID)
}

// RemoveDependency removes a dependency between two issues.
// Delegates SQL work to issueops.RemoveDependencyInTx which handles wisp routing.
func (s *DoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	// Wisps live in dolt_ignored tables — skip Dolt versioning entirely.
	if s.isActiveWisp(ctx, issueID) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := issueops.RemoveDependencyInTx(ctx, tx, issueID, dependsOnID); err != nil {
			return err
		}
		s.invalidateBlockedIDsCache()
		return wrapTransactionError("commit remove wisp dependency", tx.Commit())
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := issueops.RemoveDependencyInTx(ctx, tx, issueID, dependsOnID); err != nil {
		return err
	}

	s.invalidateBlockedIDsCache()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql commit: %w", err)
	}
	// GH#2455: Use explicit DOLT_ADD to avoid sweeping up stale config changes.
	if err := s.doltAddAndCommit(ctx, []string{"dependencies"}, "dependency: remove "+issueID+" -> "+dependsOnID); err != nil {
		return err
	}
	return nil
}

// GetDependencies retrieves issues that this issue depends on
func (s *DoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependenciesInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependents retrieves issues that depend on this issue
func (s *DoltStore) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentsInTx(ctx, tx, issueID)
		return err
	})
	return result, err
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

// GetDependentsWithMetadata returns dependents with metadata.
// Delegates to issueops.GetDependentsWithMetadataInTx which handles wisp routing.
func (s *DoltStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var result []*types.IssueWithDependencyMetadata
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentsWithMetadataInTx(ctx, tx, issueID)
		return err
	})
	return result, err
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

// GetAllDependencyRecords returns all dependency records.
// Delegates to issueops.GetAllDependencyRecordsInTx for shared query logic.
func (s *DoltStore) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	var result map[string][]*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetAllDependencyRecordsInTx(ctx, tx)
		return err
	})
	return result, err
}

// GetDependencyRecordsForIssues returns dependency records for specific issues.
// Delegates to issueops.GetDependencyRecordsForIssuesInTx for shared query logic.
func (s *DoltStore) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	var result map[string][]*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyRecordsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

// GetBlockingInfoForIssues returns blocking dependency records relevant to a set of issue IDs.
// Delegates to issueops.GetBlockingInfoForIssuesInTx for shared query logic.
func (s *DoltStore) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (
	blockedByMap map[string][]string,
	blocksMap map[string][]string,
	parentMap map[string]string,
	err error,
) {
	err = s.withReadTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		blockedByMap, blocksMap, parentMap, txErr = issueops.GetBlockingInfoForIssuesInTx(ctx, tx, issueIDs)
		return txErr
	})
	return
}

// GetDependencyCounts returns dependency counts for multiple issues.
// Delegates to issueops.GetDependencyCountsInTx for shared query logic.
func (s *DoltStore) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	var result map[string]*types.DependencyCounts
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyCountsInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
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
	var result [][]*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.DetectCyclesInTx(ctx, tx)
		return err
	})
	return result, err
}

// IsBlocked checks if an issue has open blockers.
// Uses computeBlockedIDs for authoritative blocked status, consistent with
// GetReadyWork. This covers all blocking dependency types (blocks, waits-for)
// with full gate evaluation semantics. (GH#1524)
func (s *DoltStore) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	// Use computeBlockedIDs as the single source of truth for blocked status.
	// This ensures the close guard is consistent with ready work calculation.
	_, err := s.computeBlockedIDs(ctx, true)
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
	// Query all blocking dependency types to stay consistent with
	// computeBlockedIDs which considers blocks, waits-for, and
	// conditional-blocks (GH-1524).
	rows, err := s.queryContext(ctx, `
		SELECT d.depends_on_id, d.type
		FROM dependencies d
		JOIN issues i ON d.depends_on_id = i.id
		WHERE d.issue_id = ?
		  AND d.type IN ('blocks', 'waits-for', 'conditional-blocks')
		  AND i.status NOT IN ('closed', 'pinned')
	`, issueID)
	if err != nil {
		return false, nil, fmt.Errorf("failed to check blockers: %w", err)
	}

	var blockers []string
	for rows.Next() {
		var id, depType string
		if err := rows.Scan(&id, &depType); err != nil {
			_ = rows.Close()
			return false, nil, wrapScanError("is blocked: scan blocker", err)
		}
		if depType != "blocks" {
			blockers = append(blockers, id+" ("+depType+")")
		} else {
			blockers = append(blockers, id)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, nil, wrapQueryError("is blocked: blocker rows", err)
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
	// Uses batched IN clauses (queryBatchSize) to avoid full table scans on Dolt.
	stillBlocked := make(map[string]bool)
	for start := 0; start < len(candidateIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(candidateIDs) {
			end = len(candidateIDs)
		}
		batch := candidateIDs[start:end]
		placeholders, args := doltBuildSQLInClause(batch)
		// Append the closedIssueID to exclude it from "other blockers"
		args = append(args, closedIssueID)

		// nolint:gosec // G201: placeholders contains only ? markers, actual values passed via args
		stillBlockedQuery := fmt.Sprintf(`
			SELECT DISTINCT d2.issue_id
			FROM dependencies d2
			JOIN issues blocker ON d2.depends_on_id = blocker.id
			WHERE d2.issue_id IN (%s)
			  AND d2.type = 'blocks'
			  AND d2.depends_on_id != ?
			  AND blocker.status NOT IN ('closed', 'pinned')
		`, placeholders)

		blockedRows, err := s.queryContext(ctx, stillBlockedQuery, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to check remaining blockers: %w", err)
		}

		for blockedRows.Next() {
			var id string
			if err := blockedRows.Scan(&id); err != nil {
				_ = blockedRows.Close()
				return nil, fmt.Errorf("failed to scan still-blocked: %w", err)
			}
			stillBlocked[id] = true
		}
		_ = blockedRows.Close()
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

// GetIssuesByIDs retrieves multiple issues by ID.
// Delegates to issueops.GetIssuesByIDsInTx which handles wisp routing and label hydration.
func (s *DoltStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetIssuesByIDsInTx(ctx, tx, ids)
		return err
	})
	return result, err
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
