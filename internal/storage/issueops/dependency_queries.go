package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetAllDependencyRecordsInTx returns all dependency records from permanent and
// wisp dependency tables.
func GetAllDependencyRecordsInTx(ctx context.Context, tx DBTX) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		if err := getAllDependencyRecordsIntoFromTable(ctx, tx, depTable, result); err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
				continue
			}
			return nil, err
		}
	}
	return result, nil
}

//nolint:gosec // G201: depTable is "dependencies" or "wisp_dependencies" (hardcoded by caller).
func getAllDependencyRecordsIntoFromTable(ctx context.Context, tx DBTX, depTable string, result map[string][]*types.Dependency) error {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
			FROM %s
			ORDER BY issue_id
		`, DepTargetExpr, depTable))
	if err != nil {
		return fmt.Errorf("get all dependency records from %s: %w", depTable, err)
	}
	defer rows.Close()

	for rows.Next() {
		dep, scanErr := scanDependencyRow(rows)
		if scanErr != nil {
			return fmt.Errorf("get all dependency records from %s: %w", depTable, scanErr)
		}
		result[dep.IssueID] = append(result[dep.IssueID], dep)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("get all dependency records from %s: %w", depTable, err)
	}
	return nil
}

// GetDependencyRecordsForIssuesInTx returns dependency records for specific issues,
// routing each ID to dependencies or wisp_dependencies based on wisp status.
// Uses a single batched wisp-partition query + batched IN clauses, so cost is
// O(1 + N/queryBatchSize) round-trips rather than O(N) — important on remote
// backends (see GH#3414).
func GetDependencyRecordsForIssuesInTx(ctx context.Context, tx DBTX, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Dependency), nil
	}

	wispIDs, permIDs, err := PartitionWispIDsInTx(ctx, tx, issueIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]*types.Dependency)
	if len(wispIDs) > 0 {
		if err := getDependencyRecordsIntoFromTable(ctx, tx, "wisp_dependencies", wispIDs, result); err != nil {
			return nil, err
		}
	}
	if len(permIDs) > 0 {
		if err := getDependencyRecordsIntoFromTable(ctx, tx, "dependencies", permIDs, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetDependencyRecordsForIssuesFromTableInTx is a fast-path variant used by
// callers that already know every ID belongs to a single dep table (e.g.
// searchTableInTx). Skips the wisp-partition round-trip.
func GetDependencyRecordsForIssuesFromTableInTx(ctx context.Context, tx DBTX, depTable string, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Dependency), nil
	}
	result := make(map[string][]*types.Dependency)
	if err := getDependencyRecordsIntoFromTable(ctx, tx, depTable, issueIDs, result); err != nil {
		return nil, err
	}
	return result, nil
}

//nolint:gosec // G201: depTable is "dependencies" or "wisp_dependencies" (hardcoded by callers).
func getDependencyRecordsIntoFromTable(ctx context.Context, tx DBTX, depTable string, ids []string, result map[string][]*types.Dependency) error {
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
			 FROM %s WHERE issue_id IN (%s) ORDER BY issue_id`,
			DepTargetExpr, depTable, strings.Join(placeholders, ",")), args...)
		if err != nil {
			return fmt.Errorf("get dependency records from %s: %w", depTable, err)
		}
		for rows.Next() {
			dep, scanErr := scanDependencyRow(rows)
			if scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("get dependency records: scan: %w", scanErr)
			}
			result[dep.IssueID] = append(result[dep.IssueID], dep)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("get dependency records: rows: %w", err)
		}
	}
	return nil
}

