package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// SearchIssueSummariesInTx executes a filtered search within an existing
// transaction and returns narrow IssueSummary rows. Mirrors SearchIssuesInTx
// (filter resolution, wisp admission, label hydration) but selects only
// IssueSummaryColumns — no TEXT/JSON dereferences. D3 build, be-nu4.3.2.
//
// Label hydration uses the D2 wisp-set helper; the set is built once before
// any result-producing query to avoid multiple-active-result-sets on the
// same connection.
func SearchIssueSummariesInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.IssueSummary, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		results, err := searchSummaryTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("search wisps summaries (ephemeral filter): %w", err)
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	results, err := searchSummaryTableInTx(ctx, tx, query, filter, IssuesFilterTables)
	if err != nil {
		return nil, fmt.Errorf("search issue summaries: %w", err)
	}

	if filter.Ephemeral == nil {
		wispResults, wispErr := searchSummaryTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if wispErr != nil && !isTableNotExistError(wispErr) {
			return nil, fmt.Errorf("search wisps summaries (merge): %w", wispErr)
		}
		if len(wispResults) > 0 {
			seen := make(map[string]bool, len(results))
			for _, s := range results {
				seen[s.ID] = true
			}
			for _, s := range wispResults {
				if !seen[s.ID] {
					results = append(results, s)
				}
			}
		}
	}

	return results, nil
}

// searchSummaryTableInTx runs a filtered search against a specific table set
// (issues or wisps) and returns narrow summaries. Parallel to searchTableInTx
// but with IssueSummaryColumns and the summary scan path.
func searchSummaryTableInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables) ([]*types.IssueSummary, error) {
	whereClauses, args, err := BuildIssueFilterClauses(query, filter, tables)
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
	querySQL := fmt.Sprintf(`SELECT %s FROM %s %s ORDER BY priority ASC, created_at DESC, id ASC %s`,
		IssueSummaryColumns, tables.Main, whereSQL, limitSQL)

	// Build the wisp set BEFORE the result-producing query so label hydration
	// below can reuse it without needing a second active result set.
	wispSet, err := WispIDSetInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("build wisp set: %w", err)
	}

	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search %s summaries: %w", tables.Main, err)
	}

	var summaries []*types.IssueSummary
	for rows.Next() {
		sum, scanErr := ScanIssueSummaryFrom(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("search %s summaries: scan: %w", tables.Main, scanErr)
		}
		summaries = append(summaries, sum)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search %s summaries: rows: %w", tables.Main, err)
	}

	if len(summaries) > 0 {
		ids := make([]string, len(summaries))
		for i, s := range summaries {
			ids[i] = s.ID
		}
		labelMap, labelErr := GetLabelsForIssuesInTx(ctx, tx, ids, wispSet)
		if labelErr != nil {
			return nil, fmt.Errorf("search %s summaries: hydrate labels: %w", tables.Main, labelErr)
		}
		for _, s := range summaries {
			if labels, ok := labelMap[s.ID]; ok {
				s.Labels = labels
			}
		}
	}

	return summaries, nil
}
