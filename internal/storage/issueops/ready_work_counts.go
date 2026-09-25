package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// readyHydrationFor reads a ready filter's hydration opt-outs, the twin of
// search_counts.go's hydrationFor. A WORK filter carries only one of the three:
// SkipLabels and SkipCounts are not carried onto the ready arm (see
// runReadyCountsInTx and issueops.ListRequest.SkipCounts), so ready work still
// hydrates labels and cardinalities unconditionally. Lite is carried, because
// it bounds the SIZE of a row the caller asked for rather than dropping a
// number the ready renderings print.
//
// It is one function per filter type rather than one shared read so that the
// asymmetry above is stated in code at the one place it applies, instead of
// being a field quietly absent from a struct literal at four call sites. That
// absence is what left this path on a hardcoded zero value.
func readyHydrationFor(filter types.WorkFilter) sqlbuild.CountsHydration {
	return sqlbuild.CountsHydration{Lite: filter.Lite}
}

// GetReadyWorkWithCountsInTx returns the ready-work page for filter, hydrated
// with counts. It is GetReadyWorkWithCountsAndTotalInTx without the total.
func GetReadyWorkWithCountsInTx(ctx context.Context, tx DBTX, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	items, _, err := getReadyWorkWithCountsInTx(ctx, tx, filter, false)
	return items, err
}

// GetReadyWorkWithCountsAndTotalInTx returns the ready-work page for filter
// together with the size of the whole ready set the page was cut from —
// identical to CountReadyWorkInTx(filter), and so to
// len(GetReadyWorkWithCountsInTx(filter with Limit=0)) — resolved inside the
// SAME statements that select the page.
//
// It exists because `bd ready --limit N` used to answer its "Showing N of M"
// with a second, separate pass (CountReadyWorkInTx in a new transaction,
// behind a second defer-wake sweep) that re-ran every probe and predicate the
// page had just run. Against a remote SQL server each statement is a
// sequential round trip, so asking for ONE row took longer than asking for
// all of them. Here the total rides the page's ID query as a window count
// (COUNT(*) OVER ()), evaluated over the full predicate before LIMIT, so a
// capped page costs no extra statement for its total. For an unbounded page
// (Limit <= 0) the total is simply the page's length.
//
// The two families are summed and their overlap (an ID ready as both an issue
// and a wisp, which the merge dedupes wisp-wins) subtracted, exactly as
// CountReadyWorkInTx does; the overlap statement runs only when the probe saw
// an ID present in both tables.
func GetReadyWorkWithCountsAndTotalInTx(ctx context.Context, tx DBTX, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	return getReadyWorkWithCountsInTx(ctx, tx, filter, true)
}

