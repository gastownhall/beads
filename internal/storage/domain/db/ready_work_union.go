package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (r *issueSQLRepositoryImpl) getReadyWorkIDPage(ctx context.Context, filter types.WorkFilter) (idSrcPage, bool, error) {
	issuePreds, err := r.buildReadyWorkPredicates(ctx, filter, issuesFilterTables)
	if err != nil {
		return idSrcPage{}, false, err
	}

	wispsEmpty, probeErr := r.wispsTableEmptyOrMissing(ctx)
	if probeErr != nil {
		return idSrcPage{}, false, fmt.Errorf("ready work union: wisps probe: %w", probeErr)
	}

	subqueries := make([]string, 0, 2)
	var allArgs []any

	//nolint:gosec // G201: whereSQL composed from hardcoded fragments and ? placeholders.
	issueSub := fmt.Sprintf(
		"SELECT id, 'i' AS src, %s FROM issues %s",
		unionSortColumnsSQL, issuePreds.whereSQL,
	)
	subqueries = append(subqueries, issueSub)
	allArgs = append(allArgs, issuePreds.args...)

	if !wispsEmpty {
		// The ephemeral predicate applies to wisps too: the wisps table also
		// holds non-ephemeral NoHistory beads (ephemeral=0), so forcing
		// IncludeEphemeral here would leak true ephemerals into default ready
		// work — issueops.getReadyWispsInTx filters them the same way.
		wispPreds, err := r.buildReadyWorkPredicates(ctx, filter, wispsFilterTables)
		if err != nil {
			return idSrcPage{}, false, err
		}
		//nolint:gosec // G201: whereSQL composed from hardcoded fragments and ? placeholders.
		wispSub := fmt.Sprintf(
			"SELECT id, 'w' AS src, %s FROM wisps %s",
			unionSortColumnsSQL, wispPreds.whereSQL,
		)
		subqueries = append(subqueries, wispSub)
		allArgs = append(allArgs, wispPreds.args...)
	}

	sortOrder := buildReadyWorkOrder(filter.SortPolicy)
	// limitOffsetSQL keeps the +1 overfetch for hasMore AND honors Offset
	// when Limit is 0 (the hand-rolled guard here used to drop the offset
	// entirely in that case, bd-6dnrw.44 P3).
	outerLimit := limitOffsetSQL(filter.Limit, filter.Offset)

	//nolint:gosec // G201: subqueries built from hardcoded fragments and ? placeholders.
	unionSQL := fmt.Sprintf(
		"SELECT id, src FROM (%s) merged %s %s",
		strings.Join(subqueries, " UNION ALL "),
		sortOrder.SQL,
		outerLimit,
	)
	allArgs = append(allArgs, sortOrder.Args...)

	rows, err := r.runner.QueryContext(ctx, unionSQL, allArgs...)
	if err != nil {
		return idSrcPage{}, false, fmt.Errorf("ready work union: query: %w", err)
	}
	page, err := scanIDSrcPage(rows, true)
	if err != nil {
		return idSrcPage{}, false, fmt.Errorf("ready work union: %w", err)
	}
	hasMore := page.trimToLimit(filter.Limit)
	return page, hasMore, nil
}

func (r *issueSQLRepositoryImpl) getReadyWorkUnion(ctx context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	page, hasMore, err := r.getReadyWorkIDPage(ctx, filter)
	if err != nil {
		return domain.SearchPage{}, err
	}

	issuesByID, err := r.fetchIssuesByIDs(ctx, page.issueIDs, issuesFilterTables, types.IssueFilter{})
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("ready work union: hydrate issues: %w", err)
	}
	var wispsByID map[string]*types.Issue
	if len(page.wispIDs) > 0 {
		wispsByID, err = r.fetchIssuesByIDs(ctx, page.wispIDs, wispsFilterTables, types.IssueFilter{})
		if err != nil && !dberrors.IsTableNotExist(err) {
			return domain.SearchPage{}, fmt.Errorf("ready work union: hydrate wisps: %w", err)
		}
	}

	return domain.SearchPage{
		Items:   reassembleBySrc(page.ordered, issuesByID, wispsByID),
		HasMore: hasMore,
	}, nil
}

func (r *issueSQLRepositoryImpl) getReadyWorkWithCountsUnion(ctx context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	wispDepsExist, err := r.optionalTableExists(ctx, "wisp_dependencies")
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("ready work union with counts: wisp dependency probe: %w", err)
	}

	page, hasMore, err := r.getReadyWorkIDPage(ctx, filter)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	if len(page.ordered) == 0 {
		return domain.SearchCountsPage{Items: nil, HasMore: hasMore}, nil
	}

	issuesByID, err := r.fetchCountsByIDs(ctx, page.issueIDs, issuesFilterTables, wispDepsExist, false)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("ready work union with counts: hydrate issues: %w", err)
	}
	var wispsByID map[string]*types.IssueWithCounts
	if len(page.wispIDs) > 0 {
		wispsByID, err = r.fetchCountsByIDs(ctx, page.wispIDs, wispsFilterTables, true, false)
		if err != nil && !dberrors.IsTableNotExist(err) {
			return domain.SearchCountsPage{}, fmt.Errorf("ready work union with counts: hydrate wisps: %w", err)
		}
	}

	return domain.SearchCountsPage{
		Items:   reassembleBySrc(page.ordered, issuesByID, wispsByID),
		HasMore: hasMore,
	}, nil
}
