package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// SearchIssuesInTx executes a filtered issue search within an existing
// transaction and returns hydrated issues (labels, and optionally
// dependencies via filter.IncludeDependencies). Routing, wisp-merge, and
// overlap detection live in the shared searchInTx wrapper.
func SearchIssuesInTx(ctx context.Context, tx DBTX, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return searchInTx(ctx, tx, query, filter, issueProjection)
}

// SearchIssueIDsInTx is the narrow-projection variant of SearchIssuesInTx:
// applies the same WHERE clauses (label joins, wisp-merge semantics) but
// projects only `id` and returns []string. Use when full row hydration is
// wasted (e.g., partial-ID resolution in internal/utils/id_parser.go).
func SearchIssueIDsInTx(ctx context.Context, tx DBTX, query string, filter types.IssueFilter) ([]string, error) {
	return searchInTx(ctx, tx, query, filter, idProjection)
}

// searchProjection describes how to project, scan, and dedup search results.
// Adding a narrow-projection variant means adding a new projection literal —
// not a parallel top-level function or wisp-merge wrapper, which is how the
// two paths drifted historically.
type searchProjection[T any] struct {
	// columns returns the SELECT column expression. Receives FilterTables so
	// projections can qualify identifiers with tables.Main when needed.
	columns func(tables FilterTables) string
	// scan reads one row into T.
	scan func(*sql.Rows) (T, error)
	// id returns the issue ID for dedup (within a single table) and wisp-merge
	// overlap detection (across tables).
	id func(T) string
	// hydrate is invoked once per table after rows are scanned and the result
	// set is closed (so we don't hold multiple active result sets on the same
	// connection). nil for projections that don't need post-scan loading.
	hydrate func(ctx context.Context, tx DBTX, tables FilterTables, items []T, filter types.IssueFilter) error
	// idShrink enables Pattern B (cheap SELECT id scan → batch hydrate) for
	// limited queries. Worth it only for wide projections; the id projection
	// already scans id-only with no hydration, so it leaves this false.
	idShrink bool
}

var issueProjection = searchProjection[*types.Issue]{
	columns:  func(_ FilterTables) string { return IssueSelectColumns },
	scan:     func(rows *sql.Rows) (*types.Issue, error) { return ScanIssueFrom(rows) },
	id:       func(issue *types.Issue) string { return issue.ID },
	hydrate:  hydrateIssueLabelsAndDeps,
	idShrink: true,
}

var idProjection = searchProjection[string]{
	columns: func(tables FilterTables) string { return tables.Main + ".id" },
	scan: func(rows *sql.Rows) (string, error) {
		var id string
		err := rows.Scan(&id)
		return id, err
	},
	id:      func(id string) string { return id },
	hydrate: nil,
}

