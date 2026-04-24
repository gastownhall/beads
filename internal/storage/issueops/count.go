package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// groupByColumnMap is the hardcoded mapping from the public CountIssuesGroupedBy
// field name to its SQL column. Any future field additions go here; callers never
// interpolate user-supplied strings into SQL. Per be-nu4 §4.D1 and §7.
var groupByColumnMap = map[string]string{
	"status":     "status",
	"priority":   "priority",
	"issue_type": "issue_type",
	"assignee":   "assignee",
	"label":      "label", // sentinel — label grouping takes the two-phase path
}

// groupByAllowedFields is the error-message-safe ordered list of accepted
// field values. Kept as a slice so the error string is stable.
var groupByAllowedFields = []string{"status", "priority", "issue_type", "assignee", "label"}

// CountIssuesInTx returns the number of issues matching filter within an
// existing transaction. Uses the shared BuildIssueFilterClauses so filter
// semantics match SearchIssues exactly — parity at 1K rows is a hard gate
// (be-nu4 §11.7).
//
// Wisp admission mirrors SearchIssuesInTx: Ephemeral=true → wisps-only with
// fall-through to issues if wisps are missing or empty; Ephemeral=nil →
// issues count plus wisps count; Ephemeral=false → issues only.
func CountIssuesInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter) (int, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		wispCount, err := countTableInTx(ctx, tx, filter, WispsFilterTables)
		if err != nil && !isTableNotExistError(err) {
			return 0, fmt.Errorf("count wisps (ephemeral filter): %w", err)
		}
		if wispCount > 0 {
			return wispCount, nil
		}
		// Fall through: wisps table missing or empty; behave like SearchIssuesInTx.
	}

	count, err := countTableInTx(ctx, tx, filter, IssuesFilterTables)
	if err != nil {
		return 0, fmt.Errorf("count issues: %w", err)
	}

	if filter.Ephemeral == nil {
		wispCount, wispErr := countTableInTx(ctx, tx, filter, WispsFilterTables)
		if wispErr != nil && !isTableNotExistError(wispErr) {
			return 0, fmt.Errorf("count wisps (merge): %w", wispErr)
		}
		count += wispCount
	}

	return count, nil
}

