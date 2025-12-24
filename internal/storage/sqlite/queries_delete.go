package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// CreateTombstone converts an existing issue to a tombstone record.
// This is a soft-delete that preserves the issue in the database with status="tombstone".
// The issue will still appear in exports but be excluded from normal queries.
// Dependencies must be removed separately before calling this method.
func (s *SQLiteStorage) CreateTombstone(ctx context.Context, id string, actor string, reason string) error {
	// Get the issue to preserve its original type
	issue, err := s.GetIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue not found: %s", id)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	originalType := string(issue.IssueType)

	// Convert issue to tombstone
	// Note: closed_at must be set to NULL because of CHECK constraint:
	// (status = 'closed') = (closed_at IS NOT NULL)
	_, err = tx.ExecContext(ctx, `
		UPDATE issues
		SET status = ?,
		    closed_at = NULL,
		    deleted_at = ?,
		    deleted_by = ?,
		    delete_reason = ?,
		    original_type = ?,
		    updated_at = ?
		WHERE id = ?
	`, types.StatusTombstone, now, actor, reason, originalType, now, id)
	if err != nil {
		return fmt.Errorf("failed to create tombstone: %w", err)
	}

	// Record tombstone creation event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, id, "deleted", actor, reason)
	if err != nil {
		return fmt.Errorf("failed to record tombstone event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, now)
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Invalidate blocked issues cache since status changed (bd-5qim)
	// Tombstone issues don't block others, so this affects blocking calculations
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return wrapDBError("commit tombstone transaction", err)
	}

	return nil
}

// DeleteIssue permanently removes an issue from the database
func (s *SQLiteStorage) DeleteIssue(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete dependencies (both directions)
	_, err = tx.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// Delete events
	_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete events: %w", err)
	}

	// Delete comments (no FK cascade on this table) (bd-687g)
	_, err = tx.ExecContext(ctx, `DELETE FROM comments WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete comments: %w", err)
	}

	// Delete from dirty_issues
	_, err = tx.ExecContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete dirty marker: %w", err)
	}

	// Delete the issue itself
	result, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	if err := tx.Commit(); err != nil {
		return wrapDBError("commit delete transaction", err)
	}

	// REMOVED (bd-c7af): Counter sync after deletion - no longer needed with hash IDs
	return nil
}

// DeleteIssuesResult contains statistics about a batch deletion operation
type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	OrphanedIssues    []string
}

// DeleteIssues deletes multiple issues in a single transaction
// If cascade is true, recursively deletes dependents
// If cascade is false but force is true, deletes issues and orphans their dependents
// If cascade and force are both false, returns an error if any issue has dependents
// If dryRun is true, only computes statistics without deleting
func (s *SQLiteStorage) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &DeleteIssuesResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	idSet := buildIDSet(ids)
	result := &DeleteIssuesResult{}

	expandedIDs, err := s.resolveDeleteSet(ctx, tx, ids, idSet, cascade, force, result)
	if err != nil {
		return nil, wrapDBError("resolve delete set", err)
	}

	inClause, args := buildSQLInClause(expandedIDs)
	if err := s.populateDeleteStats(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if dryRun {
		return result, nil
	}

	if err := s.executeDelete(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// REMOVED (bd-c7af): Counter sync after deletion - no longer needed with hash IDs

	return result, nil
}

func buildIDSet(ids []string) map[string]bool {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	return idSet
}

func (s *SQLiteStorage) resolveDeleteSet(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, cascade bool, force bool, result *DeleteIssuesResult) ([]string, error) {
	if cascade {
		return s.expandWithDependents(ctx, tx, ids, idSet)
	}
	if !force {
		return ids, s.validateNoDependents(ctx, tx, ids, idSet, result)
	}
	return ids, s.trackOrphanedIssues(ctx, tx, ids, idSet, result)
}

func (s *SQLiteStorage) expandWithDependents(ctx context.Context, tx *sql.Tx, ids []string, _ map[string]bool) ([]string, error) {
	allToDelete, err := s.findAllDependentsRecursive(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to find dependents: %w", err)
	}
	expandedIDs := make([]string, 0, len(allToDelete))
	for id := range allToDelete {
		expandedIDs = append(expandedIDs, id)
	}
	return expandedIDs, nil
}

func (s *SQLiteStorage) validateNoDependents(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	for _, id := range ids {
		if err := s.checkSingleIssueValidation(ctx, tx, id, idSet, result); err != nil {
			return wrapDBError("check dependents", err)
		}
	}
	return nil
}

func (s *SQLiteStorage) checkSingleIssueValidation(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, result *DeleteIssuesResult) error {
	var depCount int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE depends_on_id = ?`, id).Scan(&depCount)
	if err != nil {
		return fmt.Errorf("failed to check dependents for %s: %w", id, err)
	}
	if depCount == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	hasExternal := false
	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			hasExternal = true
			result.OrphanedIssues = append(result.OrphanedIssues, depID)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dependents for %s: %w", id, err)
	}

	if hasExternal {
		return fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
	}
	return nil
}

