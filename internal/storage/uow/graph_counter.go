package uow

import (
	"context"
	"fmt"

	publicops "github.com/steveyegge/beads/issueops"
)

// GraphCounterSource is the capability accessor a unit-of-work provider offers
// for the edge-count role, the sibling of EdgeReaderSource and TreeWalkerSource.
type GraphCounterSource interface {
	GraphCounter() (publicops.GraphCounter, error)
}

// graphCounter answers an edge count through a unit of work.
type graphCounter struct {
	provider UnitOfWorkProvider
}

// GraphCounter returns the guarded edge-count surface for this provider.
func (p *doltSQLProvider) GraphCounter() (publicops.GraphCounter, error) {
	return NewGraphCounter(p)
}

// NewGraphCounter constructs a public edge counter backed by provider.
func NewGraphCounter(provider UnitOfWorkProvider) (publicops.GraphCounter, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new graph counter: unit-of-work provider must not be nil")
	}
	return &graphCounter{provider: provider}, nil
}

var _ publicops.GraphCounter = (*graphCounter)(nil)

// CountEdges counts inside ONE read-only unit of work.
//
// One unit of work is load-bearing rather than tidy: the answer is an existence
// probe plus a tally, and AnchorEdgeCount.Missing is only true of a graph that
// existed if the two saw one snapshot.
func (c *graphCounter) CountEdges(ctx context.Context, req publicops.EdgeCountRequest) (publicops.EdgeCountResult, error) {
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.EdgeCountResult, error) {
		return uw.DependencyUseCase().CountEdges(ctx, req)
	})
}
