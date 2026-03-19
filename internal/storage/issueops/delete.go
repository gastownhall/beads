package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// deleteBatchSize controls the maximum number of IDs per IN-clause query
// for delete operations. Kept small to avoid large IN-clause queries.
const deleteBatchSize = 50

// maxRecursiveResults is the safety limit for the total number of issues
// discovered during recursive dependent traversal.
const maxRecursiveResults = 10000

// DeleteIssueInTx deletes a single issue and all its related data within a transaction.
// Routes to the correct tables (issues/wisps) via IsActiveWispInTx.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func DeleteIssueInTx(ctx context.Context, tx *sql.Tx, id string) error {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, labelTable, eventTable, depTable := WispTableRouting(isWisp)

	// commentTable follows the same naming convention
	commentTable := "comments"
	if isWisp {
		commentTable = "wisp_comments"
	}

	// Delete related data
	for _, table := range []string{depTable, eventTable, commentTable, labelTable} {
		if table == depTable {
			_, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id)
			if err != nil {
				return fmt.Errorf("delete from %s: %w", table, err)
			}
		} else {
			_, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id)
			if err != nil {
				return fmt.Errorf("delete from %s: %w", table, err)
			}
		}
	}

	result, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", issueTable), id)
	if err != nil {
		return fmt.Errorf("delete issue from %s: %w", issueTable, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	return nil
}

// DeleteIssuesInTx deletes multiple issues in a single transaction.
// If cascade is true, recursively deletes dependents.
// If cascade is false but force is true, deletes issues and orphans dependents.
// If both are false, returns an error if any issue has dependents outside the set.
// If dryRun is true, only computes statistics without deleting.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func DeleteIssuesInTx(ctx context.Context, tx *sql.Tx, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{}, nil
	}

	// Partition into wisps and regular issues.
	var wispIDs, regularIDs []string
	for _, id := range ids {
		if IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			regularIDs = append(regularIDs, id)
		}
	}

	// Delete wisps first.
	wispDeleteCount := 0
	if len(wispIDs) > 0 && !dryRun {
		for _, id := range wispIDs {
			if err := DeleteIssueInTx(ctx, tx, id); err != nil {
				return nil, fmt.Errorf("delete wisp %s: %w", id, err)
			}
			wispDeleteCount++
		}
	} else {
		wispDeleteCount = len(wispIDs)
	}

	ids = regularIDs
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{DeletedCount: wispDeleteCount}, nil
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	result := &types.DeleteIssuesResult{}

	// Resolve the full set of IDs to delete.
	expandedIDs := ids
	if cascade {
		allToDelete, err := findAllDependentsRecursiveInTx(ctx, tx, ids)
		if err != nil {
			return nil, fmt.Errorf("find dependents: %w", err)
		}
		expandedIDs = make([]string, 0, len(allToDelete))
		for id := range allToDelete {
			expandedIDs = append(expandedIDs, id)
		}
	} else if !force {
		// Check for external dependents.
		for i := 0; i < len(ids); i += deleteBatchSize {
			end := i + deleteBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[i:end]
			inClause, args := buildSQLInClause(batch)

			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT depends_on_id, issue_id FROM dependencies WHERE depends_on_id IN (%s)`, inClause),
				args...)
			if err != nil {
				return nil, fmt.Errorf("check dependents: %w", err)
			}

			externalBySource := make(map[string][]string)
			for rows.Next() {
				var depOnID, issueID string
				if err := rows.Scan(&depOnID, &issueID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan dependent: %w", err)
				}
				if !idSet[issueID] {
					externalBySource[depOnID] = append(externalBySource[depOnID], issueID)
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate dependents: %w", err)
			}

			for _, id := range batch {
				if deps, ok := externalBySource[id]; ok {
					result.OrphanedIssues = deps
					return result, fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
				}
			}
		}
	} else {
		// Force mode: track orphaned issues.
		orphans, err := findExternalDependentsBatchedInTx(ctx, tx, ids, idSet)
		if err != nil {
			return nil, fmt.Errorf("get dependents: %w", err)
		}
		result.OrphanedIssues = orphans
	}

	// Populate stats using batched queries.
	expandedIDSet := make(map[string]bool, len(expandedIDs))
	for _, id := range expandedIDs {
		expandedIDSet[id] = true
	}

	var depsCount, labelsCount, eventsCount int
	// Pass 1: deps originating from deleted issues.
	for i := 0; i < len(expandedIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(expandedIDs) {
			end = len(expandedIDs)
		}
		batch := expandedIDs[i:end]
		batchInClause, batchArgs := buildSQLInClause(batch)

		var batchDeps int
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...).Scan(&batchDeps); err != nil {
			return nil, fmt.Errorf("count dependencies: %w", err)
		}
		depsCount += batchDeps

		var batchLabels int
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...).Scan(&batchLabels); err != nil {
			return nil, fmt.Errorf("count labels: %w", err)
		}
		labelsCount += batchLabels

		var batchEvents int
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM events WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...).Scan(&batchEvents); err != nil {
			return nil, fmt.Errorf("count events: %w", err)
		}
		eventsCount += batchEvents
	}

	// Pass 2: inbound deps from outside the deletion set.
	for i := 0; i < len(expandedIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(expandedIDs) {
			end = len(expandedIDs)
		}
		batch := expandedIDs[i:end]
		batchInClause, batchArgs := buildSQLInClause(batch)

		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM dependencies WHERE depends_on_id IN (%s)`, batchInClause),
			batchArgs...)
		if err != nil {
			return nil, fmt.Errorf("count inbound dependencies: %w", err)
		}
		for rows.Next() {
			var issID string
			if err := rows.Scan(&issID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan inbound dependency: %w", err)
			}
			if !expandedIDSet[issID] {
				depsCount++
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate inbound dependencies: %w", err)
		}
	}

	result.DependenciesCount = depsCount
	result.LabelsCount = labelsCount
	result.EventsCount = eventsCount
	result.DeletedCount = len(expandedIDs) + wispDeleteCount

	if dryRun {
		return result, nil
	}

	// Delete in batches. CASCADE handles labels, comments, events,
	// child_counters, issue_snapshots, compaction_snapshots, and
	// dependencies.issue_id — only inbound dependency edges need explicit cleanup.
	totalDeleted := 0
	for i := 0; i < len(expandedIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(expandedIDs) {
			end = len(expandedIDs)
		}
		batch := expandedIDs[i:end]
		batchInClause, batchArgs := buildSQLInClause(batch)

		// Delete inbound dependency edges (depends_on_id has no FK CASCADE).
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM dependencies WHERE depends_on_id IN (%s)`, batchInClause),
			batchArgs...); err != nil {
			return nil, fmt.Errorf("delete inbound dependencies: %w", err)
		}

		// Delete the issues — CASCADE handles the rest.
		deleteResult, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM issues WHERE id IN (%s)`, batchInClause),
			batchArgs...)
		if err != nil {
			return nil, fmt.Errorf("delete issues: %w", err)
		}
		rowsAffected, _ := deleteResult.RowsAffected()
		totalDeleted += int(rowsAffected)
	}
	result.DeletedCount = totalDeleted + wispDeleteCount

	return result, nil
}

