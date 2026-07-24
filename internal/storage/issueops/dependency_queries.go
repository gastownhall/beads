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

// GetDependentRecordsForIssuesInTx returns raw dependency rows keyed by TARGET
// id: for each id in targetIDs, the rows whose target is that id — its INCOMING
// edges, i.e. its dependents — spanning BOTH the durable and wisp dependency
// tables and applying NO type filter or visibility policy (the caller filters
// at hydration). It is the batched, target-keyed mirror of the source-keyed
// GetDependencyRecordsForIssuesInTx: one query per table per batch of
// queryBatchSize target ids, so cost is O(1 + N/queryBatchSize) round-trips per
// table rather than O(N) — the whole-page read that lets a caller render every
// id's inbound `blocks` edges without a per-id fan-out.
//
// A target is matched by the coalesced target expression (DepTargetExpr) — the
// same predicate the batched source-keyed blocks/counts reads use — so an id
// that appears in any of the three typed target columns resolves; each returned
// row's DependsOnID is that resolved target, which is the map key. Rows are
// de-duped by their primary id across the two tables exactly as
// GetDependentRecordsInTx does: a wisp promoted to durable carries ONE depid in
// both tables, so the durable table is scanned first and a repeat id from the
// wisp table is skipped — the edge is returned once, as its authoritative
// durable row.
func GetDependentRecordsForIssuesInTx(ctx context.Context, tx DBTX, targetIDs []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency)
	if len(targetIDs) == 0 {
		return result, nil
	}
	// De-dup by row id across the two tables, preferring the durable copy scanned
	// first — same cross-table collision handling as GetDependentRecordsInTx.
	seen := make(map[string]bool)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		if err := getDependentRecordsIntoFromTable(ctx, tx, depTable, targetIDs, seen, result); err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
				continue
			}
			return nil, err
		}
	}
	return result, nil
}

