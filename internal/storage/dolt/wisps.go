package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Wisp table routing helpers.
// Wisps are stored in dolt_ignored tables (wisps, wisp_labels, wisp_dependencies,
// wisp_events, wisp_comments) to avoid Dolt history bloat. All operations use the
// same Dolt SQL connection — no separate store or transaction routing needed.

// insertIssueIntoTable delegates to the shared issueops.InsertIssueIntoTable.
func insertIssueIntoTable(ctx context.Context, tx *sql.Tx, table string, issue *types.Issue) error {
	return issueops.InsertIssueIntoTable(ctx, tx, table, issue)
}

// scanIssueFromTable scans a single issue from the specified table.
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func scanIssueFromTable(ctx context.Context, db *sql.DB, table, id string) (*types.Issue, error) {
	row := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE id = ?
	`, issueSelectColumns, table), id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue from %s: %w", table, err)
	}
	return issue, nil
}

// generateIssueIDInTable generates a unique ID, checking for collisions
// in the specified table. Supports counter mode for non-ephemeral issues.
//
//nolint:gosec // G201: table is a hardcoded constant
func generateIssueIDInTable(ctx context.Context, tx *sql.Tx, table, prefix string, issue *types.Issue, actor string) (string, error) {
	// Counter mode only applies to the issues table (not wisps).
	if table == "issues" {
		counterMode, err := isCounterModeTx(ctx, tx)
		if err != nil {
			return "", err
		}
		if counterMode {
			return nextCounterIDTx(ctx, tx, prefix)
		}
	}

	baseLength := getAdaptiveIDLengthFromTable(ctx, tx, table, prefix)

	var err error
	maxLength := 8
	if baseLength > maxLength {
		baseLength = maxLength
	}

	for length := baseLength; length <= maxLength; length++ {
		for nonce := 0; nonce < 10; nonce++ {
			candidate := generateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)

			var count int
			err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, table), candidate).Scan(&count) //nolint:gosec // G201
			if err != nil {
				return "", fmt.Errorf("failed to check for ID collision: %w", err)
			}

			if count == 0 {
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("failed to generate unique ID after trying lengths %d-%d with 10 nonces each", baseLength, maxLength)
}

// getAdaptiveIDLengthFromTable returns the adaptive ID length based on table size.
//
//nolint:gosec // G201: table is a hardcoded constant
func getAdaptiveIDLengthFromTable(ctx context.Context, tx *sql.Tx, table, prefix string) int {
	var count int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id LIKE ?`, table), prefix+"%").Scan(&count); err != nil {
		return 4 // Default for wisps (small tables)
	}

	switch {
	case count < 100:
		return 4
	case count < 1000:
		return 5
	case count < 10000:
		return 6
	default:
		return 7
	}
}

// insertIssueTxIntoTable is the transaction-context version for inserting into a named table.
// Delegates to insertIssueIntoTable to ensure all columns are written.
func insertIssueTxIntoTable(ctx context.Context, tx *sql.Tx, table string, issue *types.Issue) error {
	return insertIssueIntoTable(ctx, tx, table, issue)
}