// hydrateIssueLabelsAndDeps bulk-loads labels (and optionally dependencies)
// for the given issues. searchTableInTxT runs against exactly one of the
// issues/wisps tables, so every ID here belongs to tables.Labels — we use
// GetLabelsForIssuesFromTableInTx and skip the per-batch wisp-partition
// round-trip the generic GetLabelsForIssuesInTx performs (GH#3414).
func hydrateIssueLabelsAndDeps(ctx context.Context, tx DBTX, tables FilterTables, issues []*types.Issue, filter types.IssueFilter) error {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	if !filter.SkipLabels {
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

	if filter.IncludeDependencies {
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

// searchInTx is the shared wisp-merge wrapper. Ephemeral routing, the
// empty-wisps probe, the issues+wisps queries, and overlap detection live
// here once. Both SearchIssuesInTx and SearchIssueIDsInTx use this body —
// future projections pick up improvements (e.g., the empty-probe) for free.
func searchInTx[T any](ctx context.Context, tx DBTX, query string, filter types.IssueFilter, proj searchProjection[T]) ([]T, error) {
	// Route ephemeral-only queries to wisps table.
	if filter.Ephemeral != nil && *filter.Ephemeral {
		results, err := searchTableInTxT(ctx, tx, query, filter, WispsFilterTables, proj)
		if err != nil && !isTableNotExistError(err) {
			return nil, fmt.Errorf("search wisps (ephemeral filter): %w", err)
		}
		if len(results) > 0 {
			return results, nil
		}
		// Fall through: wisps table doesn't exist or returned no results
	}

	results, err := searchTableInTxT(ctx, tx, query, filter, IssuesFilterTables, proj)
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
		wispResults, wispErr := searchTableInTxT(ctx, tx, query, filter, WispsFilterTables, proj)
		if wispErr != nil && !isTableNotExistError(wispErr) {
			return nil, fmt.Errorf("search wisps (merge): %w", wispErr)
		}
		if len(wispResults) > 0 {
			// Prefer the canonical wisp record when an ID exists in both tables.
			// Cross-table dups are a transient data-integrity issue (be-iabdi);
			// hard-erroring breaks every lookup city-wide.
			wispByID := make(map[string]struct{}, len(wispResults))
			for _, w := range wispResults {
				wispByID[proj.id(w)] = struct{}{}
			}
			var filtered []T
			for _, r := range results {
				if _, dup := wispByID[proj.id(r)]; !dup {
					filtered = append(filtered, r)
				}
			}
			results = append(filtered, wispResults...)
		}
	}

	return results, nil
}

// searchTableInTxT runs a filtered search against a specific table set
// (issues or wisps) under the given projection.
//
// When proj.idShrink && filter.Limit > 0 && !filter.NoIDShrink, uses Pattern B
// (id-shrunk): a cheap SELECT id scan + batch hydration instead of a full
// wide-projection scan, which is faster on large corpora where most rows are
// never needed (mirrors GetStaleIssuesInTx).
func searchTableInTxT[T any](ctx context.Context, tx DBTX, query string, filter types.IssueFilter, tables FilterTables, proj searchProjection[T]) ([]T, error) {
	// Pattern B: for wide projections with a LIMIT, first run the cheap,
	// non-hydrating id-only search (the very query SearchIssueIDsInTx issues),
	// then batch-fetch and hydrate only the rows that survived the LIMIT —
	// instead of streaming the full projection for rows the LIMIT discards
	// (mirrors GetStaleIssuesInTx). The id projection itself leaves idShrink
	// false: it *is* the id-only scan, so it falls straight through to the
	// direct path below — one query, no second fetch, no hydration.
	if proj.idShrink && filter.Limit > 0 && !filter.NoIDShrink {
		return searchTablePatternBT(ctx, tx, query, filter, tables, proj)
	}

	plan := sqlbuild.BuildLabelDrivenSearch(filter, tables)
	whereClauses, args, err := BuildIssueFilterClauses(query, plan.Filter, tables)
	if err != nil {
		return nil, err
	}
	whereClauses, args = plan.MergeInto(whereClauses, args)

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	selectKeyword := "SELECT "
	if plan.Distinct {
		selectKeyword = "SELECT DISTINCT "
	}
	//nolint:gosec // G201: SQL fragments are built from fixed table/column names and parameterized filters.
	querySQL := fmt.Sprintf(`%s%s FROM %s %s %s %s`,
		selectKeyword, proj.columns(tables), plan.FromSQL, whereSQL, sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, ""), limitSQL)

	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", tables.Main, err)
	}

	var results []T
	seen := make(map[string]struct{})
	for rows.Next() {
		item, scanErr := proj.scan(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("search %s: scan: %w", tables.Main, scanErr)
		}
		id := proj.id(item)
		if _, dup := seen[id]; dup {
			continue // GH#3567: skip duplicate rows from dependency subqueries
		}
		seen[id] = struct{}{}
		results = append(results, item)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search %s: rows: %w", tables.Main, err)
	}

	if proj.hydrate != nil && len(results) > 0 {
		if err := proj.hydrate(ctx, tx, tables, results, filter); err != nil {
			return nil, fmt.Errorf("search %s: %w", tables.Main, err)
		}
	}

	return results, nil
}

// searchTablePatternBT runs Pattern B for wide projections. It reuses the
// id-only search (idProjection) — byte-for-byte the non-hydrating query
// SearchIssueIDsInTx runs against this table — to get the ordered, LIMIT-bound
// id list, then batch-fetches the full projection for those ids and hydrates.
// Keeping the shrink scan in exactly one place (the id projection) is why this
// no longer hand-rolls its own SELECT id loop. Narrow projections never reach
// here: they leave idShrink false and are themselves the id scan.
func searchTablePatternBT[T any](ctx context.Context, tx DBTX, query string, filter types.IssueFilter, tables FilterTables, proj searchProjection[T]) ([]T, error) {
	ids, err := searchTableInTxT(ctx, tx, query, filter, tables, idProjection)
	if err != nil {
		return nil, err
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
	//nolint:gosec // G201: column expression and table name are fixed; ids are parameterized.
	fetchSQL := fmt.Sprintf(`SELECT %s FROM %s WHERE id IN (%s)`,
		proj.columns(tables), tables.Main, strings.Join(placeholders, ","))

	fetchRows, err := tx.QueryContext(ctx, fetchSQL, fetchArgs...)
	if err != nil {
		return nil, fmt.Errorf("search %s (hydrate): %w", tables.Main, err)
	}

	itemMap := make(map[string]T, len(ids))
	for fetchRows.Next() {
		item, scanErr := proj.scan(fetchRows)
		if scanErr != nil {
			_ = fetchRows.Close()
			return nil, fmt.Errorf("search %s (hydrate): scan: %w", tables.Main, scanErr)
		}
		itemMap[proj.id(item)] = item
	}
	_ = fetchRows.Close()
	if err := fetchRows.Err(); err != nil {
		return nil, fmt.Errorf("search %s (hydrate): rows: %w", tables.Main, err)
	}

	// Reorder to preserve the id-scan ORDER BY.
	results := make([]T, 0, len(ids))
	for _, id := range ids {
		if item, ok := itemMap[id]; ok {
			results = append(results, item)
		}
	}

	if proj.hydrate != nil && len(results) > 0 {
		if err := proj.hydrate(ctx, tx, tables, results, filter); err != nil {
			return nil, fmt.Errorf("search %s (pattern B): %w", tables.Main, err)
		}
	}

	return results, nil
}