func GetDependencyCountsInTx(ctx context.Context, tx DBTX, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*types.DependencyCounts), nil
	}

	result := make(map[string]*types.DependencyCounts)
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{}
	}

	depTables := []string{"dependencies", "wisp_dependencies"}
	if empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx); probeErr != nil {
		return nil, fmt.Errorf("get dependency counts: probe: %w", probeErr)
	} else if empty {
		depTables = []string{"dependencies"}
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

		for _, depTable := range depTables {
			//nolint:gosec // G201: depTable is hardcoded and inClause contains only ? placeholders.
			depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
				SELECT issue_id, COUNT(*) as cnt
				FROM %s
				WHERE issue_id IN (%s) AND type = 'blocks'
				GROUP BY issue_id
			`, depTable, inClause), args...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("get dependency counts (blockers from %s): %w", depTable, err)
			}
			for depRows.Next() {
				var id string
				var cnt int
				if err := depRows.Scan(&id, &cnt); err != nil {
					_ = depRows.Close()
					return nil, fmt.Errorf("get dependency counts: scan blocker: %w", err)
				}
				if c, ok := result[id]; ok {
					c.DependencyCount += cnt
				}
			}
			_ = depRows.Close()
			if err := depRows.Err(); err != nil {
				return nil, fmt.Errorf("get dependency counts: blocker rows: %w", err)
			}

			//nolint:gosec // G201: depTable is hardcoded and inClause contains only ? placeholders.
			blockingRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
				SELECT %s AS depends_on_id, COUNT(*) as cnt
				FROM %s
				WHERE %s AND type = 'blocks'
				GROUP BY %s
			`, DepTargetExpr, depTable, depTargetIn("", inClause), DepTargetExpr), args...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("get dependency counts (dependents from %s): %w", depTable, err)
			}
			for blockingRows.Next() {
				var id string
				var cnt int
				if err := blockingRows.Scan(&id, &cnt); err != nil {
					_ = blockingRows.Close()
					return nil, fmt.Errorf("get dependency counts: scan dependent: %w", err)
				}
				if c, ok := result[id]; ok {
					c.DependentCount += cnt
				}
			}
			_ = blockingRows.Close()
			if err := blockingRows.Err(); err != nil {
				return nil, fmt.Errorf("get dependency counts: dependent rows: %w", err)
			}
		}
	}

	return result, nil
}

// GetBlockingInfoForIssuesInTx returns blocking dependency records for a set of issue IDs.
// Returns three maps:
//   - blockedByMap: issueID -> list of IDs blocking it
//   - blocksMap: issueID -> list of IDs it blocks
//   - parentMap: childID -> parentID (parent-child deps)
func GetBlockingInfoForIssuesInTx(ctx context.Context, tx DBTX, issueIDs []string) (
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

	// Partition into wisp and perm IDs for routing. Use the batched
	// partitioner so we don't take a round-trip per ID on remote backends
	// (GH#3414).
	wispIDs, permIDs, partErr := PartitionWispIDsInTx(ctx, tx, issueIDs)
	if partErr != nil {
		return nil, nil, nil, partErr
	}

	// Process wisp IDs against wisp_dependencies.
	if len(wispIDs) > 0 {
		if err := queryBlockedByInfo(ctx, tx, wispIDs, "wisp_dependencies", blockedByMap, parentMap); err != nil {
			return nil, nil, nil, err
		}
	}

	// Process perm IDs against dependencies.
	if len(permIDs) > 0 {
		if err := queryBlockedByInfo(ctx, tx, permIDs, "dependencies", blockedByMap, parentMap); err != nil {
			return nil, nil, nil, err
		}
	}

	// "Blocks" is target-oriented, so scan both dependency tables regardless of
	// the target issue's storage class.
	if err := queryBlocksInfo(ctx, tx, issueIDs, []string{"dependencies", "wisp_dependencies"}, blocksMap); err != nil {
		return nil, nil, nil, err
	}

	return blockedByMap, blocksMap, parentMap, nil
}

type blockingInfoRow struct {
	issueID, blockerID, depType string
}

// queryBlockedByInfo queries outbound blocking info from a specific dependency
// table. Blocker status is resolved against both issue storage classes so
// cross-class closed blockers do not appear active.
// Uses batched IN clauses (queryBatchSize) to avoid query-planner spikes.
func queryBlockedByInfo(
	ctx context.Context, tx DBTX,
	issueIDs []string,
	depTable string,
	blockedByMap map[string][]string,
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

		// Query: "blocked by" — deps where issue_id is in our set.
		//nolint:gosec // G201: depTable is a caller-controlled constant.
		blockedByQuery := fmt.Sprintf(`
			SELECT d.issue_id, %s AS depends_on_id, d.type
			FROM %s d
			WHERE d.issue_id IN (%s) AND d.type IN ('blocks', 'parent-child')
		`, depTargetExpr("d"), depTable, inClause)

		rows, err := tx.QueryContext(ctx, blockedByQuery, args...)
		if err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
				continue
			}
			return fmt.Errorf("get blocked-by info from %s: %w", depTable, err)
		}
		var depRows []blockingInfoRow
		var blockerIDs []string
		for rows.Next() {
			var row blockingInfoRow
			if scanErr := rows.Scan(&row.issueID, &row.blockerID, &row.depType); scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("get blocking info: scan blocked-by: %w", scanErr)
			}
			depRows = append(depRows, row)
			blockerIDs = append(blockerIDs, row.blockerID)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("get blocking info: blocked-by rows: %w", err)
		}

		statusByID, err := loadStatusByIDInTx(ctx, tx, blockerIDs)
		if err != nil {
			return fmt.Errorf("get blocking info: blocker status: %w", err)
		}
		for _, row := range depRows {
			if statusByID[row.blockerID] == types.StatusClosed {
				continue
			}
			if row.depType == "parent-child" {
				parentMap[row.issueID] = row.blockerID
			} else {
				blockedByMap[row.issueID] = append(blockedByMap[row.issueID], row.blockerID)
			}
		}
	}

	return nil
}