// scanIssueTxFromTable scans a full issue from a named table within a transaction.
// Delegates to the unified scanIssueFrom to ensure all columns are hydrated.
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func scanIssueTxFromTable(ctx context.Context, tx *sql.Tx, table, id string) (*types.Issue, error) {
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s FROM %s WHERE id = ?
	`, issueSelectColumns, table), id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, wrapScanError("scan issue from "+table, err)
	}
	return issue, nil
}

// wispPrefix returns the ID prefix for wisp ID generation.
// Uses IDPrefix if set (e.g., IDPrefix="wisp" → "bd-wisp"), otherwise
// appends "-wisp" to the config prefix (e.g., "bd" → "bd-wisp").
func wispPrefix(configPrefix string, issue *types.Issue) string {
	if issue.PrefixOverride != "" {
		return issue.PrefixOverride
	}
	if issue.IDPrefix != "" {
		return configPrefix + "-" + issue.IDPrefix
	}
	return configPrefix + "-wisp"
}

// getWisp retrieves an issue from the wisps table.
func (s *DoltStore) getWisp(ctx context.Context, id string) (*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	issue, err := scanIssueFromTable(ctx, s.db, "wisps", id)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	labels, err := s.getWispLabels(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp labels: %w", err)
	}
	issue.Labels = labels
	return issue, nil
}

// getWispLabels retrieves labels from the wisp_labels table.
func (s *DoltStore) getWispLabels(ctx context.Context, issueID string) ([]string, error) {
	rows, err := s.queryContext(ctx, `SELECT label FROM wisp_labels WHERE issue_id = ? ORDER BY label`, issueID)
	if err != nil {
		return nil, wrapQueryError("get wisp labels", err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, wrapScanError("scan wisp label", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// updateWisp updates fields on a wisp in the wisps table.
// Delegates SQL work to issueops.UpdateIssueInTx; no Dolt versioning needed
// since wisps live in dolt_ignored tables.
func (s *DoltStore) updateWisp(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor); err != nil {
		return err
	}

	return wrapTransactionError("commit update wisp", tx.Commit())
}

// closeWisp closes a wisp in the wisps table.
// Delegates SQL work to issueops.CloseIssueInTx; no Dolt versioning needed
// since wisps live in dolt_ignored tables.
func (s *DoltStore) closeWisp(ctx context.Context, id string, reason string, actor string, session string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := issueops.CloseIssueInTx(ctx, tx, id, reason, actor, session); err != nil {
		return err
	}

	return wrapTransactionError("commit close wisp", tx.Commit())
}

// deleteWisp permanently removes a wisp and its related data.
func (s *DoltStore) deleteWisp(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete from auxiliary tables
	for _, table := range []string{"wisp_dependencies", "wisp_events", "wisp_comments", "wisp_labels"} {
		if table == "wisp_dependencies" {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id) //nolint:gosec // G201: table is hardcoded
		} else {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id) //nolint:gosec // G201: table is hardcoded
		}
		if err != nil {
			return fmt.Errorf("failed to delete from %s: %w", table, err)
		}
	}

	result, err := tx.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete wisp: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("wisp not found: %s", id)
	}

	return wrapTransactionError("commit delete wisp", tx.Commit())
}

// deleteWispBatch permanently removes multiple wisps using one transaction per
// batch of 200. Committing per-batch keeps each transaction short enough to
// complete within Dolt's writeTimeout (10 s), preventing i/o timeout errors
// when GC-ing hundreds of wisps at once (ff-tqm).
//
// Previously the entire set was wrapped in one mega-transaction; at 631 wisps
// the commit exceeded the driver write timeout and failed with
// "read tcp …: i/o timeout".
//
// Partial cleanup is acceptable: if one batch fails the earlier batches are
// already committed and the next GC run will handle the remainder.
func (s *DoltStore) deleteWispBatch(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	const batchSize = 200
	totalDeleted := 0

	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		deleted, err := s.deleteWispBatchTx(ctx, ids[i:end])
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}

	return totalDeleted, nil
}

// deleteWispBatchTx deletes one batch of wisps inside its own transaction.
// Keeping each transaction to ≤200 wisps (6 DELETE statements) ensures it
// completes well within Dolt's 10 s write timeout.
func (s *DoltStore) deleteWispBatchTx(ctx context.Context, ids []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inClause, args := doltBuildSQLInClause(ids)

	// Delete from wisp_dependencies using two separate queries rather than a
	// single OR condition. An OR across issue_id and depends_on_id forces Dolt
	// to union two index scans in one statement, which is slow enough to trigger
	// the driver's write timeout on large batches (ff-tqm). Two targeted queries
	// each use their own index: PRIMARY KEY for issue_id and
	// idx_wisp_dep_depends for depends_on_id.
	//nolint:gosec // G201: inClause contains only ? markers
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM wisp_dependencies WHERE issue_id IN (%s)", inClause),
		args...); err != nil {
		return 0, fmt.Errorf("failed to batch delete from wisp_dependencies (issue_id): %w", err)
	}
	//nolint:gosec // G201: inClause contains only ? markers
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM wisp_dependencies WHERE depends_on_id IN (%s)", inClause),
		args...); err != nil {
		return 0, fmt.Errorf("failed to batch delete from wisp_dependencies (depends_on_id): %w", err)
	}

	for _, table := range []string{"wisp_events", "wisp_comments", "wisp_labels"} {
		//nolint:gosec // G201: table is a hardcoded constant, inClause contains only ? markers
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE issue_id IN (%s)", table, inClause),
			args...); err != nil {
			return 0, fmt.Errorf("failed to batch delete from %s: %w", table, err)
		}
	}

	// Delete the wisps themselves
	//nolint:gosec // G201: inClause contains only ? markers
	result, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM wisps WHERE id IN (%s)", inClause),
		args...)
	if err != nil {
		return 0, fmt.Errorf("failed to batch delete wisps: %w", err)
	}
	rowsAffected, _ := result.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit batch wisp delete: %w", err)
	}

	return int(rowsAffected), nil
}

// claimWisp atomically claims a wisp.
// Delegates SQL work to issueops.ClaimIssueInTx; no Dolt versioning needed
// since wisps live in dolt_ignored tables.
func (s *DoltStore) claimWisp(ctx context.Context, id string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := issueops.ClaimIssueInTx(ctx, tx, id, actor); err != nil {
		return err
	}

	return wrapTransactionError("commit claim wisp", tx.Commit())
}

// ListWisps returns ephemeral issues matching the filter.
// It always queries the wisps table (Ephemeral=true); callers do not need to set that flag.
func (s *DoltStore) ListWisps(ctx context.Context, filter types.WispFilter) ([]*types.Issue, error) {
	issueFilter := issueops.WispFilterToIssueFilter(filter)
	return s.searchWisps(ctx, "", issueFilter)
}

// searchWisps searches for issues in the wisps table.
func (s *DoltStore) searchWisps(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClauses, args, err := buildIssueFilterClauses(query, filter, wispsFilterTables)
	if err != nil {
		return nil, err
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	//nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	querySQL := fmt.Sprintf(`
		SELECT id FROM wisps
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.queryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search wisps: %w", err)
	}
	defer rows.Close()

	return s.scanWispIDs(ctx, rows)
}

