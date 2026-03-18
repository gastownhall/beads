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
func SearchIssuesInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Route ephemeral-only queries to wisps table.
	if filter.Ephemeral != nil && *filter.Ephemeral {
		results, err := searchTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if err != nil {
			return nil, fmt.Errorf("search wisps (ephemeral filter): %w", err)
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	results, err := searchTableInTx(ctx, tx, query, filter, IssuesFilterTables)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	// When filter.Ephemeral is nil (search everything), also search the wisps
	// table and merge results.
	if filter.Ephemeral == nil {
		wispResults, wispErr := searchTableInTx(ctx, tx, query, filter, WispsFilterTables)
		if wispErr == nil && len(wispResults) > 0 {
			seen := make(map[string]bool, len(results))
			for _, issue := range results {
				seen[issue.ID] = true
			}
			for _, issue := range wispResults {
				if !seen[issue.ID] {
					results = append(results, issue)
				}
			}
		}
	}

	return results, nil
}

// searchTableInTx runs a filtered search against a specific table set (issues or wisps).
func searchTableInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables) ([]*types.Issue, error) {
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
		IssueSelectColumns, tables.Main, whereSQL, limitSQL)

	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", tables.Main, err)
	}
	defer rows.Close()

	var issues []*types.Issue
	for rows.Next() {
		issue, scanErr := ScanIssueFrom(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("search %s: scan: %w", tables.Main, scanErr)
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search %s: rows: %w", tables.Main, err)
	}

	// Hydrate labels for each issue.
	for _, issue := range issues {
		labels, labelErr := GetLabelsInTx(ctx, tx, tables.Labels, issue.ID)
		if labelErr == nil {
			issue.Labels = labels
		}
	}

	return issues, nil
}
