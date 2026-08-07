package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// QuerierSource is the capability accessor a unit-of-work provider offers for
// the boolean-query role, the sibling of IssueReaderSource and CounterSource.
type QuerierSource interface {
	Querier() (publicops.Querier, error)
}

// querier answers boolean-expression queries through a unit of work.
type querier struct {
	provider UnitOfWorkProvider
}

// Querier returns the guarded boolean-query surface for this provider.
func (p *doltSQLProvider) Querier() (publicops.Querier, error) {
	return NewQuerier(p)
}

// NewQuerier constructs a public querier backed by provider.
func NewQuerier(provider UnitOfWorkProvider) (publicops.Querier, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new querier: unit-of-work provider must not be nil")
	}
	return &querier{provider: provider}, nil
}

var _ publicops.Querier = (*querier)(nil)

// Query answers one expression inside one read-only unit of work.
//
// It differs from the store-backed body in exactly two places, and both are
// this seam's capabilities rather than a second opinion about the query. It
// renders OFFSET, so a filter-expressible query pushes the skip down; and it
// reports HasMore natively, so the epilogue's seed is that verdict rather than
// an over-fetched row. The plan itself is workapi.BuildQueryPlan, the same
// function the other body calls.
func (q *querier) Query(ctx context.Context, req publicops.QueryRequest) (publicops.IssuePage, error) {
	plan, err := workapi.BuildQueryPlan(req)
	if err != nil {
		return publicops.IssuePage{}, err
	}
	return RunTxRead(ctx, q.provider, func(ctx context.Context, uw UnitOfWork) (publicops.IssuePage, error) {
		filter := plan.Filter
		if !plan.RequiresPredicate() {
			// Only the shape the database answers exactly may push the skip
			// down: an OFFSET applied to a predicate query's candidate rows
			// would discard rows the predicate never accepted
			// (issueops/querier.go:80-86).
			filter.Offset = plan.Offset
		}

		page, err := uw.IssueUseCase().SearchIssuesWithCounts(ctx, "", filter)
		if err != nil {
			return publicops.IssuePage{}, err
		}
		rows, hasMore := page.Items, page.HasMore
		if plan.RequiresPredicate() {
			rows = workapi.SkipRows(workapi.ApplyQueryPredicate(rows, plan.Predicate), plan.Offset)
		}

		items, hasMore := workapi.FinishPage(rows, plan.SortBy, plan.Reverse, plan.Limit, hasMore)
		return publicops.IssuePage{Items: items, HasMore: hasMore}, nil
	})
}
