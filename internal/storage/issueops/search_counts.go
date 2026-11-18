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

// runFilterSearchQueryInTx answers one counts search leg in two steps: it
// selects the surviving, ordered page IDs with the narrow id projection
// searchTableInTxT runs for SearchIssueIDsInTx — label-driven JOINs, residual
// filters, EffectiveSearchLimit and the Go-side sort-before-bound rule all
// included — and then hydrates ONLY those IDs through the by-IDs counts form
// (GH#4370). The pre-change shape ran the counts mega-query in predicate form
// over the whole matching set: its aggregate LEFT JOINs computed
// whole-side-table counts — and the reverse-blocker's COALESCE self-join
// re-scanned its full materialization once per driver row — for every row the
// sort then kept, so a bounded page over a ~100k-issue corpus paid for the
// whole side-table world and seized under concurrent write churn while point
// reads stayed fast. hydrateCountsByIDsInTx never issues the predicate form; an
// empty page means no counts query at all.
//
// The predicate form remains in sqlbuild.SearchCountsSQL as the parity oracle
// for tests (runSearchQueryInTx below renders it; only the ready seam's
// unbounded branch still calls that wrapper).
//
// WHY THE ID SELECTION MAY BORROW the label-driven JOIN plan the plain search
// seam uses: the JOIN-and-DISTINCT rewrite answers the same row set the
// IN-subquery clauses selected, and the page bound rides the ID query in full
// — a wrong leg could only answer a different SET, never a different page of
// the same set, and the parity matrix tests compare this path against the
// predicate oracle row for row.
func runFilterSearchQueryInTx(ctx context.Context, tx DBTX, query string, filter types.IssueFilter, tables FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	ids, err := searchTableInTxT(ctx, tx, query, filter, tables, idProjection)
	if err != nil {
		return nil, err
	}
	return hydrateCountsByIDsInTx(ctx, tx, tables, ids, includeWispReverseDeps, hydrationFor(filter))
}

// hydrateCountsByIDsInTx hydrates counts rows for the given ordered IDs through
// the by-IDs form of the counts mega-query and restores the caller's order. It
// is the one hydration loop the store-backed counts seams share: search here,
// and ready's page (bounded already, unbounded since the GH#4370 rewrite).
//
// THE EMPTY-ID CASE RETURNS WITHOUT QUERYING. The by-IDs form is selected by
// "ids non-empty"; handing it an empty slice would silently select the
// predicate form, whose aggregates read the WHOLE side table — the exact shape
// this helper exists to keep off the hot path.
//
// The IDs are chunked at sqlbuild.QueryBatchSize so the by-IDs form's
// up-to-twelvefold placeholder binding stays within every backend's
// per-statement limit. The page IDs a caller hands here are already distinct
// (searchTableInTxT and the ready ID queries dedupe), so no cross-batch dedup
// is needed; scanCountsRowsInTx's in-batch dedup stays as a defensive no-op.
//
// A hydrated row missing from the map is skipped, never an error: it answers
// the page without the row exactly as the predicate scan would have (both
// dedupe rather than fail on a partially-visible page).
func hydrateCountsByIDsInTx(ctx context.Context, tx DBTX, tables FilterTables, ids []string, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	byID := make(map[string]*types.IssueWithCounts, len(ids))
	for start := 0; start < len(ids); start += sqlbuild.QueryBatchSize {
		end := start + sqlbuild.QueryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
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
	ordered := make([]*types.IssueWithCounts, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered, nil
}

//nolint:gosec // G201: SQL fragments are caller-built from hardcoded shapes
func runSearchQueryInTx(ctx context.Context, tx DBTX, tables FilterTables, whereSQL, orderBySQL, limitSQL string, args []interface{}, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	searchSQL, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, hyd)
	return scanCountsRowsInTx(ctx, tx, tables.Main, searchSQL, args, hyd)
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
