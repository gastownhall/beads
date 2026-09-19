package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// ReadyCounterSource is the capability accessor a unit-of-work provider offers
// for the ready-count role.
type ReadyCounterSource interface {
	ReadyCounter() (publicops.ReadyCounter, error)
}

// readyCounter sizes the ready set through a unit of work.
type readyCounter struct {
	provider UnitOfWorkProvider
}

// ReadyCounter returns the guarded ready-count surface for this provider.
func (p *doltSQLProvider) ReadyCounter() (publicops.ReadyCounter, error) {
	return NewReadyCounter(p)
}

// NewReadyCounter constructs a public ready counter backed by provider.
func NewReadyCounter(provider UnitOfWorkProvider) (publicops.ReadyCounter, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new ready counter: unit-of-work provider must not be nil")
	}
	return &readyCounter{provider: provider}, nil
}

var _ publicops.ReadyCounter = (*readyCounter)(nil)

// CountReady answers one ready count inside one read-only unit of work.
//
// IT COUNTS THE PAGE, deliberately. This seam has no COUNT(*) over the ready
// predicate — the ready set is a UNION of two tables whose overlap the id query
// resolves in Go (domain/db/issue_search.go: an id present in both planes is a
// hard error here, not a de-duplication) — so a hand-rolled COUNT would be a
// second definition of the ready set. Running the SAME query Reader.Ready runs,
// unbounded, and taking its length makes the identity CountReady promises true
// BY CONSTRUCTION, including the way it fails.
//
// What that costs is the unbounded ready query the store-backed sibling avoids
// with an indexed COUNT(*). Both front doors ask for this number only when a
// page came back full, so the cost is paid on truncated listings and nowhere
// else.
func (c *readyCounter) CountReady(ctx context.Context, req publicops.ReadyRequest) (publicops.ReadyCountResult, error) {
	// The same builder the store-backed sibling runs, which is also
	// BuildReadyFilter with the page removed — so the refusals of a Limit and
	// an Offset are one definition rather than one per backend.
	filter, err := workapi.BuildReadyCountFilter(req)
	if err != nil {
		return publicops.ReadyCountResult{}, err
	}
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ReadyCountResult, error) {
		page, err := uw.IssueUseCase().GetReadyWorkWithCounts(ctx, filter)
		if err != nil {
			return publicops.ReadyCountResult{}, err
		}
		return publicops.ReadyCountResult{Total: int64(len(page.Items))}, nil
	})
}
