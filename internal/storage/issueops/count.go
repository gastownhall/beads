package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// CountIssuesInTx counts issues matching the query and filter within a
// transaction. Mirrors SearchIssuesInTx's wisps-merge semantics but returns
// only the count: ephemeral-only filters route to the wisps table,
// SkipWisps=true counts the durable issues table only, and otherwise the
// wisps count is merged in (GH#4387).
func CountIssuesInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) (int, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		wispCount, err := countTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if err != nil && !isTableNotExistError(err) {
			return 0, fmt.Errorf("count wisps (ephemeral filter): %w", err)
		}
		if wispCount > 0 {
			return wispCount, nil
		}
		// Fall through: the wisps table is missing or has no matching rows.
		// SearchIssuesInTx does the same — it searches the durable issues table
		// in this case (search.go "Fall through: wisps table doesn't exist or
		// returned no results"). Mirroring it keeps the GH#4387 count/list parity
		// contract for an infra-type filter that matches only a durable
		// issues-table row flagged ephemeral=1 — a defensive parity state that
		// normal creation never produces (ephemeral/infra beads route to the
		// wisps table on insert), but which would otherwise be reported as 0 by
		// count while list returns it.
		count, err := countTableInTx(ctx, tx, query, filter, IssuesFilterTables)
		if err != nil {
			return 0, fmt.Errorf("count issues (ephemeral fall-through): %w", err)
		}
		return count, nil
	}

	count, err := countTableInTx(ctx, tx, query, filter, IssuesFilterTables)
	if err != nil {
		return 0, fmt.Errorf("count issues: %w", err)
	}

	if filter.SkipWisps {
		return count, nil
	}

	// Merge wisps count when caller hasn't opted out (same semantics as SearchIssuesInTx).
	// Issues and wisps are always in separate tables (PromoteFromEphemeral deletes the
	// wisps row), so the two counts don't double-count. count trusts that disjoint-table
	// invariant; SearchIssuesInTx is the corruption detector — it errors loudly if an ID
	// appears in both tables ("id %q exists in both issues and wisps").
	wispCount, wispErr := countTableInTx(ctx, tx, query, filter, WispsFilterTables)
	if wispErr != nil && !isTableNotExistError(wispErr) {
		return 0, fmt.Errorf("count wisps (merge): %w", wispErr)
	}
	return count + wispCount, nil
}

// CountIssuesByGroupInTx counts issues grouped by a field within a transaction.
// groupBy must be one of: status, priority, type, assignee, label.
// Returns a map of group value → count, using the same display format as bd count.
//
// Mirrors CountIssuesInTx's wisps-merge semantics: ephemeral-only filters
// route to the wisps table, SkipWisps=true counts the durable issues table
// only, and otherwise the wisps tier is merged into each group (GH#4387).
func CountIssuesByGroupInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		wispCounts, err := countGroupForTablesInTx(ctx, tx, filter, groupBy, WispsFilterTables)
		if err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("count wisps by %s (ephemeral filter): %w", groupBy, err)
		}
		total := 0
		for _, v := range wispCounts {
			total += v
		}
		if total > 0 {
			return wispCounts, nil
		}
		// Fall through: the wisps table is missing or matched no rows. Mirror
		// CountIssuesInTx's scalar ephemeral fall-through (and SearchIssuesInTx)
		// so grouped counts also report a durable issues-table row flagged
		// ephemeral=1. Without this the scalar Total (which falls through) would
		// disagree with the sum of the grouped buckets (wisps-only), breaking
		// the GH#4387 count/list cardinality parity for `bd count
		// --include-infra --by-*`.
		counts, err := countGroupForTablesInTx(ctx, tx, filter, groupBy, IssuesFilterTables)
		if err != nil {
			return nil, fmt.Errorf("count issues by %s (ephemeral fall-through): %w", groupBy, err)
		}
		return counts, nil
	}

	counts, err := countGroupForTablesInTx(ctx, tx, filter, groupBy, IssuesFilterTables)
	if err != nil {
		return nil, err
	}

	if filter.SkipWisps {
		return counts, nil
	}

	// Merge wisps counts when the caller hasn't opted out (same semantics as
	// CountIssuesInTx / SearchIssuesInTx; the two tables never share an ID).
	wispCounts, wispErr := countGroupForTablesInTx(ctx, tx, filter, groupBy, WispsFilterTables)
	if wispErr != nil && !isTableNotExistError(wispErr) {
		return nil, fmt.Errorf("count wisps by %s (merge): %w", groupBy, wispErr)
	}
	for k, v := range wispCounts {
		counts[k] += v
	}
	return counts, nil
}

