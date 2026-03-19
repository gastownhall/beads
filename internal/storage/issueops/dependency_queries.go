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
// Uses batched IN clauses (queryBatchSize) to avoid query-planner spikes.
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
		for start := 0; start < len(pair.ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(pair.ids) {
				end = len(pair.ids)
			}
			batch := pair.ids[start:end]
			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
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
	}

	return result, nil
}

// GetDependencyCountsInTx returns dependency counts for multiple issues within a transaction.
// Uses batched IN clauses (queryBatchSize) to avoid query-planner spikes.
func GetDependencyCountsInTx(ctx context.Context, tx *sql.Tx, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*types.DependencyCounts), nil
	}

	result := make(map[string]*types.DependencyCounts)
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{}
	}

	for start := 0; start < len(issueIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(issueIDs) {
			end = len(issueIDs)
		}
		batch := issueIDs[start:end]

		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
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
// Uses batched IN clauses (queryBatchSize) to avoid query-planner spikes.
func queryBlockingInfo(
	ctx context.Context, tx *sql.Tx,
	issueIDs []string,
	depTable, issueTable string,
	blockedByMap map[string][]string,
	blocksMap map[string][]string,
	parentMap map[string]string,
) error {
	for start := 0; start < len(issueIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(issueIDs) {
			end = len(issueIDs)
		}
		batch := issueIDs[start:end]

		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
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
	}

	return nil
}

// GetNewlyUnblockedByCloseInTx finds issues that become unblocked when the
// given issue is closed. Works within an existing transaction.
// Returns full issue objects for the newly-unblocked issues.
// Uses separate single-table queries (no JOINs) to avoid Dolt's mergeJoinKvIter
// panic when joining across tables with different tuple formats.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetNewlyUnblockedByCloseInTx(ctx context.Context, tx *sql.Tx, closedIssueID string) ([]*types.Issue, error) {
	// Step 1: Find issue IDs that depend on the closed issue via "blocks" deps.
	// Query both dep tables to cover cross-table dependencies.
	candidateSet := make(map[string]bool)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id FROM %s
			WHERE depends_on_id = ? AND type = 'blocks'
		`, depTable), closedIssueID)
		if err != nil {
			return nil, fmt.Errorf("find blocked candidates from %s: %w", depTable, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan candidate from %s: %w", depTable, err)
			}
			candidateSet[id] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("candidate rows from %s: %w", depTable, err)
		}
	}

	if len(candidateSet) == 0 {
		return nil, nil
	}

	// Filter to only open/active candidates (check both tables, no JOINs).
	var candidateIDs []string
	for id := range candidateSet {
		var status string
		found := false
		for _, table := range []string{"issues", "wisps"} {
			err := tx.QueryRowContext(ctx, fmt.Sprintf(
				`SELECT status FROM %s WHERE id = ?`, table), id).Scan(&status)
			if err == nil {
				found = true
				break
			}
		}
		if !found || status == "closed" || status == "pinned" {
			continue
		}
		candidateIDs = append(candidateIDs, id)
	}

	if len(candidateIDs) == 0 {
		return nil, nil
	}

	// Step 2: Filter out candidates that still have other open blockers.
	// For each candidate, get all its blocking deps (excluding the closed issue),
	// then check if any of those blockers are still active.
	stillBlocked := make(map[string]bool)
	for _, candidateID := range candidateIDs {
		// Determine which dep table this candidate uses.
		isWisp := IsActiveWispInTx(ctx, tx, candidateID)
		_, _, _, depTable := WispTableRouting(isWisp)

		//nolint:gosec // G201: depTable from WispTableRouting (hardcoded)
		depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT depends_on_id FROM %s
			WHERE issue_id = ? AND type = 'blocks' AND depends_on_id != ?
		`, depTable), candidateID, closedIssueID)
		if err != nil {
			return nil, fmt.Errorf("check remaining blockers for %s: %w", candidateID, err)
		}
		for depRows.Next() {
			var blockerID string
			if err := depRows.Scan(&blockerID); err != nil {
				_ = depRows.Close()
				return nil, fmt.Errorf("scan remaining blocker: %w", err)
			}
			// Check if this blocker is still active (in either table).
			var blockerStatus string
			for _, table := range []string{"issues", "wisps"} {
				err := tx.QueryRowContext(ctx, fmt.Sprintf(
					`SELECT status FROM %s WHERE id = ?`, table), blockerID).Scan(&blockerStatus)
				if err == nil {
					break
				}
			}
			if blockerStatus != "" && blockerStatus != "closed" && blockerStatus != "pinned" {
				stillBlocked[candidateID] = true
				break
			}
		}
		_ = depRows.Close()
	}

	// Step 3: Collect unblocked issues.
	var unblocked []*types.Issue
	for _, id := range candidateIDs {
		if stillBlocked[id] {
			continue
		}
		issue, err := GetIssueInTx(ctx, tx, id)
		if err != nil {
			continue
		}
		unblocked = append(unblocked, issue)
	}

	return unblocked, nil
}

// IsBlockedInTx checks if an issue is blocked by active dependencies within
// an existing transaction. Returns whether the issue is blocked and, if so,
// a list of blocker descriptions for display.
// Uses separate single-table queries (no JOINs) to avoid Dolt's mergeJoinKvIter
// panic when joining across tables with different tuple formats.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func IsBlockedInTx(ctx context.Context, tx *sql.Tx, issueID string) (bool, []string, error) {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	_, _, _, depTable := WispTableRouting(isWisp)

	// Step 1: Get all blocking dependency targets from the dep table.
	type depEdge struct {
		dependsOnID, depType string
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT depends_on_id, type FROM %s
		WHERE issue_id = ? AND type IN ('blocks', 'waits-for', 'conditional-blocks')
	`, depTable), issueID)
	if err != nil {
		return false, nil, fmt.Errorf("check blockers: %w", err)
	}
	var edges []depEdge
	for rows.Next() {
		var e depEdge
		if err := rows.Scan(&e.dependsOnID, &e.depType); err != nil {
			_ = rows.Close()
			return false, nil, fmt.Errorf("scan blocker edge: %w", err)
		}
		edges = append(edges, e)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, nil, fmt.Errorf("blocker edge rows: %w", err)
	}

	if len(edges) == 0 {
		return false, nil, nil
	}

	// Step 2: Check each blocker's status in both issues and wisps tables.
	// Uses single-row queries to avoid cross-table JOINs.
	var blockers []string
	for _, e := range edges {
		var status string
		found := false
		for _, table := range []string{"issues", "wisps"} {
			err := tx.QueryRowContext(ctx, fmt.Sprintf(
				`SELECT status FROM %s WHERE id = ?`, table), e.dependsOnID).Scan(&status)
			if err == nil {
				found = true
				break
			}
		}
		if !found {
			continue // Blocker not found in either table
		}
		if status == "closed" || status == "pinned" {
			continue // Not an active blocker
		}
		if e.depType != "blocks" {
			blockers = append(blockers, e.dependsOnID+" ("+e.depType+")")
		} else {
			blockers = append(blockers, e.dependsOnID)
		}
	}

	return len(blockers) > 0, blockers, nil
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