func (s *SQLiteStorage) trackOrphanedIssues(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	orphanSet := make(map[string]bool)
	for _, id := range ids {
		if err := s.collectOrphansForID(ctx, tx, id, idSet, orphanSet); err != nil {
			return wrapDBError("collect orphans", err)
		}
	}
	for orphanID := range orphanSet {
		result.OrphanedIssues = append(result.OrphanedIssues, orphanID)
	}
	return nil
}

func (s *SQLiteStorage) collectOrphansForID(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, orphanSet map[string]bool) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			orphanSet[depID] = true
		}
	}
	return rows.Err()
}

func buildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *SQLiteStorage) populateDeleteStats(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	counts := []struct {
		query string
		dest  *int
	}{
		{fmt.Sprintf(`SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause), &result.DependenciesCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)`, inClause), &result.LabelsCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM events WHERE issue_id IN (%s)`, inClause), &result.EventsCount},
	}

	for _, c := range counts {
		queryArgs := args
		if c.dest == &result.DependenciesCount {
			queryArgs = append(args, args...)
		}
		if err := tx.QueryRowContext(ctx, c.query, queryArgs...).Scan(c.dest); err != nil {
			return fmt.Errorf("failed to count: %w", err)
		}
	}

	result.DeletedCount = len(args)
	return nil
}

func (s *SQLiteStorage) executeDelete(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	// Note: This method now creates tombstones instead of hard-deleting (bd-3b4)
	// Only dependencies are deleted - issues are converted to tombstones

	// 1. Delete dependencies - tombstones don't block other issues
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause),
		append(args, args...)...)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// 2. Get issue types before converting to tombstones (need for original_type)
	issueTypes := make(map[string]string)
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, issue_type FROM issues WHERE id IN (%s)`, inClause),
		args...)
	if err != nil {
		return fmt.Errorf("failed to get issue types: %w", err)
	}
	for rows.Next() {
		var id, issueType string
		if err := rows.Scan(&id, &issueType); err != nil {
			_ = rows.Close() // #nosec G104 - error handling not critical in error path
			return fmt.Errorf("failed to scan issue type: %w", err)
		}
		issueTypes[id] = issueType
	}
	_ = rows.Close()

	// 3. Convert issues to tombstones (only for issues that exist)
	// Note: closed_at must be set to NULL because of CHECK constraint:
	// (status = 'closed') = (closed_at IS NOT NULL)
	now := time.Now()
	deletedCount := 0
	for id, originalType := range issueTypes {
		execResult, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET status = ?,
			    closed_at = NULL,
			    deleted_at = ?,
			    deleted_by = ?,
			    delete_reason = ?,
			    original_type = ?,
			    updated_at = ?
			WHERE id = ?
		`, types.StatusTombstone, now, "batch delete", "batch delete", originalType, now, id)
		if err != nil {
			return fmt.Errorf("failed to create tombstone for %s: %w", id, err)
		}

		rowsAffected, _ := execResult.RowsAffected()
		if rowsAffected == 0 {
			continue // Issue doesn't exist, skip
		}
		deletedCount++

		// Record tombstone creation event
		_, err = tx.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?)
		`, id, "deleted", "batch delete", "batch delete")
		if err != nil {
			return fmt.Errorf("failed to record tombstone event for %s: %w", id, err)
		}

		// Mark issue as dirty for incremental export
		_, err = tx.ExecContext(ctx, `
			INSERT INTO dirty_issues (issue_id, marked_at)
			VALUES (?, ?)
			ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
		`, id, now)
		if err != nil {
			return fmt.Errorf("failed to mark issue dirty for %s: %w", id, err)
		}
	}

	// 4. Invalidate blocked issues cache since statuses changed (bd-5qim)
	if err := s.invalidateBlockedCache(ctx, tx); err != nil {
		return fmt.Errorf("failed to invalidate blocked cache: %w", err)
	}

	result.DeletedCount = deletedCount
	return nil
}

// findAllDependentsRecursive finds all issues that depend on the given issues, recursively
func (s *SQLiteStorage) findAllDependentsRecursive(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		current := toProcess[0]
		toProcess = toProcess[1:]

		rows, err := tx.QueryContext(ctx,
			`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, current)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				return nil, err
			}
			if !result[depID] {
				result[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return result, nil
}
