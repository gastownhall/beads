package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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
	// joinLeases adds the leases LEFT JOIN to the FROM clause; required by
	// any projection whose columns include sqlbuild.LeaseSelectColumns.
	joinLeases bool
	// less, when non-nil, orders two elements the same way SQL's ORDER BY
	// ordered each per-table query (sqlbuild.OrderBy), given the same
	// sortBy/sortDesc values the caller's filter carried. searchInTx's
	// merge branch (issues+wisps) needs this to re-sort the concatenation
	// of two independently-ordered legs before trimming to filter.Limit —
	// without it, the trim keeps whichever leg happens to come first in
	// the concatenation regardless of sort rank (be-x42v.4 round-5
	// follow-up). nil for projections with no sortable payload to compare
	// (idProjection scans bare IDs, no column data); safe today because no
	// caller combines Limit>0 with a merged (non-SkipWisps) ID-only search
	// — see the merge-trim call site for the fallback behavior if that
	// changes.
	less func(a, b T, sortBy string, sortDesc bool) bool
}

var issueProjection = searchProjection[*types.Issue]{
	columns:    func(_ FilterTables) string { return IssueSelectColumns },
	scan:       func(rows *sql.Rows) (*types.Issue, error) { return ScanIssueFrom(rows) },
	id:         func(issue *types.Issue) string { return issue.ID },
	hydrate:    hydrateIssueLabelsAndDeps,
	less:       sqlbuild.Less,
	idShrink:   true,
	joinLeases: true,
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
			results = trimToSearchLimit(results, filter.Limit)
			if capErr := EnforceMaxRowsCap(len(results), filter.MaxRows, filter.MaxRowsSource); capErr != nil {
				return nil, capErr
			}
			return results, nil
		}
		// Fall through: wisps table doesn't exist or returned no results
	}

	results, err := searchTableInTxT(ctx, tx, query, filter, IssuesFilterTables, proj)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	// Skip wisps merge entirely when caller opts out (Q2: perf escape hatch).
	// This is also a terminal return of the row set actually handed back to
	// the caller, so — like the empty-wisps-table early return above — it
	// must enforce the cap itself; skipping it here would let SkipWisps
	// callers (e.g. bd list's default filter) silently bypass MaxRows.
	if filter.SkipWisps {
		results = trimToSearchLimit(results, filter.Limit)
		if err := EnforceMaxRowsCap(len(results), filter.MaxRows, filter.MaxRowsSource); err != nil {
			return nil, err
		}
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
			// No wisps to merge, but the issues-only result set can still
			// exceed the cap (the merge path below enforces it at line ~74;
			// this early return must enforce it too, or the cap is silently
			// skipped whenever the wisps table is empty).
			results = trimToSearchLimit(results, filter.Limit)
			if err := EnforceMaxRowsCap(len(results), filter.MaxRows, filter.MaxRowsSource); err != nil {
				return nil, err
			}
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
			// The concatenation above is two independently ORDER BY'd legs,
			// not a globally ordered set: re-sort by the same key before
			// the merge's trimToSearchLimit call below, or trimming just
			// keeps "however many durable rows fit" and can silently drop
			// a higher-ranked wisp in favor of a lower-ranked durable row
			// (be-x42v.4 round-5 follow-up). Mirrors
			// finishSearchIssuesWithCounts's sortSearchIssuesWithCounts,
			// which already sorts before its own trim.
			sortMergedResults(results, proj.less, filter.SortBy, filter.SortDesc)
		}
	}

	// Apply the defensive cap on the merged, delivered result set. Each leg
	// (issues, wisps) is independently bounded by EffectiveSearchLimit, so
	// the merged pre-trim count can exceed the cap even when filter.Limit
	// itself does not — e.g. Limit=2, MaxRows=5, 3 rows in each table merges
	// to 6, which would trip MaxRows even though the page actually handed
	// back to the caller (trimmed to Limit=2) is well within the cap.
	// Trimming before the cap check (mirrors GetReadyWorkInTx's
	// mergeReadyWisps, which trims before its caller's EnforceMaxRowsCap
	// runs) avoids that false positive; a single-source result never
	// exceeds Limit when Limit>0 in the first place, so this trim is a
	// no-op there and a genuine overage still fires (Limit=0, or
	// Limit>MaxRows overage that survives the trim).
	results = trimToSearchLimit(results, filter.Limit)
	if err := EnforceMaxRowsCap(len(results), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}

	return results, nil
}