// queryBlocksInfo queries inbound blocking info across dependency tables.
func queryBlocksInfo(
	ctx context.Context, tx DBTX,
	issueIDs []string,
	depTables []string,
	blocksMap map[string][]string,
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
		statusByID, err := loadStatusByIDInTx(ctx, tx, batch)
		if err != nil {
			return fmt.Errorf("get blocking info: blocker status: %w", err)
		}

		for _, depTable := range depTables {
			// Query: "blocks" — deps where depends_on_id is in our set.
			//nolint:gosec // G201: depTable is a caller-controlled constant.
			blocksQuery := fmt.Sprintf(`
				SELECT %s AS depends_on_id, d.issue_id, d.type
				FROM %s d
				WHERE %s AND d.type IN ('blocks', 'parent-child')
			`, depTargetExpr("d"), depTable, depTargetIn("d", inClause))

			rows, err := tx.QueryContext(ctx, blocksQuery, args...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return fmt.Errorf("get blocks info from %s: %w", depTable, err)
			}
			for rows.Next() {
				var blockerID, blockedID, depType string
				if scanErr := rows.Scan(&blockerID, &blockedID, &depType); scanErr != nil {
					_ = rows.Close()
					return fmt.Errorf("get blocking info: scan blocks: %w", scanErr)
				}
				if statusByID[blockerID] == types.StatusClosed {
					continue
				}
				if depType == "parent-child" {
					continue
				}
				blocksMap[blockerID] = append(blocksMap[blockerID], blockedID)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("get blocking info: blocks rows: %w", err)
			}
		}
	}

	return nil
}

