// Package storequerier holds the store-backed implementation of
// issueops.Querier: one shared body that every store-shaped backend's Querier
// accessor hands back.
//
// It is a package of its own for the reason internal/workapi/storereader is —
// see that package's doc. Down here the only importers are the two Dolt store
// packages, and the cmd-bd-role-constructors depguard rule in .golangci.yml
// makes a front door importing it a lint failure rather than a review comment.
package storequerier

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// New returns the boolean-query surface backed by a store handle. *DoltStore
// and *EmbeddedDoltStore answer identically because the difference between
// them is below storage.DoltStorage, not above it.
func New(store storage.DoltStorage) (issueops.Querier, error) {
	if store == nil {
		return nil, &issueops.ErrUnsupported{Op: "storequerier.New", Backend: "nil"}
	}
	return &storeQuerier{store: store}, nil
}

type storeQuerier struct{ store storage.DoltStorage }

var _ issueops.Querier = (*storeQuerier)(nil)

// THE OFFSET IS UNIFORM, not per-expression. This seam renders LIMIT without
// OFFSET, so the skip happens in Go — but it happens for BOTH shapes of query
// and it always skips MATCHES, never candidates. Which shape an expression
// takes is the evaluator's decision, so an Offset that behaved differently for
// `type=bug OR type=task` than for `type=bug` would be unpredictable from the
// outside (issueops/querier.go:70-86). A filter-expressible query reaches past
// the skipped rows in SQL; a predicate query already reads every candidate row,
// so its skip costs nothing.
func (q *storeQuerier) Query(ctx context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	plan, err := workapi.BuildQueryPlan(req)
	if err != nil {
		return issueops.IssuePage{}, err
	}

	filter := plan.Filter
	if !plan.RequiresPredicate() {
		// The store seam has no HasMore of its own, so ask for one row past the
		// page — and past the rows the offset skips — and let its presence be
		// the answer. A predicate query carries no row limit to extend: its
		// verdict comes from the count of MATCHES.
		filter = workapi.WithFetchOneExtra(workapi.WithRowsBeforeThePage(filter, plan.Offset))
	}

	rows, err := q.store.SearchIssuesWithCounts(ctx, "", filter)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	rows = workapi.ApplyQueryPredicate(rows, plan.Predicate)

	// The shared epilogue: the sort, the skip, the cut and the verdict written
	// out longhand is how two routes of one command came to disagree about
	// `--sort id --limit 5`. A query request refuses an Offset under a display
	// order (workapi.BuildQueryPlan), so the sort below is always a no-op when
	// the skip is not — which is why the two can share one function here.
	items, hasMore := workapi.FinishPageAt(rows, plan.SortBy, plan.Reverse, plan.Offset, plan.Limit, false)
	return issueops.IssuePage{Items: items, HasMore: hasMore}, nil
}
