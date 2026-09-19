package uow

import (
	"context"
	"errors"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the unit-of-work backend's answers to the OUT-OF-BAND hooks
// the two accessor-reachable Lifecycle contracts declare — the observations a
// backend publishes that the frozen role fixture kit does not carry, built here
// at the wiring site the way CycleDetectorFixture.Exec is.
//
// None of them is raw SQL. That is the point of the contracts they serve: the
// staging contract next door reads its post-state with `SELECT` through
// RawSQLUseCase, so a backend with no SQL surface can run none of it, while
// every hook below is one read-only unit of work over a use case this backend
// already publishes to ordinary callers.

// newUOWContractEventLister answers one issue's whole event journal. The limit
// is 0, which the repository reads as "no limit": the contracts take a DELTA
// around the operation under test, so a truncated journal would make an
// assertion about what a refusal wrote unfalsifiable.
func newUOWContractEventLister(provider UnitOfWorkProvider) func(context.Context, string) ([]*types.Event, error) {
	return func(ctx context.Context, issueID string) ([]*types.Event, error) {
		return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([]*types.Event, error) {
			iter, err := uw.IssueUseCase().IterEvents(ctx, issueID, 0)
			if err != nil {
				return nil, err
			}
			defer func() { _ = iter.Close() }()
			var events []*types.Event
			for iter.Next(ctx) {
				events = append(events, iter.Value())
			}
			return events, iter.Err()
		})
	}
}

// newUOWContractDependencyLister answers one issue's outgoing edges as records,
// straight off the dependency use case's record read — no target resolution, so
// the answer is the edge rows themselves.
func newUOWContractDependencyLister(provider UnitOfWorkProvider) func(context.Context, string) ([]*types.Dependency, error) {
	return func(ctx context.Context, issueID string) ([]*types.Dependency, error) {
		return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([]*types.Dependency, error) {
			byIssue, err := uw.DependencyUseCase().GetIssueDependencyRecords(ctx, []string{issueID})
			if err != nil {
				return nil, err
			}
			return byIssue[issueID], nil
		})
	}
}

// newUOWContractWispProbe reports whether the EPHEMERAL plane holds a row at id.
// It reads the wisp plane alone, which is the whole reason the hook exists: the
// backend's both-plane resolve answers the durable row first, so a wisp sharing
// an occupied durable id never reaches it.
//
// A miss arrives as ErrNotFound or as the driver's no-rows error depending on
// which layer answered, exactly as operationIssue treats it, and both mean "no
// row" rather than a failed read.
func newUOWContractWispProbe(provider UnitOfWorkProvider) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, id string) (bool, error) {
		return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (bool, error) {
			wisp, err := uw.IssueUseCase().GetWisp(ctx, id)
			if err != nil {
				if errors.Is(err, publicops.ErrNotFound) || dberrors.IsNoRows(err) {
					return false, nil
				}
				return false, err
			}
			return wisp != nil, nil
		})
	}
}
