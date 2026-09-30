// Package storereadylister holds the store-backed implementation of
// issueops.ReadyLister: one shared body that every store-shaped backend's
// ReadyLister accessor hands back.
//
// It is a package of its own for the reason internal/workapi/storereader and
// internal/workapi/storereadycounter are: a constructor sitting in
// internal/workapi would be a one-line drop-in for store.ReadyLister() that
// silently skips the decorators, and the cmd-bd-role-constructors depguard rule
// in .golangci.yml makes a front door importing this package a lint failure
// rather than a review comment.
package storereadylister

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// ReadyPager is the one store seam this body reads through: the ready page and
// the size of the whole ready set, resolved in one read transaction by the same
// statements (storage.DoltStorage.GetReadyWorkWithCountsAndTotal). Every
// storage.DoltStorage satisfies it.
//
// THE PARAMETER IS THE SEAM, not storage.DoltStorage, so the single-pass
// property is a fact about the type rather than a comment: this body has no
// other method to call, so it cannot grow a second counting pass (or a second
// defer-wake sweep) without its constructor changing shape.
type ReadyPager interface {
	GetReadyWorkWithCountsAndTotal(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error)
}

// New returns the ready-listing surface backed by a store handle. *DoltStore and
// *EmbeddedDoltStore answer identically because the difference between them is
// below storage.DoltStorage, not above it.
func New(store ReadyPager) (issueops.ReadyLister, error) {
	if store == nil {
		return nil, &issueops.ErrUnsupported{Op: "storereadylister.New", Backend: "nil"}
	}
	return &storeReadyLister{store: store}, nil
}

type storeReadyLister struct{ store ReadyPager }

var _ issueops.ReadyLister = (*storeReadyLister)(nil)

// ListReady answers one ready listing with EXACTLY ONE call to the store seam.
//
// The page and its total come back from the page's own statements: the total
// rides the ID query as a window count evaluated over the full predicate before
// LIMIT (internal/storage/issueops.GetReadyWorkWithCountsAndTotalInTx), which is
// the single-pass work `bd ready --json` already relied on and which
// TestReadyWorkStatementBudget pins statement by statement.
//
// OFFSET IS PAGED HERE, NOT IN THE QUERY, exactly as storeReader.Ready pages it:
// the seam renders LIMIT without OFFSET, so the filter reaches past the skipped
// rows and workapi.FinishPageAt drops them. The window total is unaffected by
// that widening — it counts the predicate, not the page.
//
// HAS-MORE COMES FROM THE TOTAL rather than from an over-fetched probe row,
// which is what lets this body skip the extra row storeReader.Ready fetches:
// with the whole set's size in hand, "did the limit hide anything" is
// Offset+len(Items) < Total.
func (l *storeReadyLister) ListReady(ctx context.Context, req issueops.ReadyListRequest) (issueops.ReadyListing, error) {
	filter, err := workapi.BuildReadyFilter(req.ReadyRequest)
	if err != nil {
		return issueops.ReadyListing{}, err
	}
	limit := filter.Limit
	filter = workapi.WithReadyRowsBeforeThePage(filter, req.Offset)
	filter.MaxRows = req.MaxRows
	filter.MaxRowsSource = req.MaxRowsSource

	items, total, err := l.store.GetReadyWorkWithCountsAndTotal(ctx, filter)
	if err != nil {
		return issueops.ReadyListing{}, err
	}
	items, _ = workapi.FinishPageAt(items, "", false, req.Offset, limit, false)
	return workapi.ReadyListingOf(items, req.Offset, limit, int64(total)), nil
}
