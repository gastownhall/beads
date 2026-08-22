package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func (r *issueSQLRepositoryImpl) searchAcrossIssuesAndWispsWithCounts(ctx context.Context, query string, filter types.IssueFilter) (domain.SearchCountsPage, error) {
	wispDepsExist, err := r.optionalTableExists(ctx, "wisp_dependencies")
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search issues with counts: wisp dependency probe: %w", err)
	}

	if filter.Ephemeral != nil && *filter.Ephemeral {
		empty, probeErr := r.wispsTableEmptyOrMissing(ctx)
		if probeErr != nil {
			return domain.SearchCountsPage{}, fmt.Errorf("search issues with counts: ephemeral wisp probe: %w", probeErr)
		}
		if empty || !wispDepsExist {
			return domain.SearchCountsPage{}, nil
		}
		wisps, err := r.runFilterSearchQuery(ctx, query, filter, wispsFilterTables, true)
		if err != nil {
			return domain.SearchCountsPage{}, err
		}
		return finishSearchCountsPage(wisps, filter)
	}

	if filter.SkipWisps {
		out, err := r.runFilterSearchQuery(ctx, query, filter, issuesFilterTables, wispDepsExist)
		if err != nil {
			return domain.SearchCountsPage{}, err
		}
		return finishSearchCountsPage(out, filter)
	}

	empty, probeErr := r.wispsTableEmptyOrMissing(ctx)
	if probeErr != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search issues with counts: wisp probe: %w", probeErr)
	}
	if empty || !wispDepsExist {
		out, err := r.runFilterSearchQuery(ctx, query, filter, issuesFilterTables, wispDepsExist)
		if err != nil {
			return domain.SearchCountsPage{}, err
		}
		return finishSearchCountsPage(out, filter)
	}

	return r.searchUnionWithCounts(ctx, query, filter, wispDepsExist)
}

func (r *issueSQLRepositoryImpl) searchUnionWithCounts(ctx context.Context, query string, filter types.IssueFilter, wispDepsExist bool) (domain.SearchCountsPage, error) {
	outerOrderBy := unionOrderBySQL(filter.SortBy, filter.SortDesc)
	window := searchWindowForFilter(filter)
	legWindow := legWindowSQL(outerOrderBy, window)

	iSub, iArgs, err := r.buildUnionSubquery(query, filter, issuesFilterTables, "i", legWindow)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (issues): %w", err)
	}
	wSub, wArgs, err := r.buildUnionSubquery(query, filter, wispsFilterTables, "w", legWindow)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (wisps): %w", err)
	}

	// EACH LEG IS PARENTHESIZED, and it is not decoration. A leg that carries
	// its own ORDER BY and LIMIT (legWindowSQL) is a syntax error inside a bare
	// UNION ALL — the engine reads the clause as belonging to the union — so the
	// parentheses are what let the window be pushed down at all.
	//nolint:gosec // G201: subqueries built from hardcoded table names and ? placeholders.
	unionSQL := fmt.Sprintf("SELECT id, src FROM ((%s) UNION ALL (%s)) merged %s %s",
		iSub, wSub, outerOrderBy, window.sql)

	args := make([]any, 0, len(iArgs)+len(wArgs))
	args = append(args, iArgs...)
	args = append(args, wArgs...)

	rows, err := r.runner.QueryContext(ctx, unionSQL, args...)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts: %w", err)
	}
	page, err := scanIDSrcPage(rows)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts: %w", err)
	}
	page.sortGoSide(filter.SortBy, filter.SortDesc)
	hasMore, err := page.finishWindow(window)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}

	issuesByID, err := r.fetchCountsByIDs(ctx, page.issueIDs, issuesFilterTables, wispDepsExist, hydrationFor(filter))
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (hydrate issues): %w", err)
	}
	wispsByID, err := r.fetchCountsByIDs(ctx, page.wispIDs, wispsFilterTables, true, hydrationFor(filter))
	if err != nil && !missingOptionalWispTable(err) {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (hydrate wisps): %w", err)
	}

	out := reassembleBySrc(page.ordered, issuesByID, wispsByID)
	return domain.SearchCountsPage{Items: out, HasMore: hasMore}, nil
}

// hydrationFor reads the hydration opt-outs off a search filter, matching the
// store-backed path's helper of the same name (internal/storage/issueops).
// Both bodies must read the same fields off the same filter or a caller that
// set one would get different columns from the two backends.
func hydrationFor(filter types.IssueFilter) sqlbuild.CountsHydration {
	return sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts, Lite: filter.Lite}
}

