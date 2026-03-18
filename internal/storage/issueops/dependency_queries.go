package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetAllDependencyRecordsInTx returns all dependency records from the dependencies table.
func GetAllDependencyRecordsInTx(ctx context.Context, tx *sql.Tx) (map[string][]*types.Dependency, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
		ORDER BY issue_id
	`)
	if err != nil {
		return nil, fmt.Errorf("get all dependency records: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*types.Dependency)
	for rows.Next() {
		dep, scanErr := scanDependencyRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("get all dependency records: %w", scanErr)
		}
		result[dep.IssueID] = append(result[dep.IssueID], dep)
	}
	return result, rows.Err()
}

// GetDependencyRecordsForIssuesInTx returns dependency records for specific issues,
// routing each ID to dependencies or wisp_dependencies based on wisp status.
func GetDependencyRecordsForIssuesInTx(ctx context.Context, tx *sql.Tx, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Dependency), nil
	}

	result := make(map[string][]*types.Dependency)

	var wispIDs, permIDs []string
	for _, id := range issueIDs {
		if IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}

	for _, pair := range []struct {
		table string
		ids   []string
	}{
		{"wisp_dependencies", wispIDs},
		{"dependencies", permIDs},
	} {
		if len(pair.ids) == 0 {
			continue
		}
		placeholders := make([]string, len(pair.ids))
		args := make([]any, len(pair.ids))
		for i, id := range pair.ids {
			placeholders[i] = "?"
			args[i] = id
		}
		//nolint:gosec // G201: pair.table is hardcoded
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
			 FROM %s WHERE issue_id IN (%s) ORDER BY issue_id`,
			pair.table, strings.Join(placeholders, ",")), args...)
		if err != nil {
			return nil, fmt.Errorf("get dependency records from %s: %w", pair.table, err)
		}
		for rows.Next() {
			dep, scanErr := scanDependencyRow(rows)
			if scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependency records: scan: %w", scanErr)
			}
			result[dep.IssueID] = append(result[dep.IssueID], dep)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependency records: rows: %w", err)
		}
	}

	return result, nil
}

// GetDependencyCountsInTx returns dependency counts for multiple issues within a transaction.
func GetDependencyCountsInTx(ctx context.Context, tx *sql.Tx, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*types.DependencyCounts), nil
	}

	result := make(map[string]*types.DependencyCounts)
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{}
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Blockers: issues that block the given IDs
	//nolint:gosec // G201: inClause contains only ? placeholders
	depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, COUNT(*) as cnt
		FROM dependencies
		WHERE issue_id IN (%s) AND type = 'blocks'
		GROUP BY issue_id
	`, inClause), args...)
	if err != nil {
		return nil, fmt.Errorf("get dependency counts (blockers): %w", err)
	}
	for depRows.Next() {
		var id string
		var cnt int
		if err := depRows.Scan(&id, &cnt); err != nil {
			_ = depRows.Close()
			return nil, fmt.Errorf("get dependency counts: scan blocker: %w", err)
		}
		if c, ok := result[id]; ok {
			c.DependencyCount = cnt
		}
	}
	_ = depRows.Close()
	if err := depRows.Err(); err != nil {
		return nil, fmt.Errorf("get dependency counts: blocker rows: %w", err)
	}

	// Dependents: issues blocked by the given IDs
	//nolint:gosec // G201: inClause contains only ? placeholders
	blockingRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT depends_on_id, COUNT(*) as cnt
		FROM dependencies
		WHERE depends_on_id IN (%s) AND type = 'blocks'
		GROUP BY depends_on_id
	`, inClause), args...)
	if err != nil {
		return nil, fmt.Errorf("get dependency counts (dependents): %w", err)
	}
	for blockingRows.Next() {
		var id string
		var cnt int
		if err := blockingRows.Scan(&id, &cnt); err != nil {
			_ = blockingRows.Close()
			return nil, fmt.Errorf("get dependency counts: scan dependent: %w", err)
		}
		if c, ok := result[id]; ok {
			c.DependentCount = cnt
		}
	}
	_ = blockingRows.Close()
	if err := blockingRows.Err(); err != nil {
		return nil, fmt.Errorf("get dependency counts: dependent rows: %w", err)
	}

	return result, nil
}

