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
		return finishSearchCountsPage(wisps, filter.Limit), nil
	}

	if filter.SkipWisps {
		out, err := r.runFilterSearchQuery(ctx, query, filter, issuesFilterTables, wispDepsExist)
		if err != nil {
			return domain.SearchCountsPage{}, err
		}
		return finishSearchCountsPage(out, filter.Limit), nil
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
		return finishSearchCountsPage(out, filter.Limit), nil
	}

	return r.searchUnionWithCounts(ctx, query, filter, wispDepsExist)
}

func (r *issueSQLRepositoryImpl) searchUnionWithCounts(ctx context.Context, query string, filter types.IssueFilter, wispDepsExist bool) (domain.SearchCountsPage, error) {
	iSub, iArgs, err := r.buildUnionSubquery(query, filter, issuesFilterTables, "i")
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (issues): %w", err)
	}
	wSub, wArgs, err := r.buildUnionSubquery(query, filter, wispsFilterTables, "w")
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (wisps): %w", err)
	}

	outerOrderBy := unionOrderBySQL(filter.SortBy, filter.SortDesc)
	outerLimit := limitOffsetSQL(filter.Limit, filter.Offset)

	//nolint:gosec // G201: subqueries built from hardcoded table names and ? placeholders.
	unionSQL := fmt.Sprintf("SELECT id, src FROM (%s UNION ALL %s) merged %s %s",
		iSub, wSub, outerOrderBy, outerLimit)

	args := make([]any, 0, len(iArgs)+len(wArgs))
	args = append(args, iArgs...)
	args = append(args, wArgs...)

	rows, err := r.runner.QueryContext(ctx, unionSQL, args...)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts: %w", err)
	}
	page, err := scanIDSrcPage(rows, true)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts: %w", err)
	}
	hasMore := page.trimToLimit(filter.Limit)

	issuesByID, err := r.fetchCountsByIDs(ctx, page.issueIDs, issuesFilterTables, wispDepsExist, hydrationFor(filter))
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (hydrate issues): %w", err)
	}
	wispsByID, err := r.fetchCountsByIDs(ctx, page.wispIDs, wispsFilterTables, true, hydrationFor(filter))
	if err != nil && !dberrors.IsTableNotExist(err) {
		return domain.SearchCountsPage{}, fmt.Errorf("search union with counts (hydrate wisps): %w", err)
	}

	out := reassembleBySrc(page.ordered, issuesByID, wispsByID)
	return domain.SearchCountsPage{Items: out, HasMore: hasMore}, nil
}

// hydrationFor reads the two hydration opt-outs off a search filter, matching
// the store-backed path's helper of the same name (internal/storage/issueops).
// Both bodies must read the same pair off the same filter or a caller that set
// one would get different columns from the two backends.
func hydrationFor(filter types.IssueFilter) sqlbuild.CountsHydration {
	return sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts}
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
		items, err := r.scanCountsQuery(ctx, tables, countsSQL, args)
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
	limitSQL := limitOffsetSQL(filter.Limit, filter.Offset)
	return r.runSearchQuery(ctx, tables, whereSQL, orderBy, limitSQL, args, includeWispReverseDeps, hydrationFor(filter))
}

//nolint:gosec // G201: SQL fragments are built from hardcoded table names and parameterized filters.
func (r *issueSQLRepositoryImpl) runSearchQuery(ctx context.Context, tables filterTables, whereSQL, orderBySQL, limitSQL string, args []any, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	searchSQL, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, hyd)
	return r.scanCountsQuery(ctx, tables, searchSQL, args)
}

// scanCountsQuery runs a prebuilt counts mega-query and hydrates each row,
// deduping by issue ID (mirrors issueops.scanCountsRowsInTx).
func (r *issueSQLRepositoryImpl) scanCountsQuery(ctx context.Context, tables filterTables, query string, args []any) ([]*types.IssueWithCounts, error) {
	rows, err := r.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search count %s: %w", tables.Main, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, scanErr := scanReadyWorkRowWithCounts(rows)
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
func scanReadyWorkRowWithCounts(rows *sql.Rows) (*types.IssueWithCounts, error) {
	return issueops.ScanReadyWorkRowWithCounts(rows)
}

func finishSearchCountsPage(items []*types.IssueWithCounts, limit int) domain.SearchCountsPage {
	trimmed, hasMore := applyN1Overflow(items, limit)
	return domain.SearchCountsPage{Items: trimmed, HasMore: hasMore}
}
