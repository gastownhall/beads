package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// GetReadyWorkInTx returns issues that are ready to work on (not blocked).
// computeBlockedFn is the caller's function for computing blocked IDs (since
// the DoltStore and EmbeddedDoltStore have different caching strategies).
//
//nolint:gosec // G201: whereSQL/orderBySQL built from hardcoded strings and ? placeholders
func GetReadyWorkInTx(
	ctx context.Context,
	tx *sql.Tx,
	filter types.WorkFilter,
	computeBlockedFn func(ctx context.Context, tx *sql.Tx, includeWisps bool) ([]string, error),
) ([]*types.Issue, error) {
	// Status filtering: default to open OR in_progress.
	var statusClause string
	if filter.Status != "" {
		statusClause = "status = ?"
	} else {
		statusClause = "status IN ('open', 'in_progress')"
	}
	whereClauses := []string{
		statusClause,
		"(pinned = 0 OR pinned IS NULL)",
	}
	if !filter.IncludeEphemeral {
		whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
	}
	var args []interface{}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	// Use subquery for type filter to prevent join issues.
	if filter.Type != "" {
		whereClauses = append(whereClauses, "id IN (SELECT id FROM issues WHERE issue_type = ?)")
		args = append(args, filter.Type)
	} else {
		excludeTypes := []string{"merge-request", "gate", "molecule", "message", "agent", "role", "rig"}
		placeholders := make([]string, len(excludeTypes))
		for i, t := range excludeTypes {
			placeholders[i] = "?"
			args = append(args, t)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM issues WHERE issue_type NOT IN (%s))", strings.Join(placeholders, ",")))
	}
	// Unassigned takes precedence over Assignee filter.
	if filter.Unassigned {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	} else if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}
	// Exclude future-deferred issues unless IncludeDeferred is set.
	if !filter.IncludeDeferred {
		whereClauses = append(whereClauses, "(defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())")
	}
	// Exclude children of future-deferred parents.
	if !filter.IncludeDeferred {
		deferredChildIDs, dcErr := getChildrenOfDeferredParentsInTx(ctx, tx)
		if dcErr == nil && len(deferredChildIDs) > 0 {
			for start := 0; start < len(deferredChildIDs); start += queryBatchSize {
				end := start + queryBatchSize
				if end > len(deferredChildIDs) {
					end = len(deferredChildIDs)
				}
				placeholders, batchArgs := buildSQLInClause(deferredChildIDs[start:end])
				args = append(args, batchArgs...)
				whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (%s)", placeholders))
			}
		}
	}
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}
	if len(filter.ExcludeLabels) > 0 {
		placeholders := make([]string, len(filter.ExcludeLabels))
		for i, label := range filter.ExcludeLabels {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM labels WHERE label IN (%s))", strings.Join(placeholders, ", ")))
	}
	// Parent filtering.
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR (id LIKE CONCAT(?, '.%') AND id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')))")
		args = append(args, parentID, parentID)
	}

	// Molecule filtering: filter to direct children of the specified molecule.
	if filter.MoleculeID != "" {
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR (id LIKE CONCAT(?, '.%') AND id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')))")
		args = append(args, filter.MoleculeID, filter.MoleculeID)
	}

	// Metadata existence check.
	if filter.HasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(filter.HasMetadataKey); err != nil {
			return nil, err
		}
		whereClauses = append(whereClauses, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, "$."+filter.HasMetadataKey)
	}

	// Metadata field equality filters.
	if len(filter.MetadataFields) > 0 {
		metaKeys := make([]string, 0, len(filter.MetadataFields))
		for k := range filter.MetadataFields {
			metaKeys = append(metaKeys, k)
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, err
			}
			whereClauses = append(whereClauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, storage.JSONMetadataPath(k), filter.MetadataFields[k])
		}
	}

	// Exclude blocked issues.
	blockedIDs, err := computeBlockedFn(ctx, tx, filter.IncludeEphemeral)
	if err == nil && len(blockedIDs) > 0 {
		// Also exclude children of blocked parents.
		childrenOfBlocked, childErr := getChildrenOfIssuesInTx(ctx, tx, blockedIDs)
		if childErr == nil {
			blockedIDs = append(blockedIDs, childrenOfBlocked...)
		}

		for start := 0; start < len(blockedIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(blockedIDs) {
				end = len(blockedIDs)
			}
			placeholders, batchArgs := buildSQLInClause(blockedIDs[start:end])
			args = append(args, batchArgs...)
			whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (%s)", placeholders))
		}
	}

	whereSQL := "WHERE " + strings.Join(whereClauses, " AND ")

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	// Build ORDER BY clause based on SortPolicy.
	var orderBySQL string
	switch filter.SortPolicy {
	case types.SortPolicyOldest:
		orderBySQL = "ORDER BY created_at ASC, id ASC"
	case types.SortPolicyPriority:
		orderBySQL = "ORDER BY priority ASC, created_at DESC, id ASC"
	case types.SortPolicyHybrid, "":
		orderBySQL = `ORDER BY
			CASE WHEN created_at >= DATE_SUB(NOW(), INTERVAL 48 HOUR) THEN 0 ELSE 1 END ASC,
			CASE WHEN created_at >= DATE_SUB(NOW(), INTERVAL 48 HOUR) THEN priority ELSE 999 END ASC,
			created_at ASC, id ASC`
	default:
		orderBySQL = "ORDER BY priority ASC, created_at DESC, id ASC"
	}

	//nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	query := fmt.Sprintf(`
		SELECT id FROM issues
		%s
		%s
		%s
	`, whereSQL, orderBySQL, limitSQL)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}

	// Collect IDs in order.
	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("get ready work: scan id: %w", err)
		}
		issueIDs = append(issueIDs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get ready work: rows: %w", err)
	}

	// Batch-fetch full issues preserving order.
	issues, err := GetIssuesByIDsInTx(ctx, tx, issueIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("get ready work: fetch issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}
	ordered := make([]*types.Issue, 0, len(issueIDs))
	for _, id := range issueIDs {
		if iss, ok := issueMap[id]; ok {
			ordered = append(ordered, iss)
		}
	}

	// When IncludeEphemeral is set, also query the wisps table.
	if filter.IncludeEphemeral {
		ephTrue := true
		wispFilter := types.IssueFilter{Limit: filter.Limit, Ephemeral: &ephTrue}
		if filter.Status != "" {
			s := filter.Status
			wispFilter.Status = &s
		}
		wisps, wErr := SearchIssuesInTx(ctx, tx, "", wispFilter)
		if wErr == nil {
			ordered = append(ordered, wisps...)
		}
	}

	return ordered, nil
}

// getChildrenOfDeferredParentsInTx returns IDs of issues whose parent has a
// future defer_until. Works within an existing transaction.
func getChildrenOfDeferredParentsInTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	// Step 1: Get IDs of issues with future defer_until.
	deferredRows, err := tx.QueryContext(ctx, `
		SELECT id FROM issues
		WHERE defer_until IS NOT NULL AND defer_until > UTC_TIMESTAMP()
	`)
	if err != nil {
		return nil, fmt.Errorf("deferred parents: get deferred issues: %w", err)
	}
	var deferredIDs []string
	for deferredRows.Next() {
		var id string
		if err := deferredRows.Scan(&id); err != nil {
			_ = deferredRows.Close()
			return nil, fmt.Errorf("deferred parents: scan deferred issue: %w", err)
		}
		deferredIDs = append(deferredIDs, id)
	}
	_ = deferredRows.Close()
	if err := deferredRows.Err(); err != nil {
		return nil, fmt.Errorf("deferred parents: deferred rows: %w", err)
	}
	if len(deferredIDs) == 0 {
		return nil, nil
	}

	// Step 2: Get children of those deferred parents.
	return getChildrenOfIssuesInTx(ctx, tx, deferredIDs)
}

// getChildrenOfIssuesInTx returns IDs of direct children (parent-child deps)
// of the given issue IDs. Scans both dependencies and wisp_dependencies tables.
//
//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
func getChildrenOfIssuesInTx(ctx context.Context, tx *sql.Tx, parentIDs []string) ([]string, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	var children []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		for start := 0; start < len(parentIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(parentIDs) {
				end = len(parentIDs)
			}
			placeholders, args := buildSQLInClause(parentIDs[start:end])

			query := fmt.Sprintf(`
				SELECT issue_id FROM %s
				WHERE type = 'parent-child' AND depends_on_id IN (%s)
			`, depTable, placeholders)
			rows, err := tx.QueryContext(ctx, query, args...)
			if err != nil {
				// wisp_dependencies table may not exist on pre-migration databases.
				if depTable == "wisp_dependencies" {
					break
				}
				return nil, fmt.Errorf("get children of issues from %s: %w", depTable, err)
			}
			for rows.Next() {
				var childID string
				if err := rows.Scan(&childID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("get children of issues: scan: %w", err)
				}
				children = append(children, childID)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("get children of issues: rows from %s: %w", depTable, err)
			}
		}
	}
	return children, nil
}

// buildSQLInClause builds a parameterized IN clause from a slice of IDs.
func buildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
