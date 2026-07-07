package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

type blockingDepRecord struct {
	issueID, dependsOnID, depType string
	metadata                      sql.NullString
}

func optionalBlockedTable(table string) bool {
	return table == "wisps" || table == "wisp_dependencies"
}

func loadBlockingDepsForIssueIDsInTx(ctx context.Context, tx DBTX, depTables []string, issueIDs []string) ([]blockingDepRecord, error) {
	var deps []blockingDepRecord
	for _, depTable := range depTables {
		//nolint:gosec // G201: depTable is a hardcoded constant.
		query := fmt.Sprintf(`
			SELECT issue_id, %s AS depends_on_id, type, metadata FROM %s
			WHERE issue_id = ?
			  AND (type = 'blocks' OR type = 'waits-for' OR type = 'conditional-blocks')
		`, DepTargetExpr, depTable)
		for _, id := range issueIDs {
			rows, err := tx.QueryContext(ctx, query, id)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("compute blocked IDs: deps from %s: %w", depTable, err)
			}
			for rows.Next() {
				var rec blockingDepRecord
				if err := rows.Scan(&rec.issueID, &rec.dependsOnID, &rec.depType, &rec.metadata); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("compute blocked IDs: scan dep: %w", err)
				}
				deps = append(deps, rec)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("compute blocked IDs: dep rows from %s: %w", depTable, err)
			}
		}
	}
	return deps, nil
}

func loadParentIDsForChildrenInTx(ctx context.Context, tx DBTX, depTables []string, childIDs []string) (map[string]string, error) {
	childParents := make(map[string]string)
	for _, depTable := range depTables {
		//nolint:gosec // G201: depTable is a hardcoded constant.
		query := fmt.Sprintf(`
			SELECT issue_id, %s AS depends_on_id FROM %s
			WHERE issue_id = ?
			  AND type = 'parent-child'
		`, DepTargetExpr, depTable)
		for _, id := range childIDs {
			rows, err := tx.QueryContext(ctx, query, id)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("candidate parents from %s: %w", depTable, err)
			}
			for rows.Next() {
				var childID, parentID string
				if err := rows.Scan(&childID, &parentID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan candidate parent: %w", err)
				}
				childParents[childID] = parentID
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("candidate parent rows from %s: %w", depTable, err)
			}
		}
	}
	return childParents, nil
}

//nolint:gosec // G201: tables are hardcoded
func GetChildrenWithParentsInTx(ctx context.Context, tx DBTX, parentIDs []string) (map[string]string, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	result := make(map[string]string)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		query := fmt.Sprintf(`
			SELECT issue_id, %s AS depends_on_id FROM %s
			WHERE type = 'parent-child' AND %s = ?
		`, DepTargetExpr, depTable, DepTargetExpr)
		for _, parentID := range parentIDs {
			rows, err := tx.QueryContext(ctx, query, parentID)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("get children with parents from %s: %w", depTable, err)
			}
			for rows.Next() {
				var childID, parentID string
				if err := rows.Scan(&childID, &parentID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan children with parents: %w", err)
				}
				result[childID] = parentID
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("children with parents rows from %s: %w", depTable, err)
			}
		}
	}
	return result, nil
}

//nolint:gosec // G201: tables are hardcoded
func GetChildrenOfIssuesInTx(ctx context.Context, tx DBTX, parentIDs []string) ([]string, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	var children []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		query := fmt.Sprintf(`
			SELECT issue_id FROM %s
			WHERE type = 'parent-child' AND %s = ?
		`, depTable, DepTargetExpr)
		for _, parentID := range parentIDs {
			rows, err := tx.QueryContext(ctx, query, parentID)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("get children of issues from %s: %w", depTable, err)
			}
			for rows.Next() {
				var childID string
				if err := rows.Scan(&childID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan child: %w", err)
				}
				children = append(children, childID)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("children rows from %s: %w", depTable, err)
			}
		}
	}
	return children, nil
}

func GetDescendantIDsInTx(ctx context.Context, tx DBTX, rootID string, maxDepth int) ([]string, error) {
	if rootID == "" {
		return nil, nil
	}

	queryDescendants := func(includeWisps bool) ([]string, bool, error) {
		edgeQuery := fmt.Sprintf(`
			SELECT issue_id, %s FROM dependencies WHERE type = 'parent-child'
		`, DepTargetExpr)
		if includeWisps {
			edgeQuery += fmt.Sprintf(`
			UNION ALL
			SELECT issue_id, %s FROM wisp_dependencies WHERE type = 'parent-child'
		`, DepTargetExpr)
		}

		//nolint:gosec // G201: edgeQuery is built from hardcoded SQL plus DepTargetExpr (no user input)
		query := fmt.Sprintf(`
			WITH RECURSIVE
			parent_edges(issue_id, depends_on_id) AS (
				%s
			),
			descendants(id, depth, path) AS (
				SELECT issue_id, 1, CONCAT(',', ?, ',', issue_id, ',')
				FROM parent_edges
				WHERE depends_on_id = ?
				UNION ALL
				SELECT e.issue_id, d.depth + 1, CONCAT(d.path, e.issue_id, ',')
				FROM parent_edges e
				JOIN descendants d ON e.depends_on_id = d.id
				WHERE (? <= 0 OR d.depth < ?)
				  AND LOCATE(CONCAT(',', e.issue_id, ','), d.path) = 0
			)
			SELECT id, depth FROM descendants WHERE id <> ?
		`, edgeQuery)

		rows, err := tx.QueryContext(ctx, query, rootID, rootID, maxDepth, maxDepth, rootID)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = rows.Close() }()

		var result []string
		reachedMaxDepth := false
		for rows.Next() {
			var id string
			var depth int
			if err := rows.Scan(&id, &depth); err != nil {
				return nil, false, fmt.Errorf("scan descendant: %w", err)
			}
			result = append(result, id)
			if maxDepth > 0 && depth >= maxDepth {
				reachedMaxDepth = true
			}
		}
		if err := rows.Err(); err != nil {
			return nil, false, fmt.Errorf("descendant rows: %w", err)
		}
		return result, reachedMaxDepth, nil
	}

	result, reachedMaxDepth, err := queryDescendants(true)
	if err != nil {
		if !isTableNotExistError(err) {
			return nil, err
		}
		result, reachedMaxDepth, err = queryDescendants(false)
		if err != nil {
			return nil, err
		}
	}
	if reachedMaxDepth {
		return nil, fmt.Errorf("parent descendant traversal for %s reached max depth %d", rootID, maxDepth)
	}
	return result, nil
}

