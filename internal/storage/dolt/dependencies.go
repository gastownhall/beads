package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// isCrossPrefixDep returns true if the two bead IDs have different prefixes,
// meaning the target lives in a different rig's database.
func isCrossPrefixDep(sourceID, targetID string) bool {
	return types.ExtractPrefix(sourceID) != types.ExtractPrefix(targetID)
}

// AddDependency adds a dependency between two issues without recording a
// dependency_added event. Create-with-deps and structural callers use this
// no-event default; the explicit dep verbs call AddDependencyWithOptions with
// EmitEvent set.
func (s *DoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return s.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

// AddDependencyWithOptions adds a dependency between two issues.
// Delegates SQL work to issueops.AddDependencyInTx; handles Dolt versioning
// and cache invalidation. EmitEvent records a dependency_added history event.
func (s *DoltStore) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, addOpts storage.DependencyAddOptions) error {
	isCrossPrefix := isCrossPrefixDep(dep.IssueID, dep.DependsOnID)

	// Route to wisp_dependencies if the source is an active wisp.
	if s.isActiveWisp(ctx, dep.IssueID) {
		return s.addWispDependency(ctx, dep, actor, isCrossPrefix, addOpts.EmitEvent)
	}

	targetTable := "issues"
	kind := issueops.DepTargetIssue
	switch {
	case isCrossPrefix, strings.HasPrefix(dep.DependsOnID, "external:"):
		kind = issueops.DepTargetExternal
	default:
		if s.isActiveWisp(ctx, dep.DependsOnID) {
			targetTable = "wisps"
			kind = issueops.DepTargetWisp
		}
	}

	var eventWritten bool
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		opts := issueops.AddDependencyOpts{
			SourceTable:   "issues",
			TargetTable:   targetTable,
			WriteTable:    "dependencies",
			IsCrossPrefix: isCrossPrefix,
			TargetKind:    &kind,
			EmitEvent:     addOpts.EmitEvent,
		}
		var e error
		eventWritten, e = issueops.AddDependencyInTx(ctx, tx, dep, actor, opts)
		return e
	}); err != nil {
		return err
	}
	// GH#2455: Use explicit DOLT_ADD to avoid sweeping up stale config changes.
	// Stage events only when AddDependencyInTx actually recorded a
	// dependency_added event (explicit verb + genuine new edge). A structural or
	// idempotent add writes no event, so staging events would sweep unrelated
	// pending event rows into this dependency commit.
	tables := []string{"dependencies"}
	if eventWritten {
		tables = append(tables, "events")
	}
	return s.doltAddAndCommit(ctx, tables, "dependency: add "+string(dep.Type)+" "+dep.IssueID+" -> "+dep.DependsOnID)
}

// RemoveDependency removes a dependency between two issues without recording a
// dependency_removed event — the no-event default for structural callers (issue
// delete, reparent, batch, duplicate cleanup). The explicit bd dep remove verb
// calls RemoveDependencyWithOptions with EmitEvent set.
func (s *DoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return s.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, storage.DependencyRemoveOptions{})
}

// RemoveDependencyWithOptions removes a dependency between two issues.
// Delegates SQL work to issueops.RemoveDependencyInTx which handles wisp routing.
// EmitEvent records a dependency_removed history event for the explicit dep verb.
func (s *DoltStore) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, rmOpts storage.DependencyRemoveOptions) error {
	// Wisps live in dolt_ignored tables — skip Dolt versioning entirely.
	if s.isActiveWisp(ctx, issueID) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := issueops.RemoveDependencyInTx(ctx, tx, issueID, dependsOnID, actor, rmOpts.EmitEvent); err != nil {
			return err
		}
		return wrapTransactionError("commit remove wisp dependency", tx.Commit())
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	eventWritten, err := issueops.RemoveDependencyInTx(ctx, tx, issueID, dependsOnID, actor, rmOpts.EmitEvent)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql commit: %w", err)
	}
	// GH#2455: Use explicit DOLT_ADD to avoid sweeping up stale config changes.
	// Stage events only when RemoveDependencyInTx actually recorded a
	// dependency_removed event (explicit verb + genuine edge removal). A
	// structural or missing-edge remove writes no event, so staging events would
	// sweep unrelated pending event rows into this dependency commit.
	tables := []string{"dependencies"}
	if eventWritten {
		tables = append(tables, "events")
	}
	if err := s.doltAddAndCommit(ctx, tables, "dependency: remove "+issueID+" -> "+dependsOnID); err != nil {
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

	rows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT %s AS depends_on_id, d.type, d.created_at, d.created_by, d.metadata, d.thread_id
		FROM dependencies d
		WHERE d.issue_id = ?
	`, issueops.DepTargetExpr), issueID)
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

	rows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		WHERE issue_id = ?
	`, issueops.DepTargetExpr), issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency records: %w", err)
	}
	defer rows.Close()

	return scanDependencyRows(rows)
}

// GetDependentRecords returns raw dependency rows whose target is issueID,
// without hydrating the source issues. Delegates to
// issueops.GetDependentRecordsInTx for shared query logic.
func (s *DoltStore) GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	var result []*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentRecordsInTx(ctx, tx, targetID, depType, limit, afterID)
		return err
	})
	return result, err
}

// CountDependentRecords returns the total inbound-edge count of targetID across
// both dependency tables. Delegates to issueops.CountDependentRecordsInTx.
func (s *DoltStore) CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error) {
	var n int
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountDependentRecordsInTx(ctx, tx, targetID, depType)
		return err
	})
	return n, err
}

// GetDependentRecordsForIssues returns the raw inbound dependency rows for a SET
// of target ids in one batched read, keyed by target id. Delegates to
// issueops.GetDependentRecordsForIssuesInTx for shared query logic.
func (s *DoltStore) GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	var result map[string][]*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentRecordsForIssuesInTx(ctx, tx, targetIDs)
		return err
	})
	return result, err
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
	var result []*types.TreeNode
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyTreeInTx(ctx, tx, issueID, maxDepth, showAllPaths, reverse)
		return err
	})
	return result, err
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

func (s *DoltStore) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	var blocked bool
	var blockers []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		blocked, blockers, err = issueops.IsBlockedInTx(ctx, tx, issueID)
		return err
	})
	if err != nil {
		return false, nil, fmt.Errorf("failed to check blockers: %w", err)
	}
	return blocked, blockers, nil
}

// IsBlockedBatch returns the denormalized transitive is_blocked flag for each id
// in one batched read. Delegates to issueops.IsBlockedBatchInTx.
func (s *DoltStore) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	var result map[string]bool
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.IsBlockedBatchInTx(ctx, tx, ids)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to batch-check blockers: %w", err)
	}
	return result, nil
}

// GetNewlyUnblockedByClose finds issues that become unblocked when an issue is closed.
func (s *DoltStore) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetNewlyUnblockedByCloseInTx(ctx, tx, closedIssueID)
		return err
	})
	return result, err
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
		result, err = issueops.GetIssuesByIDsInTx(ctx, tx, ids, nil)
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
