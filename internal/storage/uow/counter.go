package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// CounterSource is the capability accessor a unit-of-work provider offers for
// the count role, the sibling of IssueReaderSource and CommenterSource.
type CounterSource interface {
	Counter() (publicops.Counter, error)
}

// counter answers count queries through a unit of work.
type counter struct {
	provider UnitOfWorkProvider
}

// Counter returns the guarded issue-count surface for this provider.
func (p *doltSQLProvider) Counter() (publicops.Counter, error) {
	return NewCounter(p)
}

// NewCounter constructs a public counter backed by provider.
func NewCounter(provider UnitOfWorkProvider) (publicops.Counter, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new counter: unit-of-work provider must not be nil")
	}
	return &counter{provider: provider}, nil
}

var _ publicops.Counter = (*counter)(nil)

// Count answers one count inside one read-only unit of work.
func (c *counter) Count(ctx context.Context, req publicops.CountRequest) (publicops.CountResult, error) {
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CountResult, error) {
		filter, err := countFilter(ctx, uw, req)
		if err != nil {
			return publicops.CountResult{}, err
		}
		total, err := uw.IssueUseCase().CountIssues(ctx, "", filter)
		if err != nil {
			return publicops.CountResult{}, err
		}
		return publicops.CountResult{Total: total}, nil
	})
}

// CountByGroup answers the bucketed count and its scalar total inside ONE unit
// of work.
//
// Sharing the unit of work is free here — this seam has a transaction and the
// store seam does not — so both queries see one snapshot on this backend.
// issueops.CountByGroupResult.Total deliberately does NOT promise that, because
// the store-backed body cannot offer it. What all three implementations share is
// that the total is the SCALAR count rather than the sum of the buckets, which
// is the part callers can be wrong about.
func (c *counter) CountByGroup(ctx context.Context, req publicops.CountByGroupRequest) (publicops.CountByGroupResult, error) {
	group, err := workapi.ValidateCountGroup(req.GroupBy)
	if err != nil {
		return publicops.CountByGroupResult{}, err
	}
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CountByGroupResult, error) {
		filter, err := countFilter(ctx, uw, req.Filter)
		if err != nil {
			return publicops.CountByGroupResult{}, err
		}
		groups, err := uw.IssueUseCase().CountIssuesByGroup(ctx, filter, group)
		if err != nil {
			return publicops.CountByGroupResult{}, err
		}
		total, err := uw.IssueUseCase().CountIssues(ctx, "", filter)
		if err != nil {
			return publicops.CountByGroupResult{}, err
		}
		if groups == nil {
			groups = map[string]int{}
		}
		return publicops.CountByGroupResult{Groups: groups, Total: total}, nil
	})
}

// countFilter builds the storage filter from the unit of work the call already
// holds, loading configuration only when IncludeInfra can read it — the same two
// decisions the store-backed body makes, through the same builder.
func countFilter(ctx context.Context, uw UnitOfWork, req publicops.CountRequest) (types.IssueFilter, error) {
	var cfg workapi.ListConfig
	if req.IncludeInfra {
		loaded, err := workapi.LoadUOWListConfig(ctx, uw)
		if err != nil {
			return types.IssueFilter{}, err
		}
		cfg = loaded
	}
	return workapi.BuildCountFilter(req, cfg)
}