//nolint:gosec // G201: tables are hardcoded
func GetBlockedIssuesInTx(ctx context.Context, tx DBTX, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	var blockedIDList []string
	blockedSet := make(map[string]bool)
	for _, table := range []string{"issues", "wisps"} {
		//nolint:gosec // G201: table is one of two hardcoded values.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT id FROM %s
			WHERE is_blocked = 1 AND status <> 'closed' AND status <> 'pinned'
		`, table))
		if err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return nil, fmt.Errorf("read blocked ids from %s: %w", table, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan blocked id from %s: %w", table, err)
			}
			if !blockedSet[id] {
				blockedSet[id] = true
				blockedIDList = append(blockedIDList, id)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("blocked id rows from %s: %w", table, err)
		}
	}
	if len(blockedIDList) == 0 {
		return nil, nil
	}

	blockerMap := make(map[string][]string)
	blockingDeps, err := loadBlockingDepsForIssueIDsInTx(ctx, tx, []string{"dependencies", "wisp_dependencies"}, blockedIDList)
	if err != nil {
		return nil, fmt.Errorf("get blocking deps: %w", err)
	}
	if len(blockingDeps) > 0 {
		targetIDs := make([]string, 0, len(blockingDeps))
		seenTargets := make(map[string]bool, len(blockingDeps))
		for _, rec := range blockingDeps {
			if !seenTargets[rec.dependsOnID] {
				seenTargets[rec.dependsOnID] = true
				targetIDs = append(targetIDs, rec.dependsOnID)
			}
		}
		activeTargets, err := loadStatusByIDInTx(ctx, tx, targetIDs)
		if err != nil {
			return nil, fmt.Errorf("blocker target status: %w", err)
		}
		for _, rec := range blockingDeps {
			status, ok := activeTargets[rec.dependsOnID]
			if !ok || status == types.StatusClosed || status == types.StatusPinned {
				continue
			}
			blockerMap[rec.issueID] = append(blockerMap[rec.issueID], rec.dependsOnID)
		}
	}

	var inheritedIDs []string
	for _, id := range blockedIDList {
		if _, ok := blockerMap[id]; !ok {
			inheritedIDs = append(inheritedIDs, id)
		}
	}
	if len(inheritedIDs) > 0 {
		parentMap, err := loadParentIDsForChildrenInTx(ctx, tx, []string{"dependencies", "wisp_dependencies"}, inheritedIDs)
		if err == nil {
			for childID, parentID := range parentMap {
				if _, alreadyHas := blockerMap[childID]; !alreadyHas {
					blockerMap[childID] = []string{parentID}
				}
			}
		}
	}

	displayIDs := make([]string, 0, len(blockerMap))
	for id := range blockerMap {
		displayIDs = append(displayIDs, id)
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, displayIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("batch-fetch blocked issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	var parentChildSet map[string]bool
	if filter.ParentID != nil {
		parentChildSet = make(map[string]bool)
		parentID := *filter.ParentID
		children, childErr := GetChildrenOfIssuesInTx(ctx, tx, []string{parentID})
		if childErr == nil {
			for _, childID := range children {
				parentChildSet[childID] = true
			}
		}
		for id := range blockerMap {
			if strings.HasPrefix(id, parentID+".") {
				parentChildSet[id] = true
			}
		}
	}

	var results []*types.BlockedIssue
	for id, blockerIDs := range blockerMap {
		if parentChildSet != nil && !parentChildSet[id] {
			continue
		}
		issue, ok := issueMap[id]
		if !ok || issue == nil {
			continue
		}
		results = append(results, &types.BlockedIssue{
			Issue:          *issue,
			BlockedByCount: len(blockerIDs),
			BlockedBy:      blockerIDs,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Issue.Priority != results[j].Issue.Priority {
			return results[i].Issue.Priority < results[j].Issue.Priority
		}
		return results[i].Issue.CreatedAt.After(results[j].Issue.CreatedAt)
	})

	return results, nil
}