func getReadyWorkWithCountsInTx(ctx context.Context, tx DBTX, filter types.WorkFilter, wantTotal bool) ([]*types.IssueWithCounts, int, error) {
	// A capped page needs the collision fact to size the merged set; an
	// unbounded one is its own total.
	sized := wantTotal && filter.Limit > 0
	probe, err := probeReadyWorkInTx(ctx, tx, filter, sized)
	if err != nil {
		return nil, 0, fmt.Errorf("get ready work with counts: %w", err)
	}

	issuePreds, err := buildReadyWorkPredicatesFrom(filter, IssuesFilterTables, probe.inputs)
	if err != nil {
		return nil, 0, err
	}
	out, issueTotal, err := runReadyCountsInTx(ctx, tx, IssuesFilterTables, filter.Limit, issuePreds, probe.wispDepsExist, readyHydrationFor(filter), sized)
	if err != nil {
		return nil, 0, err
	}

	finish := func(items []*types.IssueWithCounts, total int) ([]*types.IssueWithCounts, int, error) {
		if !sized {
			// Unbounded: every ready row is on the page (the MaxRows cap
			// refuses rather than truncates), so the page IS the set.
			total = len(items)
		}
		items, err := finishReadyWorkWithCounts(items, filter)
		if err != nil {
			return nil, 0, err
		}
		return items, total, nil
	}

	if !probe.readsWisps() {
		return finish(out, issueTotal)
	}

	wispPreds, err := buildReadyWorkPredicatesFrom(filter, WispsFilterTables, probe.inputs)
	if err != nil {
		return nil, 0, err
	}
	wisps, wispTotal, err := runReadyCountsInTx(ctx, tx, WispsFilterTables, filter.Limit, wispPreds, true, readyHydrationFor(filter), sized)
	if err != nil {
		if missingOptionalWispTable(err) {
			return finish(out, issueTotal)
		}
		return nil, 0, err
	}
	if len(wisps) == 0 {
		return finish(out, issueTotal)
	}

	total := issueTotal + wispTotal
	if sized && probe.idCollision && issueTotal > 0 {
		overlap, err := countReadyOverlapInTx(ctx, tx, issuePreds, wispPreds)
		if err != nil {
			return nil, 0, fmt.Errorf("get ready work with counts: overlap: %w", err)
		}
		total -= overlap
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
	sortIssuesWithCountsByPolicy(kept, filter.SortPolicy)
	return finish(kept, total)
}

// finishReadyWorkWithCounts is the terminal hook every
// GetReadyWorkWithCountsInTx exit path routes through: it applies the
// caller-facing Limit trim and then enforces the defensive MaxRows cap
// (be-x42v) on the delivered count — mirroring GetReadyWorkInTx's
// non-counts path, where mergeReadyWisps already trims the merged
// issues+wisps set to Limit before EnforceMaxRowsCap runs on it.
//
// Trim-before-cap matters specifically for the merged (issues+wisps) case:
// each table's query is independently bounded by
// EffectiveSearchLimit(filter.Limit, filter.MaxRows), so with
// --include-ephemeral the merged pre-trim slice can hold up to ~2x that
// per-table bound — e.g. Limit=2, MaxRows=3, two rows ready in each table
// merges to 4, which trips MaxRows even though the actually-delivered page
// (trimmed to Limit=2) is well within the cap. Checking the cap against the
// delivered/post-trim count instead avoids that false positive.
//
// This does not weaken cap enforcement for the single-table (no wisps, or
// wisps empty/unmerged) paths: EffectiveSearchLimit already bounds a lone
// query's LIMIT to at most max(Limit, MaxRows+1), so a single source's
// result never exceeds Limit when Limit>0 and the trim is a no-op there —
// only the two-source merge can produce more rows than Limit pre-trim.
func finishReadyWorkWithCounts(items []*types.IssueWithCounts, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	if err := EnforceMaxRowsCap(len(items), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return items, nil
}

// runReadyCountsInTx renders the ready-work counts mega-query for one table
// family, pushing the page down when the caller bounded it.
//
// For a bounded page (limit > 0) it first resolves the ≤limit ready IDs with the
// cheap indexed ID query (the same SELECT id … the non-counts GetReadyWork path
// uses), then hydrates the counts constrained to exactly those IDs. This is what
// de-quadratics the query: the reverse-blocker subquery rc joins on
// COALESCE(depends_on_issue_id, …), an expression the pure-Go GMS analyzer
// cannot auto-index, so the planner re-scans rc's whole materialization once per
// driver row. Bounding the driver to the page turns that O(candidates × blockers)
// scan into O(page × blockers). Each per-issue count is a function of the full
// dependency graph, not of the candidate set, so constraining the driver leaves
// every emitted count byte-identical to the unbounded mega-query; the page is
// the same top-N the ORDER BY … LIMIT selected because the ready order ends in a
// unique `id` tiebreak.
//
// The page IDs are chunked into sqlbuild.QueryBatchSize batches so a large page
// stays within every backend's per-statement placeholder limit (the by-IDs form
// binds the page up to eight times) without falling back to the quadratic query.
//
// For limit <= 0 (unbounded) there is no page to push down, so it runs the
// predicate-form mega-query unchanged.
//
// withTotal (bounded pages only) also returns the number of rows the family's
// ready predicate admits, as a window count over the same ID query:
// COUNT(*) OVER () is evaluated over every row the WHERE admits, before ORDER
// BY … LIMIT cut the page, so it is exactly `SELECT COUNT(*) … WHERE` — the
// count CountReadyWorkInTx takes — at no extra statement. The ID query has no
// DISTINCT, so the window counts rows and IDs alike. Otherwise the returned
// total is len(result).
//
// Both callers pass readyHydrationFor(filter), which carries Lite and nothing
// else: ready work always hydrates labels and cardinalities, because
// types.WorkFilter carries neither opt-out — the projection that builds it
// drops both — and issueops.ListRequest says so where a caller reads it,
// SkipLabels and SkipCounts are not carried onto the ReadyFlag arm.
//
//nolint:gosec // G201: whereSQL/orderBySQL/limitSQL are hardcoded fragments; user input rides ? placeholders.
func runReadyCountsInTx(ctx context.Context, tx DBTX, tables FilterTables, limit int, preds *readyWorkPredicates, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration, withTotal bool) ([]*types.IssueWithCounts, int, error) {
	if limit <= 0 {
		out, err := runSearchQueryInTx(ctx, tx, tables, preds.whereSQL, preds.orderBySQL, preds.limitSQL, preds.args, includeWispReverseDeps, hyd)
		return out, len(out), err
	}

	var pageIDs []string
	var total int
	var err error
	if withTotal {
		idQuery := fmt.Sprintf("SELECT id, COUNT(*) OVER () FROM %s %s %s %s", tables.Main, preds.whereSQL, preds.orderBySQL, preds.limitSQL)
		pageIDs, total, err = queryReadyIDPageWithTotal(ctx, tx, idQuery, preds.args)
	} else {
		idQuery := fmt.Sprintf("SELECT id FROM %s %s %s %s", tables.Main, preds.whereSQL, preds.orderBySQL, preds.limitSQL)
		pageIDs, err = queryReadyIssueIDPage(ctx, tx, idQuery, preds.args)
		total = len(pageIDs)
	}
	if err != nil {
		return nil, 0, err
	}
	if len(pageIDs) == 0 {
		return nil, 0, nil
	}

	// Hydrate the counts for the resolved page, chunking the IN-list. The page
	// IDs are already distinct, so a per-chunk scan needs no cross-chunk dedup.
	byID := make(map[string]*types.IssueWithCounts, len(pageIDs))
	for start := 0; start < len(pageIDs); start += sqlbuild.QueryBatchSize {
		end := start + sqlbuild.QueryBatchSize
		if end > len(pageIDs) {
			end = len(pageIDs)
		}
		countsSQL, idArgs := sqlbuild.SearchCountsSQL(tables, pageIDs[start:end], "", "", "", includeWispReverseDeps, hyd)
		rows, scanErr := scanCountsRowsInTx(ctx, tx, tables.Main, countsSQL, idArgs, hyd)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		for _, r := range rows {
			if r != nil && r.Issue != nil {
				byID[r.Issue.ID] = r
			}
		}
	}

	// Restore the ready order the ID query already computed so the result stays
	// identical to the unbounded mega-query's ORDER BY … LIMIT.
	ordered := make([]*types.IssueWithCounts, 0, len(pageIDs))
	for _, id := range pageIDs {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered, total, nil
}

// queryReadyIDPageWithTotal runs a `SELECT id, COUNT(*) OVER () …` page query
// and returns the page IDs with the window total. An empty page carries no
// row to read the total from, and means the predicate admitted nothing.
func queryReadyIDPageWithTotal(ctx context.Context, tx DBTX, query string, args []interface{}) ([]string, int, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get ready work: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	var total int64
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &total); err != nil {
			return nil, 0, fmt.Errorf("get ready work: scan id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("get ready work: rows: %w", err)
	}
	return ids, int(total), nil
}

// CountReadyWorkInTx returns the number of ready-work items — identical to
// len(GetReadyWorkWithCountsInTx(filter with Limit=0)) — without materializing
// the counts mega-query. The ready set is a union of the issues and wisps that
// match the ready predicate, so it sizes each family with an indexed COUNT(*)
// over that predicate and subtracts the overlap (IDs present in both ready
// sets, which GetReadyWorkWithCountsInTx dedupes wisp-wins). It never re-runs
// the mega-query, so a single wisp no longer disables the fast path.
//
// Statement budget: the shared probe (one statement; plus one for the
// deferred-parent children when a future-deferred parent exists), then both
// family counts in ONE statement of scalar subqueries, then the overlap only
// when the probe saw an ID present in both tables. `bd ready` itself sizes a
// capped --json page in-band (GetReadyWorkWithCountsAndTotalInTx); this backs
// the ReadyCounter role and the human-readable "Showing X of N".
func CountReadyWorkInTx(ctx context.Context, tx DBTX, filter types.WorkFilter) (int, error) {
	countFilter := filter
	countFilter.Limit = 0

	probe, err := probeReadyWorkInTx(ctx, tx, countFilter, true)
	if err != nil {
		return 0, fmt.Errorf("count ready work: %w", err)
	}

	issuePreds, err := buildReadyWorkPredicatesFrom(countFilter, IssuesFilterTables, probe.inputs)
	if err != nil {
		return 0, err
	}

	// Mirror GetReadyWorkWithCountsInTx's wisp gating: an empty/missing wisps
	// table or absent wisp_dependencies means the ready set is issues-only.
	if !probe.readsWisps() {
		issueCount, err := countReadyPredicateInTx(ctx, tx, "issues", issuePreds.whereSQL, issuePreds.whereArgs)
		if err != nil {
			return 0, fmt.Errorf("count ready work: issues: %w", err)
		}
		return issueCount, nil
	}

	wispPreds, err := buildReadyWorkPredicatesFrom(countFilter, WispsFilterTables, probe.inputs)
	if err != nil {
		return 0, err
	}
	issueCount, wispCount, err := countReadyFamiliesInTx(ctx, tx, issuePreds, wispPreds)
	if err != nil {
		if !missingOptionalWispTable(err) {
			return 0, fmt.Errorf("count ready work: %w", err)
		}
		// A wisp plane the database may legitimately lack: issues-only, and
		// the issues count has to be taken on its own.
		issueCount, err = countReadyPredicateInTx(ctx, tx, "issues", issuePreds.whereSQL, issuePreds.whereArgs)
		if err != nil {
			return 0, fmt.Errorf("count ready work: issues: %w", err)
		}
		return issueCount, nil
	}
	if wispCount == 0 || issueCount == 0 || !probe.idCollision {
		return issueCount + wispCount, nil
	}

	overlap, err := countReadyOverlapInTx(ctx, tx, issuePreds, wispPreds)
	if err != nil {
		return 0, fmt.Errorf("count ready work: overlap: %w", err)
	}
	return issueCount + wispCount - overlap, nil
}

// countReadyFamiliesInTx counts both families' ready rows in one statement of
// two scalar subqueries (portable: no FROM-less dialect extension beyond
// SELECT of subqueries, which every supported backend accepts).
//
//nolint:gosec // G201: whereSQL fragments are hardcoded; user input rides ? placeholders.
func countReadyFamiliesInTx(ctx context.Context, tx DBTX, issuePreds, wispPreds *readyWorkPredicates) (int, int, error) {
	q := fmt.Sprintf("SELECT (SELECT COUNT(*) FROM issues %s), (SELECT COUNT(*) FROM wisps %s)", issuePreds.whereSQL, wispPreds.whereSQL)
	args := make([]interface{}, 0, len(issuePreds.whereArgs)+len(wispPreds.whereArgs))
	args = append(args, issuePreds.whereArgs...)
	args = append(args, wispPreds.whereArgs...)
	var issues, wisps int
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&issues, &wisps); err != nil {
		return 0, 0, err
	}
	return issues, wisps, nil
}

// countReadyPredicateInTx counts the rows in one table family that match the
// ready predicate. whereSQL already begins with "WHERE " and whereArgs binds
// only its placeholders (no ORDER BY params).
//
//nolint:gosec // G201: whereSQL is hardcoded fragments; user input rides ? placeholders.
func countReadyPredicateInTx(ctx context.Context, tx DBTX, table, whereSQL string, whereArgs []interface{}) (int, error) {
	var n int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s %s", table, whereSQL), whereArgs...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// countReadyOverlapInTx counts the IDs that satisfy the ready predicate as both
// an issue and a wisp. GetReadyWorkWithCountsInTx keeps the wisp row and drops
// the issue row for such an ID, so |ready| = issueCount + wispCount - overlap.
//
//nolint:gosec // G201: whereSQL fragments are hardcoded; user input rides ? placeholders.
func countReadyOverlapInTx(ctx context.Context, tx DBTX, issuePreds, wispPreds *readyWorkPredicates) (int, error) {
	q := fmt.Sprintf("SELECT COUNT(*) FROM issues %s AND id IN (SELECT id FROM wisps %s)", issuePreds.whereSQL, wispPreds.whereSQL)
	args := make([]interface{}, 0, len(issuePreds.whereArgs)+len(wispPreds.whereArgs))
	args = append(args, issuePreds.whereArgs...)
	args = append(args, wispPreds.whereArgs...)
	var n int
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func sortIssuesWithCountsByPolicy(items []*types.IssueWithCounts, policy types.SortPolicy) {
	if len(items) <= 1 {
		return
	}
	issues := make([]*types.Issue, 0, len(items))
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		issues = append(issues, item.Issue)
	}
	if len(issues) != len(items) {
		return
	}
	sortReadyIssues(issues, policy)
	byID := make(map[string]int, len(issues))
	for i, iss := range issues {
		byID[iss.ID] = i
	}
	sorted := make([]*types.IssueWithCounts, len(items))
	for _, item := range items {
		sorted[byID[item.Issue.ID]] = item
	}
	copy(items, sorted)
}

// ScanReadyWorkRowWithCounts scans one row of the counts mega-query
// (sqlbuild.SearchCountsSQL): the issue columns followed by labels JSON,
// dep/rdep/comment counts, parent ID, and dependency JSON. Exported so the
// domain/db stack hydrates counts rows through the exact same code path.
//
// It takes the whole hydration rather than a bool because hyd is what chose
// the SELECT list on the other side: reading the same value here is what keeps
// the two in agreement, where a separately-passed flag could disagree with the
// query it is scanning.
func ScanReadyWorkRowWithCounts(rows *sql.Rows, hyd sqlbuild.CountsHydration) (*types.IssueWithCounts, error) {
	var labelsJSON, depsJSON sql.NullString
	var parentID sql.NullString
	var depCount, rdepCount, commentCount sql.NullInt64

	composite := &compositeReadyRow{
		row: rows,
		extra: []any{
			&labelsJSON,
			&depCount,
			&rdepCount,
			&commentCount,
			&parentID,
			&depsJSON,
		},
	}
	scan := ScanIssueFrom
	if hyd.Lite {
		scan = ScanIssueLiteFrom
	}
	issue, err := scan(composite)
	if err != nil {
		return nil, fmt.Errorf("scan issue with counts: %w", err)
	}

	if labelsJSON.Valid && labelsJSON.String != "" {
		var labels []string
		if err := json.Unmarshal([]byte(labelsJSON.String), &labels); err != nil {
			return nil, fmt.Errorf("scan issue with counts: parse labels_json: %w", err)
		}
		sort.Strings(labels)
		issue.Labels = labels
	}

	if depsJSON.Valid && depsJSON.String != "" {
		var deps []*types.Dependency
		if err := json.Unmarshal([]byte(depsJSON.String), &deps); err != nil {
			return nil, fmt.Errorf("scan issue with counts: parse deps_json: %w", err)
		}
		issue.Dependencies = deps
	}

	iwc := &types.IssueWithCounts{
		Issue:           issue,
		DependencyCount: int(depCount.Int64),
		DependentCount:  int(rdepCount.Int64),
		CommentCount:    int(commentCount.Int64),
	}
	if parentID.Valid {
		s := parentID.String
		iwc.Parent = &s
	}
	return iwc, nil
}

type compositeReadyRow struct {
	row   *sql.Rows
	extra []any
}

func (c *compositeReadyRow) Scan(dest ...any) error {
	combined := make([]any, 0, len(dest)+len(c.extra))
	combined = append(combined, dest...)
	combined = append(combined, c.extra...)
	return c.row.Scan(combined...)
}