func loadStatusByIDInTx(ctx context.Context, tx DBTX, ids []string) (map[string]types.Status, error) {
	statusByID := make(map[string]types.Status)
	if len(ids) == 0 {
		return statusByID, nil
	}

	sourceByID := make(map[string]string)
	for _, issueTable := range []string{"issues", "wisps"} {
		for start := 0; start < len(ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			placeholders, args := buildSQLInClause(ids[start:end])
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
				SELECT id, status FROM %s WHERE id IN (%s)
			`, issueTable, placeholders), args...)
			if err != nil {
				if optionalBlockedTable(issueTable) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("status from %s: %w", issueTable, err)
			}
			for rows.Next() {
				var id string
				var status types.Status
				if err := rows.Scan(&id, &status); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan status: %w", err)
				}
				if _, exists := sourceByID[id]; exists {
					// Prefer wisps-table status on cross-table dup (be-iabdi).
					// Tables iterate issues→wisps so the second encounter is always wisps.
					sourceByID[id] = issueTable
					statusByID[id] = status
					continue
				}
				sourceByID[id] = issueTable
				statusByID[id] = status
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("status rows from %s: %w", issueTable, err)
			}
		}
	}
	return statusByID, nil
}

// GetNewlyUnblockedByCloseInTx finds issues that become unblocked when the
// given issue is closed. Works within an existing transaction.
// Returns full issue objects for the newly-unblocked issues.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetNewlyUnblockedByCloseInTx(ctx context.Context, tx DBTX, closedIssueID string) ([]*types.Issue, error) {
	candidateSet := make(map[string]bool)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id FROM %s
			WHERE %s AND type = 'blocks'
		`, depTable, depTargetEquals("")), closedIssueID)
		if err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
				continue
			}
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

	candidateIDs := make([]string, 0, len(candidateSet))
	for id := range candidateSet {
		candidateIDs = append(candidateIDs, id)
	}
	sort.Strings(candidateIDs)

	candidateStatusByID, err := loadStatusByIDInTx(ctx, tx, candidateIDs)
	if err != nil {
		return nil, fmt.Errorf("check candidate status: %w", err)
	}
	activeCandidateIDs := candidateIDs[:0]
	for _, id := range candidateIDs {
		status, ok := candidateStatusByID[id]
		if !ok || status == types.StatusClosed || status == types.StatusPinned {
			continue
		}
		activeCandidateIDs = append(activeCandidateIDs, id)
	}
	candidateIDs = activeCandidateIDs
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	stillBlocked := make(map[string]bool)
	for start := 0; start < len(candidateIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(candidateIDs) {
			end = len(candidateIDs)
		}
		batch := candidateIDs[start:end]
		placeholders, batchArgs := buildSQLInClause(batch)

		remainingByCandidate := make(map[string][]string, len(batch))
		remainingBlockerSet := make(map[string]struct{})
		for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
			//nolint:gosec // G201: depTable is hardcoded.
			depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
				SELECT issue_id, %s AS depends_on_id FROM %s
				WHERE issue_id IN (%s) AND type = 'blocks' AND %s != ?
			`, DepTargetExpr, depTable, placeholders, DepTargetExpr), append(batchArgs, closedIssueID)...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("check remaining blockers from %s: %w", depTable, err)
			}
			for depRows.Next() {
				var candidateID, blockerID string
				if err := depRows.Scan(&candidateID, &blockerID); err != nil {
					_ = depRows.Close()
					return nil, fmt.Errorf("scan remaining blocker: %w", err)
				}
				remainingByCandidate[candidateID] = append(remainingByCandidate[candidateID], blockerID)
				remainingBlockerSet[blockerID] = struct{}{}
			}
			_ = depRows.Close()
			if err := depRows.Err(); err != nil {
				return nil, fmt.Errorf("remaining blocker rows from %s: %w", depTable, err)
			}
		}

		remainingBlockerIDs := make([]string, 0, len(remainingBlockerSet))
		for blockerID := range remainingBlockerSet {
			remainingBlockerIDs = append(remainingBlockerIDs, blockerID)
		}
		sort.Strings(remainingBlockerIDs)
		statusByID, err := loadStatusByIDInTx(ctx, tx, remainingBlockerIDs)
		if err != nil {
			return nil, fmt.Errorf("check remaining blocker status: %w", err)
		}
		for candidateID, blockerIDs := range remainingByCandidate {
			for _, blockerID := range blockerIDs {
				status, ok := statusByID[blockerID]
				if ok && status != types.StatusClosed && status != types.StatusPinned {
					stillBlocked[candidateID] = true
					break
				}
			}
		}
	}

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
//
//nolint:gosec // G201: table names are hardcoded constants.
func IsBlockedInTx(ctx context.Context, tx DBTX, issueID string) (bool, []string, error) {
	var blocked bool
	found := false
	for _, table := range []string{"issues", "wisps"} {
		var b int
		//nolint:gosec // G201: table is a hardcoded "issues" or "wisps".
		err := tx.QueryRowContext(ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", issueID).Scan(&b)
		if err == nil {
			blocked = b != 0
			found = true
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return false, nil, fmt.Errorf("read is_blocked from %s: %w", table, err)
		}
	}
	if !found || !blocked {
		return false, nil, nil
	}

	type depEdge struct {
		dependsOnID, depType string
	}
	var edges []depEdge
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT %s AS depends_on_id, type FROM %s
			WHERE issue_id = ? AND type IN ('blocks', 'waits-for', 'conditional-blocks')
		`, DepTargetExpr, depTable), issueID)
		if err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
				continue
			}
			return false, nil, fmt.Errorf("check blockers from %s: %w", depTable, err)
		}
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
			return false, nil, fmt.Errorf("blocker edge rows from %s: %w", depTable, err)
		}
	}

	if len(edges) == 0 {
		return true, nil, nil
	}

	blockerIDs := make([]string, 0, len(edges))
	for _, e := range edges {
		blockerIDs = append(blockerIDs, e.dependsOnID)
	}
	statusByID, err := loadStatusByIDInTx(ctx, tx, blockerIDs)
	if err != nil {
		return false, nil, fmt.Errorf("check blocker status: %w", err)
	}
	var blockers []string
	for _, e := range edges {
		status, ok := statusByID[e.dependsOnID]
		if !ok {
			continue
		}
		if status == types.StatusClosed || status == types.StatusPinned {
			continue
		}
		if e.depType != "blocks" {
			blockers = append(blockers, e.dependsOnID+" ("+e.depType+")")
		} else {
			blockers = append(blockers, e.dependsOnID)
		}
	}

	return true, blockers, nil
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