// scanWispIDs collects IDs from rows and fetches full issues from the wisps table.
func (s *DoltStore) scanWispIDs(ctx context.Context, rows *sql.Rows) ([]*types.Issue, error) {
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan wisp id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp IDs", err)
	}
	_ = rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	return s.getWispsByIDs(ctx, ids)
}

// getWispsByIDs retrieves multiple wisps by ID, batching queries to avoid
// oversized IN-clauses that cause slow queries on large databases.
func (s *DoltStore) getWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Fetch wisps in batches to keep IN-clause size bounded.
	var issues []*types.Issue
	issueMap := make(map[string]*types.Issue, len(ids))
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		placeholders, args := doltBuildSQLInClause(batch)

		//nolint:gosec // G201: placeholders contains only ? markers
		querySQL := fmt.Sprintf(`
			SELECT %s
			FROM wisps
			WHERE id IN (%s)
		`, issueSelectColumns, placeholders)

		queryRows, err := s.queryContext(ctx, querySQL, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to get wisps by IDs: %w", err)
		}

		for queryRows.Next() {
			issue, err := scanIssueFrom(queryRows)
			if err != nil {
				_ = queryRows.Close()
				return nil, wrapScanError("scan wisp", err)
			}
			issues = append(issues, issue)
			issueMap[issue.ID] = issue
		}
		if err := queryRows.Err(); err != nil {
			_ = queryRows.Close()
			return nil, wrapQueryError("iterate wisps", err)
		}
		_ = queryRows.Close()
	}

	// Hydrate labels in batches.
	if len(issues) > 0 {
		allIDs := make([]string, len(issues))
		for i, issue := range issues {
			allIDs[i] = issue.ID
		}

		for start := 0; start < len(allIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(allIDs) {
				end = len(allIDs)
			}
			batch := allIDs[start:end]
			placeholders, args := doltBuildSQLInClause(batch)

			//nolint:gosec // G201: placeholders contains only ? markers
			labelSQL := fmt.Sprintf(`
				SELECT issue_id, label FROM wisp_labels
				WHERE issue_id IN (%s)
				ORDER BY issue_id, label
			`, placeholders)

			labelRows, err := s.queryContext(ctx, labelSQL, args...)
			if err != nil {
				return nil, fmt.Errorf("failed to get wisp labels: %w", err)
			}

			for labelRows.Next() {
				var issueID, label string
				if err := labelRows.Scan(&issueID, &label); err != nil {
					_ = labelRows.Close()
					return nil, wrapScanError("scan wisp label", err)
				}
				if issue, ok := issueMap[issueID]; ok {
					issue.Labels = append(issue.Labels, label)
				}
			}
			if err := labelRows.Err(); err != nil {
				_ = labelRows.Close()
				return nil, wrapQueryError("iterate wisp labels", err)
			}
			_ = labelRows.Close()
		}
	}

	return issues, nil
}

