package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// SearchIssuesInTx executes a filtered issue search within an existing transaction.
// It queries the issues table, optionally merges wisps, and returns hydrated issues
// with labels populated.
//
// Set filter.SkipWisps=true for callers that never need ephemeral results; this
// avoids the unconditional full-table wisps scan (Q2 perf opt).
func SearchIssuesInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Route ephemeral-only queries to wisps table.
	if filter.Ephemeral != nil && *filter.Ephemeral {
		results, err := searchTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("search wisps (ephemeral filter): %w", err)
		}
		if len(results) > 0 {
			return results, nil
		}
		// Fall through: wisps table doesn't exist or returned no results
	}

	results, err := searchTableInTx(ctx, tx, query, filter, IssuesFilterTables)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	// Skip wisps merge entirely when caller opts out (Q2: perf escape hatch).
	if filter.SkipWisps {
		return results, nil
	}

	// When filter.Ephemeral is nil (search everything) or false (non-ephemeral
	// only), also search the wisps table and merge results. NoHistory beads are
	// stored in the wisps table with ephemeral=0, so they must survive an
	// Ephemeral=&false filter (GH#3649). The WHERE clause added by
	// BuildIssueFilterClauses handles the per-row ephemeral column check, so
	// querying wisps here with Ephemeral=&false returns only NoHistory beads
	// while correctly excluding true ephemeral wisps. (GH#3659)
	if filter.Ephemeral == nil || !*filter.Ephemeral {
		empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
		if probeErr != nil {
			return nil, fmt.Errorf("search wisps (merge): probe: %w", probeErr)
		}
		if empty {
			return results, nil
		}
		wispResults, wispErr := searchTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if wispErr != nil && !isTableNotExistError(wispErr) {
			return nil, fmt.Errorf("search wisps (merge): %w", wispErr)
		}
		if len(wispResults) > 0 {
			seen := make(map[string]bool, len(results))
			for _, issue := range results {
				seen[issue.ID] = true
			}
			for _, issue := range wispResults {
				if seen[issue.ID] {
					return nil, fmt.Errorf("id %q exists in both issues and wisps", issue.ID)
				}
				results = append(results, issue)
			}
		}
	}

	return results, nil
}

// searchTableInTx runs a filtered search against a specific table set (issues or wisps).
//
// When filter.Limit > 0 and !filter.NoIDShrink, uses Pattern B (id-shrunk): a cheap
// SELECT id scan + batch hydration instead of a full 47-column projection scan.
// Pattern B is equivalent to Pattern A but faster on large corpora where most rows
// are never needed (mirrors the pattern in scanIssueIDs and GetStaleIssuesInTx).
func searchTableInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables) ([]*types.Issue, error) {
	fromSQL, labelWhere, labelArgs, labelDriven, filterForClauses := buildLabelDrivenSearch(filter, tables)
	whereClauses, args, err := BuildIssueFilterClauses(query, filterForClauses, tables)
	if err != nil {
		return nil, err
	}
	if len(labelWhere) > 0 {
		whereClauses = append(labelWhere, whereClauses...)
		args = append(labelArgs, args...)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Pattern B: when Limit > 0, use a cheap id scan then hydrate in batch.
	if filter.Limit > 0 && !filter.NoIDShrink {
		return searchTablePatternB(ctx, tx, fromSQL, whereSQL, args, filter, tables, labelDriven)
	}

	// Pattern A: full 47-column scan (used for unlimited queries or when NoIDShrink is set).
	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	selectSQL := "SELECT "
	if labelDriven {
		selectSQL = "SELECT DISTINCT "
	}
	//nolint:gosec // G201: SQL fragments are built from fixed table/column names and parameterized filters.
	querySQL := fmt.Sprintf(`%s%s FROM %s %s %s %s`,
		selectSQL, IssueSelectColumns, fromSQL, whereSQL, issueOpsOrderBy(filter.SortBy, filter.SortDesc, ""), limitSQL)

	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", tables.Main, err)
	}

	var issues []*types.Issue
	seen := make(map[string]bool)
	for rows.Next() {
		issue, scanErr := ScanIssueFrom(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("search %s: scan: %w", tables.Main, scanErr)
		}
		if seen[issue.ID] {
			continue // GH#3567: skip duplicate rows from dependency subqueries
		}
		seen[issue.ID] = true
		issues = append(issues, issue)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search %s: rows: %w", tables.Main, err)
	}

	if err := hydrateIssues(ctx, tx, issues, tables, filter.IncludeDependencies, filter.SkipLabels); err != nil {
		return nil, fmt.Errorf("search %s: hydrate: %w", tables.Main, err)
	}

	return issues, nil
}