// GetBlockingInfoForIssuesInTx returns blocking dependency records for a set of issue IDs.
// Returns three maps:
//   - blockedByMap: issueID -> list of IDs blocking it
//   - blocksMap: issueID -> list of IDs it blocks
//   - parentMap: childID -> parentID (parent-child deps)
func GetBlockingInfoForIssuesInTx(ctx context.Context, tx *sql.Tx, issueIDs []string) (
	blockedByMap map[string][]string,
	blocksMap map[string][]string,
	parentMap map[string]string,
	err error,
) {
	blockedByMap = make(map[string][]string)
	blocksMap = make(map[string][]string)
	parentMap = make(map[string]string)

	if len(issueIDs) == 0 {
		return
	}

	// Partition into wisp and perm IDs for routing.
	var wispIDs, permIDs []string
	for _, id := range issueIDs {
		if IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}

	// Process wisp IDs against wisp_dependencies.
	if len(wispIDs) > 0 {
		if err := queryBlockingInfo(ctx, tx, wispIDs, "wisp_dependencies", "wisps", blockedByMap, blocksMap, parentMap); err != nil {
			return nil, nil, nil, err
		}
	}

	// Process perm IDs against dependencies.
	if len(permIDs) > 0 {
		if err := queryBlockingInfo(ctx, tx, permIDs, "dependencies", "issues", blockedByMap, blocksMap, parentMap); err != nil {
			return nil, nil, nil, err
		}
	}

	return blockedByMap, blocksMap, parentMap, nil
}

// queryBlockingInfo queries blocking info from a specific dep table + issue table pair.
func queryBlockingInfo(
	ctx context.Context, tx *sql.Tx,
	issueIDs []string,
	depTable, issueTable string,
	blockedByMap map[string][]string,
	blocksMap map[string][]string,
	parentMap map[string]string,
) error {
	placeholders := make([]string, len(issueIDs))
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Query 1: "blocked by" — deps where issue_id is in our set
	//nolint:gosec // G201: depTable and issueTable are caller-controlled constants
	blockedByQuery := fmt.Sprintf(`
		SELECT d.issue_id, d.depends_on_id, d.type, COALESCE(i.status, '') AS blocker_status
		FROM %s d
		LEFT JOIN %s i ON i.id = d.depends_on_id
		WHERE d.issue_id IN (%s) AND d.type IN ('blocks', 'parent-child')
	`, depTable, issueTable, inClause)

	rows, err := tx.QueryContext(ctx, blockedByQuery, args...)
	if err != nil {
		return fmt.Errorf("get blocked-by info from %s: %w", depTable, err)
	}
	for rows.Next() {
		var issueID, blockerID, depType, blockerStatus string
		if scanErr := rows.Scan(&issueID, &blockerID, &depType, &blockerStatus); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("get blocking info: scan blocked-by: %w", scanErr)
		}
		if types.Status(blockerStatus) == types.StatusClosed {
			continue
		}
		if depType == "parent-child" {
			parentMap[issueID] = blockerID
		} else {
			blockedByMap[issueID] = append(blockedByMap[issueID], blockerID)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("get blocking info: blocked-by rows: %w", err)
	}

	// Query 2: "blocks" — deps where depends_on_id is in our set
	//nolint:gosec // G201: depTable and issueTable are caller-controlled constants
	blocksQuery := fmt.Sprintf(`
		SELECT d.depends_on_id, d.issue_id, d.type, COALESCE(i.status, '') AS blocker_status
		FROM %s d
		LEFT JOIN %s i ON i.id = d.depends_on_id
		WHERE d.depends_on_id IN (%s) AND d.type IN ('blocks', 'parent-child')
	`, depTable, issueTable, inClause)

	rows2, err := tx.QueryContext(ctx, blocksQuery, args...)
	if err != nil {
		return fmt.Errorf("get blocks info from %s: %w", depTable, err)
	}
	for rows2.Next() {
		var blockerID, blockedID, depType, blockerStatus string
		if scanErr := rows2.Scan(&blockerID, &blockedID, &depType, &blockerStatus); scanErr != nil {
			_ = rows2.Close()
			return fmt.Errorf("get blocking info: scan blocks: %w", scanErr)
		}
		if types.Status(blockerStatus) == types.StatusClosed {
			continue
		}
		if depType == "parent-child" {
			continue
		}
		blocksMap[blockerID] = append(blocksMap[blockerID], blockedID)
	}
	_ = rows2.Close()
	if err := rows2.Err(); err != nil {
		return fmt.Errorf("get blocking info: blocks rows: %w", err)
	}

	return nil
}

// scanDependencyRow scans a single dependency row from a *sql.Rows.
func scanDependencyRow(rows *sql.Rows) (*types.Dependency, error) {
	var dep types.Dependency
	var createdAt sql.NullTime
	var metadata, threadID sql.NullString

	if err := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type, &createdAt, &dep.CreatedBy, &metadata, &threadID); err != nil {
		return nil, fmt.Errorf("scan dependency: %w", err)
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