// countGroupForTablesInTx runs a grouped count against one table set
// (issues or wisps) and normalizes keys to bd count's display format.
func countGroupForTablesInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, groupBy string, tables FilterTables) (map[string]int, error) {
	if groupBy == "label" {
		return countByLabelInTx(ctx, tx, filter, tables)
	}

	// Map user-facing groupBy name to SQL column name.
	groupByToColumn := map[string]string{
		"status":   "status",
		"priority": "priority",
		"type":     "issue_type",
		"assignee": "assignee",
	}
	col, ok := groupByToColumn[groupBy]
	if !ok {
		return nil, fmt.Errorf("unsupported groupBy: %s", groupBy)
	}

	rawCounts, err := countByColumnInTx(ctx, tx, filter, col, tables)
	if err != nil {
		return nil, err
	}

	// Normalize keys to match bd count display format.
	counts := make(map[string]int, len(rawCounts))
	for k, v := range rawCounts {
		switch groupBy {
		case "priority":
			k = "P" + k
		case "assignee":
			if k == "" {
				k = "(unassigned)"
			}
		}
		counts[k] += v
	}
	return counts, nil
}

// countTableInTx runs SELECT COUNT(*) FROM <table> WHERE <query+filter>.
func countTableInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables) (int, error) {
	clauses, args, err := BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return 0, err
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = " WHERE " + strings.Join(clauses, " AND ")
	}
	//nolint:gosec // G201: tables.Main is hardcoded to "issues" or "wisps"
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", tables.Main, whereSQL)
	var count int
	if err := tx.QueryRowContext(ctx, countSQL, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// countByColumnInTx runs SELECT <col>, COUNT(*) GROUP BY <col> against a table.
// Returns raw column values as keys (callers normalize for display).
func countByColumnInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, col string, tables FilterTables) (map[string]int, error) {
	clauses, args, err := BuildIssueFilterClauses("", filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = " WHERE " + strings.Join(clauses, " AND ")
	}
	//nolint:gosec // G201: tables.Main hardcoded; col validated by caller
	query := fmt.Sprintf("SELECT COALESCE(%s, ''), COUNT(*) FROM %s%s GROUP BY %s", col, tables.Main, whereSQL, col)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("count by column %s: %w", col, err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, fmt.Errorf("scan count row: %w", err)
		}
		counts[key] += count
	}
	return counts, rows.Err()
}

// countByLabelInTx counts issues grouped by label using a subquery to avoid
// Dolt's joinIter panic (join_iters.go:192). Issues with no labels are counted
// under "(no labels)".
func countByLabelInTx(ctx context.Context, tx *sql.Tx, filter types.IssueFilter, tables FilterTables) (map[string]int, error) {
	clauses, args, err := BuildIssueFilterClauses("", filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = " WHERE " + strings.Join(clauses, " AND ")
	}

	counts := make(map[string]int)

	// Label counts: subquery avoids JOIN-based joinIter panic.
	//nolint:gosec // G201: tables.Main/Labels hardcoded
	labelQuery := fmt.Sprintf(
		"SELECT l.label, COUNT(*) FROM %s l WHERE l.issue_id IN (SELECT id FROM %s%s) GROUP BY l.label",
		tables.Labels, tables.Main, whereSQL,
	)
	rows, err := tx.QueryContext(ctx, labelQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("count by label: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var count int
		if err := rows.Scan(&label, &count); err != nil {
			return nil, fmt.Errorf("scan label count: %w", err)
		}
		counts[label] += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// "(no labels)" count: issues matching filter with no label row.
	noLabelWhere := whereSQL
	if noLabelWhere == "" {
		noLabelWhere = fmt.Sprintf(" WHERE id NOT IN (SELECT DISTINCT issue_id FROM %s)", tables.Labels)
	} else {
		noLabelWhere += fmt.Sprintf(" AND id NOT IN (SELECT DISTINCT issue_id FROM %s)", tables.Labels)
	}
	//nolint:gosec // G201: tables.Main/Labels hardcoded
	noLabelQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", tables.Main, noLabelWhere)
	var noLabelCount int
	if err := tx.QueryRowContext(ctx, noLabelQuery, args...).Scan(&noLabelCount); err != nil {
		return nil, fmt.Errorf("count no-label issues: %w", err)
	}
	if noLabelCount > 0 {
		counts["(no labels)"] = noLabelCount
	}

	return counts, nil
}