// addWispDependency adds a dependency to the wisp_dependencies table.
func (s *DoltStore) addWispDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Cycle detection for blocking dependency types: check if adding this edge
	// would create a cycle. UNIONs both tables to detect cross-table cycles
	// (e.g., wisp A -> permanent B -> wisp A). (bd-xe27)
	if dep.Type == types.DepBlocks {
		var reachable int
		if err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE reachable AS (
				SELECT ? AS node, 0 AS depth
				UNION ALL
				SELECT d.depends_on_id, r.depth + 1
				FROM reachable r
				JOIN (
					SELECT issue_id, depends_on_id FROM dependencies WHERE type = 'blocks'
					UNION ALL
					SELECT issue_id, depends_on_id FROM wisp_dependencies WHERE type = 'blocks'
				) d ON d.issue_id = r.node
				WHERE r.depth < 100
			)
			SELECT COUNT(*) FROM reachable WHERE node = ?
		`, dep.DependsOnID, dep.IssueID).Scan(&reachable); err != nil {
			return fmt.Errorf("failed to check for dependency cycle: %w", err)
		}
		if reachable > 0 {
			return fmt.Errorf("adding dependency would create a cycle")
		}
	}

	// Check for existing dependency to prevent silent type overwrites.
	var existingType string
	err = tx.QueryRowContext(ctx, `
		SELECT type FROM wisp_dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		if existingType == string(dep.Type) {
			// Same type — idempotent; update metadata in case it changed
			if _, err := tx.ExecContext(ctx, `
				UPDATE wisp_dependencies SET metadata = ? WHERE issue_id = ? AND depends_on_id = ?
			`, metadata, dep.IssueID, dep.DependsOnID); err != nil {
				return fmt.Errorf("failed to update wisp dependency metadata: %w", err)
			}
			return wrapTransactionError("commit add wisp dependency", tx.Commit())
		}
		return fmt.Errorf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing wisp dependency: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add wisp dependency: %w", err)
	}

	return wrapTransactionError("commit add wisp dependency", tx.Commit())
}

// getWispDependencies retrieves issues that a wisp depends on.
func (s *DoltStore) getWispDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.queryContext(ctx, `
		SELECT depends_on_id FROM wisp_dependencies WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependencies: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependency", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependencies", err)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return s.GetIssuesByIDs(ctx, ids)
}

// getWispDependents retrieves issues that depend on a wisp.
func (s *DoltStore) getWispDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.queryContext(ctx, `
		SELECT issue_id FROM wisp_dependencies WHERE depends_on_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependents: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependent", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependents", err)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return s.GetIssuesByIDs(ctx, ids)
}

// getWispDependenciesWithMetadata returns wisp dependencies with metadata.
func (s *DoltStore) getWispDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.queryContext(ctx, `
		SELECT depends_on_id, type FROM wisp_dependencies WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependencies with metadata: %w", err)
	}

	type depMeta struct {
		depID, depType string
	}
	var deps []depMeta
	for rows.Next() {
		var depID, depType string
		if err := rows.Scan(&depID, &depType); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependency metadata", err)
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependencies", err)
	}

	if len(deps) == 0 {
		return nil, nil
	}

	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// FindWispDependentsRecursive finds all wisp dependents of the given IDs,
// recursively. Uses batched IN-clause queries against wisp_dependencies for
// efficiency. Returns the set of all discovered dependent IDs (excluding the
// input IDs). Capped at maxRecursiveResults to prevent runaway traversal.
func (s *DoltStore) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	var result map[string]bool
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.FindWispDependentsRecursiveInTx(ctx, tx, ids)
		return err
	})
	return result, err
}
