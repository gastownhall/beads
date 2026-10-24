package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
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
			if err != nil && !missingOptionalWispTable(err) {
				return nil, err
			}
			if len(wisps) > 0 {
				return finishSearchIssuesWithCounts(wisps, filter)
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
		return finishSearchIssuesWithCounts(out, filter)
	}

	out, err := runFilterSearchQueryInTx(ctx, tx, query, filter, IssuesFilterTables, wispDepsExist)
	if err != nil {
		return nil, err
	}

	// Skip wisps merge entirely when caller opts out (Q2: perf escape hatch).
	if filter.SkipWisps {
		return finishSearchIssuesWithCounts(out, filter)
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("search issues with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return finishSearchIssuesWithCounts(out, filter)
	}
	if !wispDepsExist {
		return finishSearchIssuesWithCounts(out, filter)
	}

	wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
	if err != nil {
		if missingOptionalWispTable(err) {
			return finishSearchIssuesWithCounts(out, filter)
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return finishSearchIssuesWithCounts(out, filter)
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
	return finishSearchIssuesWithCounts(kept, filter)
}

// hydrationFor reads the two hydration opt-outs off a search filter. It is one
// function rather than two field reads at each call site so a path cannot pick
// up one of the pair and quietly drop the other.
func hydrationFor(filter types.IssueFilter) sqlbuild.CountsHydration {
	return sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts, Lite: filter.Lite}
}

// countsIDsFirstMaxBound is the largest pushed bound a counts search answers
// ids-first — one by-IDs batch per table family; a larger bound keeps the
// predicate form. See runFilterSearchQueryInTx for why there is a ceiling.
const countsIDsFirstMaxBound = sqlbuild.QueryBatchSize

func runFilterSearchQueryInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	// A PAGE BOUND IS ONLY EVER PUSHED UNDER AN ORDER THE QUERY CAN EXPRESS —
	// the same rule searchTableInTxT applies on the plain seam. sqlbuild.OrderBy
	// renders no ORDER BY for a Go-side sort key ("id"), and a LIMIT with no
	// ORDER BY returns n rows, not the first n; this is the seam
	// bd query '<expr>' --sort id reaches on the store-shaped backends
	// (storequerier → SearchIssuesWithCounts), where BuildQueryPlan always
	// pushes a bound. So under a Go-side sort the query scans the complete
	// matching set and the same eff bound is applied below, after the order
	// exists — leaving every downstream count (the merge, the terminal
	// finishSearchIssuesWithCounts trim-then-cap) exactly what the SQL LIMIT
	// used to hand it.
	goSideSort := sqlbuild.IsGoSideSort(filter.SortBy)
	eff := EffectiveSearchLimit(filter.Limit, filter.MaxRows)

	// A PUSHED BOUND IS ANSWERED IDS-FIRST (#7635). The predicate form below
	// filters only its driver: each aggregate LEFT JOIN groups its whole side
	// table, and the LIMIT applies above all of them, so a 50-row page costs
	// what every match does (~0.5 s at 46k wisps). Instead, select the page's
	// ids with the same filter, ORDER BY and LIMIT — the plain seam's id scan,
	// labels as JOINs — and hydrate only those ids through the by-IDs form.
	// The rows are the predicate form's: the id order is total (it ends in id),
	// so the scan picks the same top eff ids, and each by-IDs row is computed
	// from the same edges (sqlbuild.SearchCountsSQL).
	//
	// Up to a ceiling, because the two forms scale differently. Ids-first
	// costs about 0.2-0.4 ms per id it hydrates, on each leg, whatever the
	// store's size; the predicate form costs about the same at any bound, and
	// that cost grows with the side tables. So the crossover moves with the
	// store: for a page the matches fill, it sat near 100 ids on a 1k-issue
	// store, near 200 at 5k, and past 1,500 at 46k wisps (dolt 2.2.0, with
	// and without table statistics). A 51-row page won or tied at every size
	// measured. The ceiling is one batch per leg, which keeps the first three
	// pages of a default listing (offset + 50 + the probe row) ids-first; the
	// price is that a full 101-200-row page on a store under ~2k rows pays up
	// to ~50 ms more than the predicate form would. Above it — an explicit
	// large --limit, or Limit 0 under a MaxRows breaker, which pushes
	// MaxRows+1 — the predicate form stays. So does a label filter the id
	// scan's JOINs would read differently (labelJoinsMatchPredicate).
	if eff > 0 && !goSideSort && eff <= countsIDsFirstMaxBound && labelJoinsMatchPredicate(filter) {
		ids, err := searchTableInTxT(ctx, tx, query, filter, tables, idProjection)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			// AN EMPTY PAGE STILL RESOLVES EVERY TABLE THE PREDICATE FORM READ.
			// The id scan reads only the main table (and its labels), so on
			// its own an empty page would answer a missing leases or
			// wisp_comments table — neither of them optional (#5942) — with
			// an empty success. One by-IDs statement over a sentinel id
			// reaches the same tables the predicate form joined, so their
			// errors still propagate; whatever it returns is discarded.
			_, err := hydrateCountsByIDsInTx(ctx, tx, tables, []string{""}, includeWispReverseDeps, hydrationFor(filter))
			return nil, err
		}
		return hydrateCountsByIDsInTx(ctx, tx, tables, ids, includeWispReverseDeps, hydrationFor(filter))
	}

	whereClauses, args, err := BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + joinAnd(whereClauses)
	}
	limitSQL := ""
	if eff > 0 && !goSideSort {
		limitSQL = fmt.Sprintf("LIMIT %d", eff)
	}
	orderBy := sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i")
	out, err := runSearchQueryInTx(ctx, tx, tables, whereSQL, orderBy, limitSQL, args, includeWispReverseDeps, hydrationFor(filter))
	if err != nil {
		return nil, err
	}
	if goSideSort {
		// scanCountsRowsInTx drops nil-Issue rows, so the accessor is safe.
		out = goSideSortAndTrim(out, func(iwc *types.IssueWithCounts) string { return iwc.Issue.ID }, filter.SortDesc, eff)
	}
	return out, nil
}

