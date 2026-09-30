package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// ReadyListerSource is the capability accessor a unit-of-work provider offers
// for the ready-listing role.
type ReadyListerSource interface {
	ReadyLister() (publicops.ReadyLister, error)
}

// readyLister lists ready work, with its total, through a unit of work.
type readyLister struct {
	provider UnitOfWorkProvider
}

// ReadyLister returns the guarded ready-listing surface for this provider.
func (p *doltSQLProvider) ReadyLister() (publicops.ReadyLister, error) {
	return NewReadyLister(p)
}

// NewReadyLister constructs a public ready lister backed by provider.
func NewReadyLister(provider UnitOfWorkProvider) (publicops.ReadyLister, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new ready lister: unit-of-work provider must not be nil")
	}
	return &readyLister{provider: provider}, nil
}

var _ publicops.ReadyLister = (*readyLister)(nil)

// ListReady answers one ready listing inside ONE read-only unit of work: the
// page Reader.Ready answers, and — only when the page cannot already say how
// large the set is — the count CountReady answers, over the same predicate.
//
// That is the proxied route's shape before this role existed (a page, then a
// ReadyCounter call only when the page came back truncated), with one
// difference: the count runs in the page's own unit of work rather than a second
// one, so the page and the total are one snapshot here too, as the role
// promises. The count is the unbounded page's length (countReadyInUOW), the
// cost ReadyCounter's doc already names, and it is paid only when rows were
// hidden. An untruncated page is its own total: Offset+len(Items).
func (l *readyLister) ListReady(ctx context.Context, req publicops.ReadyListRequest) (publicops.ReadyListing, error) {
	filter, err := workapi.BuildReadyFilter(req.ReadyRequest)
	if err != nil {
		return publicops.ReadyListing{}, err
	}
	countReq := req.ReadyRequest
	countReq.Limit, countReq.Offset = nil, 0
	countFilter, err := workapi.BuildReadyCountFilter(countReq)
	if err != nil {
		return publicops.ReadyListing{}, err
	}
	limit := filter.Limit
	filter = workapi.WithReadyRowsBeforeThePage(filter, req.Offset)
	filter.MaxRows = req.MaxRows
	filter.MaxRowsSource = req.MaxRowsSource

	// The same advisory sweep Reader.Ready runs, before the read span, which
	// never commits.
	WakeExpiredDefersAdvisory(ctx, l.provider)
	return RunTxRead(ctx, l.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ReadyListing, error) {
		page, err := uw.IssueUseCase().GetReadyWorkWithCounts(ctx, filter)
		if err != nil {
			return publicops.ReadyListing{}, err
		}
		items, hasMore := workapi.FinishPageAt(page.Items, "", false, req.Offset, limit, page.HasMore)
		total := int64(req.Offset + len(items))
		if hasMore || (req.Offset > 0 && len(items) == 0) {
			if total, err = countReadyInUOW(ctx, uw, countFilter); err != nil {
				return publicops.ReadyListing{}, err
			}
		}
		return workapi.ReadyListingOf(items, req.Offset, limit, total), nil
	})
}