// findAllDependentsRecursiveInTx finds all issues that depend on the given
// issues, recursively. Uses batched IN-clause queries. Traversal is capped
// at maxRecursiveResults total discovered IDs.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func findAllDependentsRecursiveInTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		if len(result) > maxRecursiveResults {
			return nil, fmt.Errorf("cascade traversal discovered over %d issues; aborting to prevent runaway deletion", maxRecursiveResults)
		}
		batchEnd := deleteBatchSize
		if batchEnd > len(toProcess) {
			batchEnd = len(toProcess)
		}
		batch := toProcess[:batchEnd]
		toProcess = toProcess[batchEnd:]

		inClause, args := buildSQLInClause(batch)
		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM dependencies WHERE depends_on_id IN (%s)`, inClause),
			args...)
		if err != nil {
			return nil, fmt.Errorf("query dependents for batch: %w", err)
		}

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan dependent: %w", err)
			}
			if !result[depID] {
				result[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate dependents for batch: %w", err)
		}
	}

	return result, nil
}

// findExternalDependentsBatchedInTx finds all dependents of the given IDs
// that are NOT in the idSet.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func findExternalDependentsBatchedInTx(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool) ([]string, error) {
	orphanSet := make(map[string]bool)
	for i := 0; i < len(ids); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]
		inClause, args := buildSQLInClause(batch)

		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM dependencies WHERE depends_on_id IN (%s)`, inClause),
			args...)
		if err != nil {
			return nil, fmt.Errorf("query dependents: %w", err)
		}
		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan dependent: %w", err)
			}
			if !idSet[depID] {
				orphanSet[depID] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate dependents: %w", err)
		}
	}

	result := make([]string, 0, len(orphanSet))
	for id := range orphanSet {
		result = append(result, id)
	}
	return result, nil
}
