package uow

import (
	"context"
	"fmt"

	publicops "github.com/steveyegge/beads/issueops"
)

// CycleDetectorSource is the capability accessor a unit-of-work provider offers
// for the cycle-report role, the sibling of CounterSource and IssueReaderSource.
type CycleDetectorSource interface {
	CycleDetector() (publicops.CycleDetector, error)
}

// cycleDetector answers the cycle report through a unit of work.
type cycleDetector struct {
	provider UnitOfWorkProvider
}

// CycleDetector returns the guarded cycle-report surface for this provider.
func (p *doltSQLProvider) CycleDetector() (publicops.CycleDetector, error) {
	return NewCycleDetector(p)
}

// NewCycleDetector constructs a public cycle detector backed by provider.
func NewCycleDetector(provider UnitOfWorkProvider) (publicops.CycleDetector, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new cycle detector: unit-of-work provider must not be nil")
	}
	return &cycleDetector{provider: provider}, nil
}

var _ publicops.CycleDetector = (*cycleDetector)(nil)

// DetectCycles walks the blocking graph inside ONE read-only unit of work.
//
// The report is a graph read followed by a hydration per member, and on this
// backend both run on the same transaction — so the rows a cycle names are the
// rows that were on the cycle, rather than the rows that exist by the time the
// second query runs. The two store-backed bodies get the same property from
// their own read transaction.
func (c *cycleDetector) DetectCycles(ctx context.Context, _ publicops.DetectCyclesRequest) (publicops.CycleReport, error) {
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CycleReport, error) {
		return uw.DependencyUseCase().DetectCycleReport(ctx)
	})
}