// sortMergedResults re-sorts results in place by the same key SQL's ORDER BY
// applied to each per-leg query, so a subsequent trimToSearchLimit call on a
// concatenation of two independently-ordered legs (issues+wisps) keeps the
// globally correct top-N page rather than an arbitrary prefix of the
// concatenation. No-ops when less is nil (no sortable payload for this
// projection — see searchProjection.less's doc comment) or when there's
// nothing to reorder.
func sortMergedResults[T any](results []T, less func(a, b T, sortBy string, sortDesc bool) bool, sortBy string, sortDesc bool) {
	if less == nil || len(results) <= 1 {
		return
	}
	sort.SliceStable(results, func(i, j int) bool {
		return less(results[i], results[j], sortBy, sortDesc)
	})
}

// trimToSearchLimit truncates results to filter.Limit (when set, Limit>0)
// before the caller-facing MaxRows cap check runs. Every searchInTx exit
// path routes results returned to the caller through this before
// EnforceMaxRowsCap — see the comment above the final merge check for why
// (be-x42v.4 round-4 follow-up, mirrors finishReadyWorkWithCounts).
func trimToSearchLimit[T any](results []T, limit int) []T {
	if limit > 0 && len(results) > limit {
		return results[:limit]
	}
	return results
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
	if eff := EffectiveSearchLimit(filter.Limit, filter.MaxRows); eff > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", eff)
	}

	selectKeyword := "SELECT "
	if plan.Distinct {
		selectKeyword = "SELECT DISTINCT "
	}
	fromSQL := plan.FromSQL
	if proj.joinLeases {
		fromSQL += " " + sqlbuild.LeaseJoin(tables.Main)
	}

	//nolint:gosec // G201: SQL fragments are built from fixed table/column names and parameterized filters.
	querySQL := fmt.Sprintf(`%s%s FROM %s %s %s %s`,
		selectKeyword, proj.columns(tables), fromSQL, whereSQL, sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, ""), limitSQL)

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
	fetchFrom := tables.Main
	if proj.joinLeases {
		fetchFrom += " " + sqlbuild.LeaseJoin(tables.Main)
	}
	//nolint:gosec // G201: column expression and table name are fixed; ids are parameterized.
	fetchSQL := fmt.Sprintf(`SELECT %s FROM %s WHERE id IN (%s)`,
		proj.columns(tables), fetchFrom, strings.Join(placeholders, ","))

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

// EffectiveSearchLimit returns the SQL LIMIT to apply given a caller-supplied
// Limit and a defensive MaxRows cap. Semantics (architecture be-jp5s D1/R-03):
//
//   - limit=0, maxRows=0: returns 0 (no LIMIT clause; unlimited)
//   - limit=N, maxRows=0: returns N (today's --limit behavior)
//   - limit=0, maxRows=M: returns M+1 (detect overage at cap+1)
//   - limit=N, maxRows=M: returns N if N<=M, else M+1
//
// Callers issue LIMIT cap+1 specifically so that EnforceMaxRowsCap can detect
// overage by comparing len(scanned rows) to MaxRows. The returned int is the
// LIMIT value; treat 0 as "do not emit a LIMIT clause".
func EffectiveSearchLimit(limit, maxRows int) int {
	if maxRows > 0 {
		if limit == 0 || limit > maxRows {
			return maxRows + 1
		}
	}
	return limit
}

// EnforceMaxRowsCap returns *ErrTooManyRows when found exceeds maxRows.
// maxRows<=0 disables the cap; the function returns nil. source is the
// attribution string surfaced in the error message (see ErrTooManyRows).
// Call this after scanning rows from a query that used EffectiveSearchLimit
// to size LIMIT.
func EnforceMaxRowsCap(found, maxRows int, source string) error {
	if maxRows > 0 && found > maxRows {
		return &ErrTooManyRows{Found: found, Cap: maxRows, Source: source}
	}
	return nil
}