//nolint:gosec // G201: SQL fragments are caller-built from hardcoded shapes
func runSearchQueryInTx(ctx context.Context, tx DBTX, tables FilterTables, whereSQL, orderBySQL, limitSQL string, args []interface{}, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	searchSQL, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, hyd)
	return scanCountsRowsInTx(ctx, tx, tables.Main, searchSQL, args, hyd)
}

// labelJoinsMatchPredicate reports whether the id scan's label JOINs select
// the rows the predicate form's label clauses do. They differ on an empty
// entry: sqlbuild.BuildLabelDrivenSearch drops empty strings from Labels and
// LabelsAny before it joins, while BuildIssueFilterClauses binds every entry,
// so the predicate form's `label = ?` bound to "" matches only a stored
// empty label.
// With one present, Labels {"hot", ""} would widen to every "hot" row and
// LabelsAny {"cold", ""} would lose the rows labeled "". Such a filter keeps
// the predicate form; the plain seam's own reading is left as it is.
func labelJoinsMatchPredicate(filter types.IssueFilter) bool {
	return !slices.Contains(filter.Labels, "") && !slices.Contains(filter.LabelsAny, "")
}

// hydrateCountsByIDsInTx hydrates the counts rows for ids through the by-IDs
// form of the counts mega-query and returns them in ids' order. ids must be
// distinct (an id scan's page); an id with no row is dropped.
//
// The ids are bound in sqlbuild.QueryBatchSize batches: the by-IDs form binds
// the list once per injection point (up to twelve), and Dolt's cost per
// IN-list placeholder is super-linear, so one statement over every id is far
// slower than several over a batch each (#6444). Each id lands in exactly one
// batch, so no row needs deduping across batches.
func hydrateCountsByIDsInTx(ctx context.Context, tx DBTX, tables FilterTables, ids []string, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	byID := make(map[string]*types.IssueWithCounts, len(ids))
	for start := 0; start < len(ids); start += sqlbuild.QueryBatchSize {
		end := min(start+sqlbuild.QueryBatchSize, len(ids))
		countsSQL, idArgs := sqlbuild.SearchCountsSQL(tables, ids[start:end], "", "", "", includeWispReverseDeps, hyd)
		rows, err := scanCountsRowsInTx(ctx, tx, tables.Main, countsSQL, idArgs, hyd)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r != nil && r.Issue != nil {
				byID[r.Issue.ID] = r
			}
		}
	}

	// Restore the order the id scan computed; the by-IDs form has none.
	ordered := make([]*types.IssueWithCounts, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered, nil
}

// scanCountsRowsInTx runs a prebuilt counts mega-query and hydrates each row
// through ScanReadyWorkRowWithCounts, deduping by issue ID. It is the single
// scan/dedupe loop shared by the predicate-form search path and the by-IDs
// ready-counts path.
//
//nolint:gosec // G201: query is builder-produced; user input rides ? placeholders.
func scanCountsRowsInTx(ctx context.Context, tx DBTX, mainTable, query string, args []interface{}, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search count %s: %w", mainTable, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, scanErr := ScanReadyWorkRowWithCounts(rows, hyd)
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
		return nil, fmt.Errorf("search count %s: rows: %w", mainTable, err)
	}
	return out, nil
}

// finishSearchIssuesWithCounts is the single terminal hook every
// SearchIssuesWithCountsInTx exit path routes through: it sorts the merged
// result, applies the caller-facing Limit trim, and only then enforces the
// defensive MaxRows cap (be-x42v) on the delivered count — mirroring
// searchInTx's trimToSearchLimit-before-EnforceMaxRowsCap ordering and
// finishReadyWorkWithCounts in ready_work_counts.go.
//
// Trim-before-cap matters for the merged (issues+wisps) case:
// runFilterSearchQueryInTx sizes each leg's SQL LIMIT independently via
// EffectiveSearchLimit(filter.Limit, filter.MaxRows), so the merged
// pre-trim slice can hold up to ~2x that per-leg bound — e.g. Limit=2,
// MaxRows=5, 3 rows in each table merges to 6, which would trip MaxRows
// even though the page actually handed back to the caller (trimmed to
// Limit=2) is well within the cap. Checking the cap against the delivered
// count instead avoids that false positive.
//
// This does not weaken cap enforcement for a single-source result: a lone
// query's LIMIT is already bounded to at most max(Limit, MaxRows+1), so its
// result never exceeds Limit when Limit>0 and the trim is a no-op there —
// only the two-source merge can produce more rows than Limit pre-trim, and
// a genuine overage (Limit=0, or Limit>MaxRows overage that survives the
// trim) still fires.
func finishSearchIssuesWithCounts(items []*types.IssueWithCounts, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	sortSearchIssuesWithCounts(items, filter.SortBy, filter.SortDesc)
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	if err := EnforceMaxRowsCap(len(items), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return items, nil
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
