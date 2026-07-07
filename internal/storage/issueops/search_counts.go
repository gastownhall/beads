package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func SearchIssuesWithCountsInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("search issues with counts: wisp dependency probe: %w", err)
	}

	if filter.Ephemeral != nil && *filter.Ephemeral {
		empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
		if probeErr != nil {
			return nil, fmt.Errorf("search issues with counts: ephemeral wisp probe: %w", probeErr)
		}
		if !empty && wispDepsExist {
			wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
			if err != nil && !isTableNotExistError(err) {
				return nil, err
			}
			if len(wisps) > 0 {
				return finishSearchIssuesWithCounts(wisps, filter), nil
			}
		}
		// Fall through: the wisps tier is missing/empty or matched no rows.
		// Mirror SearchIssuesInTx / CountIssuesInTx so count-projection searches
		// also surface a durable issues-table row flagged ephemeral=1 instead of
		// dropping it. Use the same IssuesFilterTables query the non-ephemeral
		// path uses, keeping the GH#4387 count/list cardinality parity for
		// searches that project counts (e.g. `bd search --counts --include-infra`).
		out, err := runFilterSearchQueryInTx(ctx, tx, query, filter, IssuesFilterTables, wispDepsExist)
		if err != nil {
			return nil, err
		}
		return finishSearchIssuesWithCounts(out, filter), nil
	}

	out, err := runFilterSearchQueryInTx(ctx, tx, query, filter, IssuesFilterTables, wispDepsExist)
	if err != nil {
		return nil, err
	}

	// Skip wisps merge entirely when caller opts out (Q2: perf escape hatch).
	if filter.SkipWisps {
		return finishSearchIssuesWithCounts(out, filter), nil
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("search issues with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return finishSearchIssuesWithCounts(out, filter), nil
	}
	if !wispDepsExist {
		return finishSearchIssuesWithCounts(out, filter), nil
	}

	wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
	if err != nil {
		if isTableNotExistError(err) {
			return finishSearchIssuesWithCounts(out, filter), nil
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return finishSearchIssuesWithCounts(out, filter), nil
	}

	// Prefer the canonical wisp record when an ID exists in both tables (be-iabdi).
	wispByID := make(map[string]struct{}, len(wisps))
	for _, w := range wisps {
		if w != nil && w.Issue != nil {
			wispByID[w.Issue.ID] = struct{}{}
		}
	}
	var kept []*types.IssueWithCounts
	for _, iwc := range out {
		if iwc == nil || iwc.Issue == nil {
			kept = append(kept, iwc)
			continue
		}
		if _, dup := wispByID[iwc.Issue.ID]; !dup {
			kept = append(kept, iwc)
		}
	}
	kept = append(kept, wisps...)
	return finishSearchIssuesWithCounts(kept, filter), nil
}

func runFilterSearchQueryInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	whereClauses, args, err := BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + joinAnd(whereClauses)
	}
	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf("LIMIT %d", filter.Limit)
	}
	orderBy := sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i")
	return runSearchQueryInTx(ctx, tx, tables, whereSQL, orderBy, limitSQL, args, includeWispReverseDeps, filter.SkipLabels)
}

//nolint:gosec // G201: SQL fragments are caller-built from hardcoded shapes
func runSearchQueryInTx(ctx context.Context, tx *sql.Tx, tables FilterTables, whereSQL, orderBySQL, limitSQL string, args []interface{}, includeWispReverseDeps bool, skipLabels bool) ([]*types.IssueWithCounts, error) {
	searchSQL := sqlbuild.SearchCountsSQL(tables, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, skipLabels)

	rows, err := tx.QueryContext(ctx, searchSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search count %s: %w", tables.Main, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, scanErr := ScanReadyWorkRowWithCounts(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if iwc == nil || iwc.Issue == nil {
			continue
		}
		if seen[iwc.Issue.ID] {
			continue
		}
		seen[iwc.Issue.ID] = true
		out = append(out, iwc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search count %s: rows: %w", tables.Main, err)
	}
	return out, nil
}

func finishSearchIssuesWithCounts(items []*types.IssueWithCounts, filter types.IssueFilter) []*types.IssueWithCounts {
	sortSearchIssuesWithCounts(items, filter.SortBy, filter.SortDesc)
	if filter.Limit > 0 && len(items) > filter.Limit {
		return items[:filter.Limit]
	}
	return items
}

// sortSearchIssuesWithCounts must order the merged issues+wisps rows the same
// way sqlbuild.OrderBy orders each per-table query; otherwise the limit cut in
// finishSearchIssuesWithCounts keeps a different row set than SQL selected.
func sortSearchIssuesWithCounts(items []*types.IssueWithCounts, sortBy string, sortDesc bool) {
	if len(items) <= 1 {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a == nil || a.Issue == nil {
			return false
		}
		if b == nil || b.Issue == nil {
			return true
		}
		return sqlbuild.Less(a.Issue, b.Issue, sortBy, sortDesc)
	})
}

func joinAnd(clauses []string) string {
	switch len(clauses) {
	case 0:
		return ""
	case 1:
		return clauses[0]
	}
	total := 0
	for _, c := range clauses {
		total += len(c)
	}
	total += 5 * (len(clauses) - 1)
	buf := make([]byte, 0, total)
	for i, c := range clauses {
		if i > 0 {
			buf = append(buf, " AND "...)
		}
		buf = append(buf, c...)
	}
	return string(buf)
}