// searchTablePatternB runs Pattern B: SELECT id LIMIT n → batch hydrate.
// Equivalent result to Pattern A but avoids streaming all 47 columns for rows
// that won't survive the LIMIT cut. Mirrors the approach in GetStaleIssuesInTx.
func searchTablePatternB(ctx context.Context, tx *sql.Tx, fromSQL, whereSQL string, args []interface{}, filter types.IssueFilter, tables FilterTables, labelDriven bool) ([]*types.Issue, error) {
	idSelect := "SELECT "
	if labelDriven {
		idSelect = "SELECT DISTINCT "
	}
	//nolint:gosec // G201: SQL fragments from fixed column/table names and parameterized filters.
	idQuery := fmt.Sprintf(`%s%s.id FROM %s %s %s LIMIT %d`,
		idSelect, tables.Main, fromSQL, whereSQL,
		issueOpsOrderBy(filter.SortBy, filter.SortDesc, tables.Main), filter.Limit)

	rows, err := tx.QueryContext(ctx, idQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search %s (id scan): %w", tables.Main, err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("search %s (id scan): scan: %w", tables.Main, err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search %s (id scan): rows: %w", tables.Main, err)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	// Batch-fetch full rows from the known table (no wispSet partition needed).
	placeholders := make([]string, len(ids))
	fetchArgs := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		fetchArgs[i] = id
	}
	//nolint:gosec // G201: table name is a fixed constant from FilterTables.
	fetchSQL := fmt.Sprintf(`SELECT %s FROM %s WHERE id IN (%s)`,
		IssueSelectColumns, tables.Main, strings.Join(placeholders, ","))

	fetchRows, err := tx.QueryContext(ctx, fetchSQL, fetchArgs...)
	if err != nil {
		return nil, fmt.Errorf("search %s (hydrate): %w", tables.Main, err)
	}

	issueMap := make(map[string]*types.Issue, len(ids))
	for fetchRows.Next() {
		issue, scanErr := ScanIssueFrom(fetchRows)
		if scanErr != nil {
			_ = fetchRows.Close()
			return nil, fmt.Errorf("search %s (hydrate): scan: %w", tables.Main, scanErr)
		}
		issueMap[issue.ID] = issue
	}
	_ = fetchRows.Close()
	if err := fetchRows.Err(); err != nil {
		return nil, fmt.Errorf("search %s (hydrate): rows: %w", tables.Main, err)
	}

	// Reorder to preserve the id-scan ORDER BY.
	issues := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := issueMap[id]; ok {
			issues = append(issues, issue)
		}
	}

	if err := hydrateIssues(ctx, tx, issues, tables, filter.IncludeDependencies, filter.SkipLabels); err != nil {
		return nil, fmt.Errorf("search %s (pattern B): hydrate: %w", tables.Main, err)
	}

	return issues, nil
}