// countTableInTx runs SELECT COUNT(*) against a specific table set with the
// shared filter clauses.
func countTableInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, tables FilterTables) (int, error) {
	whereClauses, args, err := BuildIssueFilterClauses("", filter, tables)
	if err != nil {
		return 0, err
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	//nolint:gosec // G201: tables.Main is hardcoded; whereSQL contains only parameterized comparisons.
	querySQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s %s`, tables.Main, whereSQL)

	var count int
	if err := tx.QueryRowContext(ctx, querySQL, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", tables.Main, err)
	}
	return count, nil
}

// CountIssuesGroupedByInTx returns per-group counts for the given field.
// field must be one of status | priority | issue_type | assignee | label;
// any other value is rejected with a named-allowlist error (be-nu4.1 §3).
//
// Non-label fields use SQL GROUP BY on the single column, unioned across
// issues and wisps when the filter admits both. Label grouping is two-phase:
// it fetches matching IDs then bulk-hydrates labels via GetLabelsForIssuesInTx
// so wisp/permanent labels are routed to the correct table. Issues with no
// labels contribute to the "" bucket; callers that render an "(no labels)"
// label map it at the CLI layer.
func CountIssuesGroupedByInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, field string) (map[string]int, error) {
	column, ok := groupByColumnMap[field]
	if !ok {
		return nil, fmt.Errorf("invalid group-by field %q: must be one of %s",
			field, strings.Join(groupByAllowedFields, ", "))
	}

	if field == "label" {
		return countByLabelInTx(ctx, tx, filter)
	}

	result := make(map[string]int)

	merge := func(tables FilterTables) error {
		m, err := groupByColumnSingleTableInTx(ctx, tx, filter, tables, column)
		if err != nil {
			return err
		}
		for k, v := range m {
			result[k] += v
		}
		return nil
	}

	if filter.Ephemeral != nil && *filter.Ephemeral {
		if err := merge(WispsFilterTables); err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("count wisps group-by %s (ephemeral filter): %w", field, err)
		}
		if len(result) > 0 {
			return result, nil
		}
		// Fall through: mirror SearchIssuesInTx wisp-miss behavior.
	}

	if err := merge(IssuesFilterTables); err != nil {
		return nil, fmt.Errorf("count issues group-by %s: %w", field, err)
	}

	if filter.Ephemeral == nil {
		if err := merge(WispsFilterTables); err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("count wisps group-by %s (merge): %w", field, err)
		}
	}

	return result, nil
}

// groupByColumnSingleTableInTx runs SELECT <col>, COUNT(*) GROUP BY <col>
// against one table. COALESCE collapses NULL and ” into the same bucket for
// nullable columns (assignee) so "no assignee" surfaces as "" regardless of
// storage representation.
func groupByColumnSingleTableInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, tables FilterTables, column string) (map[string]int, error) {
	whereClauses, args, err := BuildIssueFilterClauses("", filter, tables)
	if err != nil {
		return nil, err
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// assignee is the only nullable grouping column; status/priority/issue_type
	// are NOT NULL in the schema.
	colExpr := column
	if column == "assignee" {
		colExpr = "COALESCE(assignee, '')"
	}

	//nolint:gosec // G201: column is chosen from groupByColumnMap allowlist; tables.Main is hardcoded.
	querySQL := fmt.Sprintf(`SELECT %s AS grp, COUNT(*) FROM %s %s GROUP BY grp`,
		colExpr, tables.Main, whereSQL)

	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("group-by %s %s: %w", column, tables.Main, err)
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var key sql.NullString
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, fmt.Errorf("group-by %s %s: scan: %w", column, tables.Main, err)
		}
		k := ""
		if key.Valid {
			k = key.String
		}
		result[k] += count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("group-by %s %s: rows: %w", column, tables.Main, err)
	}
	return result, nil
}

// countByLabelInTx is the two-phase label grouping path: it resolves matching
// IDs across issues + wisps per wisp-admission, then bulk-hydrates labels via
// GetLabelsForIssuesInTx. An issue with N labels contributes to N groups
// (matches the Go-side semantics in cmd/bd/count.go pre-D1). An issue with
// zero labels contributes to the "" bucket.
func countByLabelInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter) (map[string]int, error) {
	var ids []string

	if filter.Ephemeral != nil && *filter.Ephemeral {
		wispIDs, err := filteredIDsInTx(ctx, tx, filter, WispsFilterTables)
		if err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("count by label: wisps ids: %w", err)
		}
		if len(wispIDs) > 0 {
			return labelCountsForIDsInTx(ctx, tx, wispIDs)
		}
		// Fall through: wisps empty or table missing — mirror SearchIssuesInTx.
	}

	issueIDs, err := filteredIDsInTx(ctx, tx, filter, IssuesFilterTables)
	if err != nil {
		return nil, fmt.Errorf("count by label: issues ids: %w", err)
	}
	ids = append(ids, issueIDs...)

	if filter.Ephemeral == nil {
		wispIDs, wispErr := filteredIDsInTx(ctx, tx, filter, WispsFilterTables)
		if wispErr != nil && !isTableNotExistError(wispErr) {
			return nil, fmt.Errorf("count by label: wisps ids (merge): %w", wispErr)
		}
		ids = append(ids, wispIDs...)
	}

	return labelCountsForIDsInTx(ctx, tx, ids)
}

// filteredIDsInTx selects the IDs matching filter from a single table.
// It is the id-only counterpart of searchTableInTx, used by label grouping
// so label hydration can route through GetLabelsForIssuesInTx.
func filteredIDsInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, tables FilterTables) ([]string, error) {
	whereClauses, args, err := BuildIssueFilterClauses("", filter, tables)
	if err != nil {
		return nil, err
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	//nolint:gosec // G201: tables.Main is hardcoded; whereSQL contains only parameterized comparisons.
	querySQL := fmt.Sprintf(`SELECT id FROM %s %s`, tables.Main, whereSQL)

	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("filtered ids %s: %w", tables.Main, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("filtered ids %s: scan: %w", tables.Main, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filtered ids %s: rows: %w", tables.Main, err)
	}
	return ids, nil
}

// labelCountsForIDsInTx hydrates labels for ids and tallies each label as a
// group. An id with zero labels contributes to the "" bucket.
func labelCountsForIDsInTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]int, error) {
	result := make(map[string]int)
	if len(ids) == 0 {
		return result, nil
	}

	wispSet, err := WispIDSetInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("count by label: wisp id set: %w", err)
	}

	labelMap, err := GetLabelsForIssuesInTx(ctx, tx, ids, wispSet)
	if err != nil {
		return nil, fmt.Errorf("count by label: hydrate labels: %w", err)
	}

	for _, id := range ids {
		labels := labelMap[id]
		if len(labels) == 0 {
			result[""]++
			continue
		}
		for _, lb := range labels {
			result[lb]++
		}
	}
	return result, nil
}
