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
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetNewlyUnblockedByCloseInTx(ctx context.Context, tx *sql.Tx, closedIssueID string) ([]*types.Issue, error) {
	// Step 1: Find open issues that depend on the closed issue via "blocks" deps.
	// Check all dep/issue table combinations to handle cross-table dependencies
	// (e.g., a permanent issue blocked by a wisp, or vice versa).
	var candidateIDs []string
	for _, pair := range []struct{ depTable, issueTable string }{
		{"dependencies", "issues"},
		{"dependencies", "wisps"},
		{"wisp_dependencies", "wisps"},
		{"wisp_dependencies", "issues"},
	} {
		//nolint:gosec // G201: depTable/issueTable come from hardcoded constants above
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT d.issue_id
			FROM %s d
			JOIN %s i ON d.issue_id = i.id
			WHERE d.depends_on_id = ?
			  AND d.type = 'blocks'
			  AND i.status NOT IN ('closed', 'pinned')
		`, pair.depTable, pair.issueTable), closedIssueID)
		if err != nil {
			return nil, fmt.Errorf("find blocked candidates from %s: %w", pair.depTable, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan candidate from %s: %w", pair.depTable, err)
			}
			candidateIDs = append(candidateIDs, id)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("candidate rows from %s: %w", pair.depTable, err)
		}
	}

	if len(candidateIDs) == 0 {
		return nil, nil
	}

	// Step 2: Filter out candidates that still have other open blockers.
	stillBlocked := make(map[string]bool)
	for start := 0; start < len(candidateIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(candidateIDs) {
			end = len(candidateIDs)
		}
		batch := candidateIDs[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := strings.Join(placeholders, ",")
		args = append(args, closedIssueID)

		for _, pair := range []struct{ depTable, issueTable string }{
			{"dependencies", "issues"},
			{"dependencies", "wisps"},
			{"wisp_dependencies", "wisps"},
			{"wisp_dependencies", "issues"},
		} {
			//nolint:gosec // G201: inClause contains only ? placeholders
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
				SELECT DISTINCT d2.issue_id
				FROM %s d2
				JOIN %s blocker ON d2.depends_on_id = blocker.id
				WHERE d2.issue_id IN (%s)
				  AND d2.type = 'blocks'
				  AND d2.depends_on_id != ?
				  AND blocker.status NOT IN ('closed', 'pinned')
			`, pair.depTable, pair.issueTable, inClause), args...)
			if err != nil {
				return nil, fmt.Errorf("check remaining blockers from %s: %w", pair.depTable, err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan still-blocked from %s: %w", pair.depTable, err)
				}
				stillBlocked[id] = true
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("still-blocked rows from %s: %w", pair.depTable, err)
			}
		}
	}

	// Step 3: Collect unblocked issues.
	var unblocked []*types.Issue
	for _, id := range candidateIDs {
		if stillBlocked[id] {
			continue
		}
		issue, err := GetIssueInTx(ctx, tx, id)
		if err != nil {
			continue // Issue may have been deleted concurrently.
		}
		unblocked = append(unblocked, issue)
	}

	return unblocked, nil
}

// IsBlockedInTx checks if an issue is blocked by active dependencies within
// an existing transaction. Returns whether the issue is blocked and, if so,
// a list of blocker descriptions for display.
// Checks all cross-table combinations so blockers in either issues or wisps
// are detected regardless of which dep table the edge lives in.
//
//nolint:gosec // G201: table names come from hardcoded constants
func IsBlockedInTx(ctx context.Context, tx *sql.Tx, issueID string) (bool, []string, error) {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	_, _, _, depTable := WispTableRouting(isWisp)

	// Check same-table blockers and cross-table blockers.
	// For a permanent issue: deps in "dependencies", blockers in "issues" or "wisps".
	// For a wisp: deps in "wisp_dependencies", blockers in "wisps" or "issues".
	seen := make(map[string]bool)
	var blockers []string

	for _, blockerTable := range []string{"issues", "wisps"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT d.depends_on_id, d.type
			FROM %s d
			JOIN %s i ON d.depends_on_id = i.id
			WHERE d.issue_id = ?
			  AND d.type IN ('blocks', 'waits-for', 'conditional-blocks')
			  AND i.status NOT IN ('closed', 'pinned')
		`, depTable, blockerTable), issueID)
		if err != nil {
			return false, nil, fmt.Errorf("check blockers (%s JOIN %s): %w", depTable, blockerTable, err)
		}
		for rows.Next() {
			var id, depType string
			if err := rows.Scan(&id, &depType); err != nil {
				_ = rows.Close()
				return false, nil, fmt.Errorf("scan blocker: %w", err)
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if depType != "blocks" {
				blockers = append(blockers, id+" ("+depType+")")
			} else {
				blockers = append(blockers, id)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return false, nil, fmt.Errorf("blocker rows (%s JOIN %s): %w", depTable, blockerTable, err)
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