// readyHydrationFor is the WORK-filter twin, matching
// issueops.readyHydrationFor. See it for why a ready filter carries Lite and
// neither of the other two.
func readyHydrationFor(filter types.WorkFilter) sqlbuild.CountsHydration {
	return sqlbuild.CountsHydration{Lite: filter.Lite}
}

// fetchCountsByIDs hydrates counts rows for explicit IDs via the by-IDs form
// of the counts mega-query, which also constrains every aggregate subquery to
// the page (row order is restored by the caller from the union page). It must
// not hand-build an id predicate for the predicate form: that form renders
// whereSQL inside a derived subquery, so a caller-written "i."-qualified
// clause would silently couple to the subquery's internal alias. The IDs are
// chunked so the by-IDs form's up-to-eightfold placeholder binding stays
// within per-statement limits (mirrors issueops.runReadyCountsInTx).
func (r *issueSQLRepositoryImpl) fetchCountsByIDs(ctx context.Context, ids []string, tables filterTables, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) (map[string]*types.IssueWithCounts, error) {
	out := make(map[string]*types.IssueWithCounts, len(ids))
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		countsSQL, args := sqlbuild.SearchCountsSQL(tables, ids[start:end], "", "", "", includeWispReverseDeps, hyd)
		items, err := r.scanCountsQuery(ctx, tables, countsSQL, args, hyd)
		if err != nil {
			return nil, err
		}
		for _, iwc := range items {
			if iwc == nil || iwc.Issue == nil {
				continue
			}
			out[iwc.Issue.ID] = iwc
		}
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) runFilterSearchQuery(ctx context.Context, query string, filter types.IssueFilter, tables filterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	whereClauses, args, err := buildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	orderBy := orderBySQL(filter.SortBy, filter.SortDesc, "i")
	return r.runSearchQuery(ctx, tables, whereSQL, orderBy, searchWindowForFilter(filter).sql, args, includeWispReverseDeps, hydrationFor(filter))
}

//nolint:gosec // G201: SQL fragments are built from hardcoded table names and parameterized filters.
func (r *issueSQLRepositoryImpl) runSearchQuery(ctx context.Context, tables filterTables, whereSQL, orderBySQL, limitSQL string, args []any, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	searchSQL, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, hyd)
	return r.scanCountsQuery(ctx, tables, searchSQL, args, hyd)
}

// scanCountsQuery runs a prebuilt counts mega-query and hydrates each row,
// deduping by issue ID (mirrors issueops.scanCountsRowsInTx).
func (r *issueSQLRepositoryImpl) scanCountsQuery(ctx context.Context, tables filterTables, query string, args []any, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	rows, err := r.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search count %s: %w", tables.Main, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, scanErr := scanReadyWorkRowWithCounts(rows, hyd)
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

func (r *issueSQLRepositoryImpl) optionalTableExists(ctx context.Context, table string) (bool, error) {
	var probe int
	//nolint:gosec // G201: table is a hardcoded constant from caller (issues, wisps, dependencies, wisp_dependencies, ...).
	err := r.runner.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s LIMIT 1", table)).Scan(&probe)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case dberrors.IsTableNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// scanReadyWorkRowWithCounts delegates to the classic implementation so both
// stacks hydrate counts rows identically (same delegation pattern as
// scanIssue -> issueops.ScanIssueFrom).
func scanReadyWorkRowWithCounts(rows *sql.Rows, hyd sqlbuild.CountsHydration) (*types.IssueWithCounts, error) {
	return issueops.ScanReadyWorkRowWithCounts(rows, hyd)
}

// finishSearchCountsPage closes the window runFilterSearchQuery opened. It
// rebuilds it from the same filter rather than being handed it, so the two
// halves cannot be given different numbers. Like the plain per-table seam, it
// establishes a Go-side sort's order before the trim (sortRowsGoSide) — the
// counts query renders no ORDER BY for such keys, so its rows arrive
// engine-ordered.
func finishSearchCountsPage(items []*types.IssueWithCounts, filter types.IssueFilter) (domain.SearchCountsPage, error) {
	sortRowsGoSide(items, func(iwc *types.IssueWithCounts) string { return iwc.Issue.ID }, filter.SortBy, filter.SortDesc)
	trimmed, hasMore, err := finishWindow(items, searchWindowForFilter(filter))
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	return domain.SearchCountsPage{Items: trimmed, HasMore: hasMore}, nil
}