//nolint:gosec // G201: depTable is "dependencies" or "wisp_dependencies" (hardcoded by caller); placeholders are ? only.
func getDependentRecordsIntoFromTable(ctx context.Context, tx DBTX, depTable string, targetIDs []string, seen map[string]bool, result map[string][]*types.Dependency) error {
	for start := 0; start < len(targetIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(targetIDs) {
			end = len(targetIDs)
		}
		batch := targetIDs[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT id, issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
			 FROM %s WHERE %s ORDER BY %s`,
			DepTargetExpr, depTable, depTargetIn("", strings.Join(placeholders, ",")), DepTargetExpr), args...)
		if err != nil {
			return fmt.Errorf("get dependent records from %s: %w", depTable, err)
		}
		for rows.Next() {
			dep, scanErr := scanDependentRow(rows)
			if scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("get dependent records: scan: %w", scanErr)
			}
			// De-dup by row id (depid): the wisp copy of a promoted edge carries
			// the same id as the durable copy scanned first, so skip the repeat.
			if seen[dep.ID] {
				continue
			}
			seen[dep.ID] = true
			result[dep.DependsOnID] = append(result[dep.DependsOnID], dep)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("get dependent records: rows: %w", err)
		}
	}
	return nil
}

// Target-keyed dependents-read bounds. A raw read has no consumer to apply a
// page size, so it clamps its own (default when limit <= 0, hard cap otherwise).
const (
	defaultDependentRecordsLimit = 100
	maxDependentRecordsLimit     = 500
)

// GetDependentRecordsInTx returns raw dependency rows whose TARGET is targetID
// — the edges pointing AT targetID — from both the permanent and wisp
// dependency tables. Unlike GetDependents/GetDependentsWithMetadata it does
// NOT join or hydrate the source issues, so edges from dangling, cross-project,
// or wisp sources are returned as raw rows rather than dropped. RAW READ: it
// spans BOTH the `dependencies` and `wisp_dependencies` tables and applies no
// visibility policy — filtering (e.g. a group-membership visibility rule) is
// the caller's job, applied at hydration.
//
// When depType is non-empty only rows of that dependency type are returned
// ("" = all types). Results are ordered by the dependency row's primary id ASC
// and bounded by limit (see the clamp constants). afterID is a keyset
// continuation over that id order: "" starts at the beginning, otherwise only
// rows with id > afterID are returned.
//
// CURSOR KEY: the dependency row's own id (depid.New(issue_id, target), a
// UUIDv5 that is stable and globally unique across both tables) — NOT the source
// issue_id. issue_id is not a total key for a fixed target: a source can appear
// across the two scanned tables, so paging on it could drop or duplicate rows.
// Paging on the unique row id is total, so each source's inbound edge is
// returned exactly once. The target match is a sargable per-column OR over the
// three typed columns (seeks idx_dep_*_target / the type composites) rather than
// a COALESCE wrapper.
func GetDependentRecordsInTx(ctx context.Context, tx DBTX, targetID, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	if limit <= 0 {
		limit = defaultDependentRecordsLimit
	}
	if limit > maxDependentRecordsLimit {
		limit = maxDependentRecordsLimit
	}

	var all []*types.Dependency
	seen := make(map[string]bool)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := queryDependentRecordsFromTable(ctx, tx, depTable, targetID, depType, limit, afterID)
		if err != nil {
			if optionalBlockedTable(depTable) && isTableNotExistError(err) {
				continue
			}
			return nil, err
		}
		// De-dup by row id across the two tables. depid.New keys the id on
		// (issue_id, target) and deliberately omits the table, so the SAME edge
		// that lands in BOTH tables — a wisp promoted to durable, or two clones
		// merged — carries ONE id in both. Appending the wisp copy would put a
		// duplicate id in the page and, at a page boundary, drop it on the next
		// `id > afterID`. We iterate durable first and skip an already-seen id,
		// so a colliding edge is returned exactly once as its DURABLE row (the
		// authoritative, non-ephemeral copy). Within a single table id is a
		// PRIMARY KEY, so intra-table rows never collide.
		for _, r := range rows {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			all = append(all, r)
		}
	}

	// Merge the de-duped per-table pages into one total order by row id. Each
	// table returned its first `limit` rows with id > afterID, so the global
	// first `limit` DISTINCT ids > afterID are all present; sorting by the
	// globally unique id and truncating yields exactly that page — no drop, no
	// dup, and stable across a cross-table id collision.
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

//nolint:gosec // G201: depTable is a hardcoded constant; targetID/depType/afterID are bound as parameters.
func queryDependentRecordsFromTable(ctx context.Context, tx DBTX, depTable, targetID, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	query := fmt.Sprintf(`
		SELECT id, issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM %s
		WHERE %s`, DepTargetExpr, depTable, depTargetEqualsOr())
	args := []any{targetID, targetID, targetID}
	if depType != "" {
		query += " AND type = ?"
		args = append(args, depType)
	}
	if afterID != "" {
		query += " AND id > ?"
		args = append(args, afterID)
	}
	query += fmt.Sprintf(" ORDER BY id ASC LIMIT %d", limit)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get dependent records from %s: %w", depTable, err)
	}
	defer rows.Close()

	var deps []*types.Dependency
	for rows.Next() {
		dep, scanErr := scanDependentRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("get dependent records: scan: %w", scanErr)
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

// scanDependentRow scans a dependents row that INCLUDES the row id (the keyset
// cursor). The shared scanDependencyRow does not select id, and adding it there
// would ripple through every source-keyed read, so the target-keyed read owns
// this variant.
func scanDependentRow(rows *sql.Rows) (*types.Dependency, error) {
	var dep types.Dependency
	var createdAt sql.NullTime
	var metadata, threadID sql.NullString

	if err := rows.Scan(&dep.ID, &dep.IssueID, &dep.DependsOnID, &dep.Type, &createdAt, &dep.CreatedBy, &metadata, &threadID); err != nil {
		return nil, fmt.Errorf("scan dependent: %w", err)
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

// CountDependentRecordsInTx returns the number of DISTINCT inbound edges of
// targetID, applying the same sargable target predicate and optional depType
// filter as GetDependentRecordsInTx but no keyset/limit. Callers that want a
// true total membership count need it without paging to exhaustion. Like the
// paged read it is a RAW count spanning both tables; the caller applies any
// visibility policy separately.
//
// It must agree with GetDependentRecordsInTx's durable-preferred de-dup: an edge
// present in BOTH tables (same depid) is ONE row in the page, so the count is
// every durable row PLUS every wisp row whose depid is not already a durable row
// for the same target/type. Summing two raw COUNT(*)s would over-count that edge
// and exceed the distinct keyset row count.
func CountDependentRecordsInTx(ctx context.Context, tx DBTX, targetID, depType string) (int, error) {
	durable, err := countDependentRecordsFromTable(ctx, tx, "dependencies", targetID, depType)
	if err != nil {
		return 0, err
	}
	wispExtra, err := countWispDependentsNotInDurableInTx(ctx, tx, targetID, depType)
	if err != nil {
		// wisp_dependencies absent: the durable count is the whole answer.
		if isTableNotExistError(err) {
			return durable, nil
		}
		return 0, err
	}
	return durable + wispExtra, nil
}

// countWispDependentsNotInDurableInTx counts wisp_dependencies rows whose target
// is targetID (optional depType) but whose depid is NOT present in the durable
// dependencies table for the same target/type — the wisp-ONLY inbound edges.
// Colliding edges (present in both tables) are counted on the durable side, so
// this is the exact complement that makes the total distinct-by-id. The NOT IN
// subquery is uncorrelated and bounded by the target's durable inbound-edge
// count; both arms use the sargable per-column OR target predicate.
//
//nolint:gosec // G201: table names are hardcoded constants; targetID/depType are bound as parameters.
func countWispDependentsNotInDurableInTx(ctx context.Context, tx DBTX, targetID, depType string) (int, error) {
	wispWhere := depTargetEqualsOr()
	durableWhere := depTargetEqualsOr()
	args := []any{targetID, targetID, targetID}
	if depType != "" {
		wispWhere += " AND type = ?"
		args = append(args, depType)
	}
	args = append(args, targetID, targetID, targetID)
	if depType != "" {
		durableWhere += " AND type = ?"
		args = append(args, depType)
	}
	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisp_dependencies WHERE %s AND id NOT IN (SELECT id FROM dependencies WHERE %s)",
		wispWhere, durableWhere)
	var n int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count wisp-only dependent records: %w", err)
	}
	return n, nil
}

//nolint:gosec // G201: depTable is a hardcoded constant; targetID/depType are bound as parameters.
func countDependentRecordsFromTable(ctx context.Context, tx DBTX, depTable, targetID, depType string) (int, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", depTable, depTargetEqualsOr())
	args := []any{targetID, targetID, targetID}
	if depType != "" {
		query += " AND type = ?"
		args = append(args, depType)
	}
	var n int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count dependent records from %s: %w", depTable, err)
	}
	return n, nil
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

// IsBlockedBatchInTx returns the denormalized, TRANSITIVE is_blocked flag for
// each of ids in one batched read — the same value IsBlockedInTx returns per id,
// without the per-row blocker-list recompute. It reads the stored is_blocked
// column (SELECT id, is_blocked FROM {issues,wisps} WHERE id IN (...)), batched
// at queryBatchSize, so it reflects inherited/ancestor blockedness (a child of a
// blocked parent has is_blocked=1 with no direct blocking edge of its own) with
// no graph walk. ids missing from both tables are absent from the map (callers
// treat absent as not-blocked). On a cross-table id collision the ISSUES row wins,
// matching IsBlockedInTx exactly: the single read scans issues→wisps and breaks on
// the first table that has the id, so the batch keeps the first-seen (issues) value
// and skips any later (wisps) duplicate. The two reads share the is_blocked field,
// so they must resolve a collision identically — this is a data anomaly, but the
// single and batch reads would otherwise disagree on the same stored flag.
func IsBlockedBatchInTx(ctx context.Context, tx DBTX, ids []string) (map[string]bool, error) {
	blocked := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return blocked, nil
	}
	// De-dup by id across the two tables, keeping the first-seen (issues) value so
	// a cross-table collision resolves ISSUES-win, exactly as IsBlockedInTx does
	// (see above). The wisps table is optional, so a missing one is skipped —
	// same two-table read shape as GetDependentRecordsForIssuesInTx.
	seen := make(map[string]bool, len(ids))
	for _, table := range []string{"issues", "wisps"} {
		if err := readIsBlockedIntoFromTable(ctx, tx, table, ids, seen, blocked); err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return nil, err
		}
	}
	return blocked, nil
}

//nolint:gosec // G201: table is a hardcoded "issues" or "wisps"; placeholders are ? only.
func readIsBlockedIntoFromTable(ctx context.Context, tx DBTX, table string, ids []string, seen, blocked map[string]bool) error {
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			"SELECT id, is_blocked FROM %s WHERE id IN (%s)", table, placeholders), args...)
		if err != nil {
			return fmt.Errorf("read is_blocked from %s: %w", table, err)
		}
		for rows.Next() {
			var id string
			var b int
			if err := rows.Scan(&id, &b); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan is_blocked from %s: %w", table, err)
			}
			// Keep the first-seen (issues) value and skip any later (wisps)
			// duplicate, so the batch is_blocked matches per-row IsBlocked.
			if seen[id] {
				continue
			}
			seen[id] = true
			blocked[id] = b != 0
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("is_blocked rows from %s: %w", table, err)
		}
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
