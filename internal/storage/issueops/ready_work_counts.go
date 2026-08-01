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

func GetReadyWorkWithCountsInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp dependency probe: %w", err)
	}

	issuePreds, err := buildReadyWorkPredicates(ctx, tx, filter, IssuesFilterTables)
	if err != nil {
		return nil, err
	}
	out, err := runReadyCountsInTx(ctx, tx, IssuesFilterTables, filter.Limit, issuePreds, wispDepsExist, false)
	if err != nil {
		return nil, err
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return finishReadyWorkWithCounts(out, filter)
	}
	if !wispDepsExist {
		return finishReadyWorkWithCounts(out, filter)
	}

	wispPreds, err := buildReadyWorkPredicates(ctx, tx, filter, WispsFilterTables)
	if err != nil {
		return nil, err
	}
	wisps, err := runReadyCountsInTx(ctx, tx, WispsFilterTables, filter.Limit, wispPreds, true, false)
	if err != nil {
		if isTableNotExistError(err) {
			return finishReadyWorkWithCounts(out, filter)
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return finishReadyWorkWithCounts(out, filter)
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
	return finishReadyWorkWithCounts(kept, filter)
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
//nolint:gosec // G201: whereSQL/orderBySQL/limitSQL are hardcoded fragments; user input rides ? placeholders.
func runReadyCountsInTx(ctx context.Context, tx *sql.Tx, tables FilterTables, limit int, preds *readyWorkPredicates, includeWispReverseDeps, skipLabels bool) ([]*types.IssueWithCounts, error) {
	if limit <= 0 {
		return runSearchQueryInTx(ctx, tx, tables, preds.whereSQL, preds.orderBySQL, preds.limitSQL, preds.args, includeWispReverseDeps, skipLabels)
	}

	idQuery := fmt.Sprintf("SELECT id FROM %s %s %s %s", tables.Main, preds.whereSQL, preds.orderBySQL, preds.limitSQL)
	pageIDs, err := queryReadyIssueIDPage(ctx, tx, idQuery, preds.args)
	if err != nil {
		return nil, err
	}
	if len(pageIDs) == 0 {
		return nil, nil
	}

	// Hydrate the counts for the resolved page, chunking the IN-list. The page
	// IDs are already distinct, so a per-chunk scan needs no cross-chunk dedup.
	byID := make(map[string]*types.IssueWithCounts, len(pageIDs))
	for start := 0; start < len(pageIDs); start += sqlbuild.QueryBatchSize {
		end := start + sqlbuild.QueryBatchSize
		if end > len(pageIDs) {
			end = len(pageIDs)
		}
		countsSQL, idArgs := sqlbuild.SearchCountsSQL(tables, pageIDs[start:end], "", "", "", includeWispReverseDeps, skipLabels)
		rows, scanErr := scanCountsRowsInTx(ctx, tx, tables.Main, countsSQL, idArgs)
		if scanErr != nil {
			return nil, scanErr
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
	return ordered, nil
}

// CountReadyWorkInTx returns the number of ready-work items — identical to
// len(GetReadyWorkWithCountsInTx(filter with Limit=0)) — without materializing
// the counts mega-query. The ready set is a union of the issues and wisps that
// match the ready predicate, so it sizes each family with a single indexed
// COUNT(*) over that predicate and subtracts the overlap (IDs present in both
// ready sets, which GetReadyWorkWithCountsInTx dedupes wisp-wins). It never
// re-runs the mega-query, so a single wisp no longer disables the fast path.
// This backs the "Showing X of N" total `bd ready` prints when the page is
// capped.
func CountReadyWorkInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) (int, error) {
	countFilter := filter
	countFilter.Limit = 0

	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return 0, fmt.Errorf("count ready work: wisp dependency probe: %w", err)
	}

	issuePreds, err := buildReadyWorkPredicates(ctx, tx, countFilter, IssuesFilterTables)
	if err != nil {
		return 0, err
	}
	issueCount, err := countReadyPredicateInTx(ctx, tx, "issues", issuePreds.whereSQL, issuePreds.whereArgs)
	if err != nil {
		return 0, fmt.Errorf("count ready work: issues: %w", err)
	}

	// Mirror GetReadyWorkWithCountsInTx's wisp gating: an empty/missing wisps
	// table or absent wisp_dependencies means the ready set is issues-only.
	empty, err := wispsTableEmptyOrMissingInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("count ready work: wisp probe: %w", err)
	}
	if empty || !wispDepsExist {
		return issueCount, nil
	}

	wispPreds, err := buildReadyWorkPredicates(ctx, tx, countFilter, WispsFilterTables)
	if err != nil {
		return 0, err
	}
	wispCount, err := countReadyPredicateInTx(ctx, tx, "wisps", wispPreds.whereSQL, wispPreds.whereArgs)
	if err != nil {
		if isTableNotExistError(err) {
			return issueCount, nil
		}
		return 0, fmt.Errorf("count ready work: wisps: %w", err)
	}
	if wispCount == 0 {
		return issueCount, nil
	}

	overlap, err := countReadyOverlapInTx(ctx, tx, issuePreds, wispPreds)
	if err != nil {
		return 0, fmt.Errorf("count ready work: overlap: %w", err)
	}
	return issueCount + wispCount - overlap, nil
}

// countReadyPredicateInTx counts the rows in one table family that match the
// ready predicate. whereSQL already begins with "WHERE " and whereArgs binds
// only its placeholders (no ORDER BY params).
//
//nolint:gosec // G201: whereSQL is hardcoded fragments; user input rides ? placeholders.
func countReadyPredicateInTx(ctx context.Context, tx *sql.Tx, table, whereSQL string, whereArgs []interface{}) (int, error) {
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
func countReadyOverlapInTx(ctx context.Context, tx *sql.Tx, issuePreds, wispPreds *readyWorkPredicates) (int, error) {
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
// (sqlbuild.SearchCountsSQL): IssueSelectColumns followed by labels JSON,
// dep/rdep/comment counts, parent ID, and dependency JSON. Exported so the
// domain/db stack hydrates counts rows through the exact same code path.
func ScanReadyWorkRowWithCounts(rows *sql.Rows) (*types.IssueWithCounts, error) {
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
	issue, err := ScanIssueFrom(composite)
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
