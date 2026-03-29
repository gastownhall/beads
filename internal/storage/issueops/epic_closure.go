package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// GetEpicsEligibleForClosureInTx returns epics whose children are all closed.
// Uses separate single-table queries to avoid Dolt's joinIter panic on
// multi-table JOINs, and batches IN clauses for performance.
//
// nolint:gosec // G201: table names are hardcoded, placeholders contain only ? markers
func GetEpicsEligibleForClosureInTx(ctx context.Context, tx *sql.Tx) ([]*types.EpicStatus, error) {
	// Step 1: Get open epic IDs (single-table scan)
	epicRows, err := tx.QueryContext(ctx, `
		SELECT id FROM issues
		WHERE issue_type = 'epic'
		  AND status != 'closed'
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get epics: %w", err)
	}
	var epicIDs []string
	for epicRows.Next() {
		var id string
		if err := epicRows.Scan(&id); err != nil {
			epicRows.Close()
			return nil, fmt.Errorf("scan epic id: %w", err)
		}
		epicIDs = append(epicIDs, id)
	}
	epicRows.Close()

	if len(epicIDs) == 0 {
		return nil, nil
	}

	// Step 2: Get parent-child dependencies from both tables (bd-w2w)
	// Wisp children store their parent-child deps in wisp_dependencies,
	// so we must check both tables to find all children of an epic.
	epicChildMap := make(map[string][]string)
	epicSet := make(map[string]bool, len(epicIDs))
	for _, id := range epicIDs {
		epicSet[id] = true
	}
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT depends_on_id, issue_id FROM %s
			WHERE type = 'parent-child'
		`, depTable))
		if err != nil {
			if isTableNotExistError(err) {
				continue // wisp_dependencies may not exist on pre-migration databases
			}
			return nil, fmt.Errorf("failed to get parent-child deps from %s: %w", depTable, err)
		}
		for depRows.Next() {
			var parentID, childID string
			if err := depRows.Scan(&parentID, &childID); err != nil {
				depRows.Close()
				return nil, fmt.Errorf("scan parent-child dep from %s: %w", depTable, err)
			}
			if epicSet[parentID] {
				epicChildMap[parentID] = append(epicChildMap[parentID], childID)
			}
		}
		depRows.Close()
	}

	// Step 3: Batch-fetch statuses for all child issues across all epics
	allChildIDs := make([]string, 0)
	for _, children := range epicChildMap {
		allChildIDs = append(allChildIDs, children...)
	}
	childStatusMap := make(map[string]string)
	if len(allChildIDs) > 0 {
		// Check both issues and wisps tables for child statuses (bd-w2w)
		for _, table := range []string{"issues", "wisps"} {
			for start := 0; start < len(allChildIDs); start += queryBatchSize {
				end := start + queryBatchSize
				if end > len(allChildIDs) {
					end = len(allChildIDs)
				}
				batch := allChildIDs[start:end]
				placeholders, args := buildSQLInClause(batch)

				statusQuery := fmt.Sprintf("SELECT id, status FROM %s WHERE id IN (%s)", table, placeholders)
				statusRows, err := tx.QueryContext(ctx, statusQuery, args...)
				if err != nil {
					if isTableNotExistError(err) {
						break // wisps table may not exist on pre-migration databases
					}
					return nil, fmt.Errorf("failed to batch-fetch child statuses from %s: %w", table, err)
				}
				for statusRows.Next() {
					var id, status string
					if err := statusRows.Scan(&id, &status); err != nil {
						statusRows.Close()
						return nil, fmt.Errorf("scan child status: %w", err)
					}
					childStatusMap[id] = status
				}
				statusRows.Close()
			}
		}
	}

	// Step 4: Batch-fetch all epic issues
	epicsWithChildren := make([]string, 0)
	for _, epicID := range epicIDs {
		if len(epicChildMap[epicID]) > 0 {
			epicsWithChildren = append(epicsWithChildren, epicID)
		}
	}
	epicIssues, err := GetIssuesByIDsInTx(ctx, tx, epicsWithChildren)
	if err != nil {
		return nil, fmt.Errorf("failed to batch-fetch epic issues: %w", err)
	}
	epicIssueMap := make(map[string]*types.Issue, len(epicIssues))
	for _, issue := range epicIssues {
		epicIssueMap[issue.ID] = issue
	}

	// Step 5: Build results from cached data
	var results []*types.EpicStatus
	for _, epicID := range epicIDs {
		children := epicChildMap[epicID]
		if len(children) == 0 {
			continue
		}

		issue, ok := epicIssueMap[epicID]
		if !ok || issue == nil {
			continue
		}

		totalChildren := len(children)
		closedChildren := 0
		for _, childID := range children {
			if status, ok := childStatusMap[childID]; ok && types.Status(status) == types.StatusClosed {
				closedChildren++
			}
		}

		results = append(results, &types.EpicStatus{
			Epic:             issue,
			TotalChildren:    totalChildren,
			ClosedChildren:   closedChildren,
			EligibleForClose: totalChildren > 0 && totalChildren == closedChildren,
		})
	}

	return results, nil
}