// hydrateIssues populates labels (and optionally dependencies) on a slice of issues.
// All issues must belong to tables.Main; labels come from tables.Labels.
// When skipLabels is true, label hydration is suppressed (Issue.Labels is left nil).
func hydrateIssues(ctx context.Context, tx *sql.Tx, issues []*types.Issue, tables FilterTables, includeDeps bool, skipLabels bool) error {
	if len(issues) == 0 {
		return nil
	}

	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}

	if !skipLabels {
		// Fast path: every ID in `ids` belongs to tables.Labels.
		// Skip the per-batch wisp-partition round-trip (GH#3414).
		labelMap, err := GetLabelsForIssuesFromTableInTx(ctx, tx, tables.Labels, ids)
		if err != nil {
			return fmt.Errorf("hydrate labels: %w", err)
		}
		for _, issue := range issues {
			if labels, ok := labelMap[issue.ID]; ok {
				issue.Labels = labels
			}
		}
	}

	if includeDeps {
		depMap, err := GetDependencyRecordsForIssuesFromTableInTx(ctx, tx, tables.Dependencies, ids)
		if err != nil {
			return fmt.Errorf("hydrate dependencies: %w", err)
		}
		for _, issue := range issues {
			if deps, ok := depMap[issue.ID]; ok {
				issue.Dependencies = deps
			}
		}
	}

	return nil
}

func buildLabelDrivenSearch(filter types.IssueFilter, tables FilterTables) (string, []string, []interface{}, bool, types.IssueFilter) {
	labels := compactNonEmptyStrings(filter.Labels)
	labelsAny := compactNonEmptyStrings(filter.LabelsAny)
	if len(labels) == 0 && len(labelsAny) == 0 {
		return tables.Main, nil, nil, false, filter
	}

	filterForClauses := filter
	filterForClauses.Labels = nil
	filterForClauses.LabelsAny = nil

	var joins []string
	var where []string
	var args []interface{}

	for i, label := range labels {
		alias := fmt.Sprintf("label_filter_%d", i)
		joins = append(joins, fmt.Sprintf("JOIN %s %s ON %s.issue_id = %s.id", tables.Labels, alias, alias, tables.Main))
		where = append(where, fmt.Sprintf("%s.label = ?", alias))
		args = append(args, label)
	}

	if len(labelsAny) > 0 {
		alias := "label_filter_any"
		joins = append(joins, fmt.Sprintf("JOIN %s %s ON %s.issue_id = %s.id", tables.Labels, alias, alias, tables.Main))
		placeholders := make([]string, len(labelsAny))
		for i, label := range labelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		where = append(where, fmt.Sprintf("%s.label IN (%s)", alias, strings.Join(placeholders, ", ")))
	}

	return tables.Main + " " + strings.Join(joins, " "), where, args, true, filterForClauses
}

func compactNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

var issueOpsSortDefs = map[string]struct {
	column     string
	defaultDir string
}{
	"":         {"priority", "ASC"},
	"priority": {"priority", "ASC"},
	"created":  {"created_at", "DESC"},
	"updated":  {"updated_at", "DESC"},
	"closed":   {"closed_at", "DESC"},
	"status":   {"status", "ASC"},
	"type":     {"issue_type", "ASC"},
	"assignee": {"assignee", "ASC"},
	"title":    {"title", "ASC"},
}

func issueOpsOrderBy(sortBy string, sortDesc bool, table string) string {
	if sortBy == "id" {
		return ""
	}
	def, ok := issueOpsSortDefs[sortBy]
	if !ok {
		def = issueOpsSortDefs[""]
		sortBy = ""
	}
	qual := ""
	if table != "" {
		qual = table + "."
	}
	dir := def.defaultDir
	if sortDesc {
		if dir == "ASC" {
			dir = "DESC"
		} else {
			dir = "ASC"
		}
	}
	col := qual + def.column
	if sortBy == "title" {
		col = "LOWER(" + qual + "title)"
	}
	if sortBy == "" || sortBy == "priority" {
		return fmt.Sprintf("ORDER BY %spriority %s, %screated_at DESC, %sid ASC", qual, dir, qual, qual)
	}
	return fmt.Sprintf("ORDER BY %s %s, %sid ASC", col, dir, qual)
}
