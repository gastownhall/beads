package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// nonCompletingCloseKeywords mark closes that redirect or abandon work rather
// than finish a deliverable. Children closed this way must not count toward
// epic/molecule "complete" eligibility (GH#5026).
var nonCompletingCloseKeywords = []string{
	"duplicate",
	"dup",
	"wontfix",
	"won't fix",
	"wont fix",
	"superseded",
	"obsolete",
	"not planned",
}

// IsNonCompletingClose reports whether closeReason is a redirection/abandon
// (duplicate, wontfix, superseded, …) rather than finished work. Empty reason
// is treated as completing for backward compatibility with closes that never
// recorded a reason.
func IsNonCompletingClose(closeReason string) bool {
	if strings.TrimSpace(closeReason) == "" {
		return false
	}
	lower := strings.ToLower(closeReason)
	for _, kw := range nonCompletingCloseKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// GetEpicsEligibleForClosureInTx returns open epics whose children are all closed
// with completing close reasons (not duplicate/wontfix/superseded).
// nolint:gosec // G201: table names are hardcoded, placeholders contain only ? markers
func GetEpicsEligibleForClosureInTx(ctx context.Context, tx DBTX) ([]*types.EpicStatus, error) {
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
			SELECT %s AS parent_id, issue_id FROM %s
			WHERE type = 'parent-child' AND %s IS NOT NULL
		`, DepTargetExpr, depTable, DepTargetExpr))
		if err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
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

	// Step 3: Batch-fetch statuses + close_reason for all child issues (bd-w2w).
	// close_reason is required so duplicate/wontfix/superseded closes do not
	// count as "complete" for EligibleForClose (GH#5026).
	allChildIDs := make([]string, 0)
	for _, children := range epicChildMap {
		allChildIDs = append(allChildIDs, children...)
	}
	type childCloseInfo struct {
		status      string
		closeReason string
	}
	childInfoMap := make(map[string]childCloseInfo)
	if len(allChildIDs) > 0 {
		for _, table := range []string{"issues", "wisps"} {
			for start := 0; start < len(allChildIDs); start += queryBatchSize {
				end := start + queryBatchSize
				if end > len(allChildIDs) {
					end = len(allChildIDs)
				}
				batch := allChildIDs[start:end]
				placeholders, args := buildSQLInClause(batch)

				statusQuery := fmt.Sprintf(
					"SELECT id, status, COALESCE(close_reason, '') FROM %s WHERE id IN (%s)",
					table, placeholders,
				)
				statusRows, err := tx.QueryContext(ctx, statusQuery, args...)
				if err != nil {
					if isTableNotExistError(err) {
						break // wisps table may not exist on pre-migration databases
					}
					return nil, fmt.Errorf("failed to batch-fetch child statuses from %s: %w", table, err)
				}
				for statusRows.Next() {
					var id, status, closeReason string
					if err := statusRows.Scan(&id, &status, &closeReason); err != nil {
						statusRows.Close()
						return nil, fmt.Errorf("scan child status: %w", err)
					}
					childInfoMap[id] = childCloseInfo{status: status, closeReason: closeReason}
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
	epicIssues, err := GetIssuesByIDsInTx(ctx, tx, epicsWithChildren, nil)
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
		completingClosed := 0
		for _, childID := range children {
			info, ok := childInfoMap[childID]
			if !ok || types.Status(info.status) != types.StatusClosed {
				continue
			}
			closedChildren++
			if !IsNonCompletingClose(info.closeReason) {
				completingClosed++
			}
		}

		// Eligible only when every child is closed AND every close is a completing
		// one (finished work). Duplicate/wontfix/superseded closes leave the
		// epic's real scope unfinished (GH#5026).
		results = append(results, &types.EpicStatus{
			Epic:             issue,
			TotalChildren:    totalChildren,
			ClosedChildren:   closedChildren,
			EligibleForClose: totalChildren > 0 && totalChildren == completingClosed,
		})
	}

	return results, nil
}
