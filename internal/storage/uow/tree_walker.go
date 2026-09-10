package uow

import (
	"context"
	"fmt"

	publicops "github.com/steveyegge/beads/issueops"
)

// TreeWalkerSource is the capability accessor a unit-of-work provider offers for
// the dependency-tree role, the sibling of CycleDetectorSource and
// EdgeReaderSource.
type TreeWalkerSource interface {
	TreeWalker() (publicops.TreeWalker, error)
}

// treeWalker answers a dependency-tree walk through a unit of work.
type treeWalker struct {
	provider UnitOfWorkProvider
}

// TreeWalker returns the guarded dependency-tree surface for this provider.
func (p *doltSQLProvider) TreeWalker() (publicops.TreeWalker, error) {
	return NewTreeWalker(p)
}

// NewTreeWalker constructs a public tree walker backed by provider.
func NewTreeWalker(provider UnitOfWorkProvider) (publicops.TreeWalker, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new tree walker: unit-of-work provider must not be nil")
	}
	return &treeWalker{provider: provider}, nil
}

var _ publicops.TreeWalker = (*treeWalker)(nil)

// WalkTree walks the dependency graph inside ONE read-only unit of work.
//
// One unit of work is load-bearing here rather than tidy. The walk is a root
// probe, a recursion of adjacency reads and a hydration per node, and a `both`
// request runs the whole thing twice; sharing one transaction is what makes the
// answer describe a graph that existed rather than a stitching of several that
// did. The proxied front door previously called the use case twice for a `both`
// walk with no transaction spanning the pair.
func (t *treeWalker) WalkTree(ctx context.Context, req publicops.WalkTreeRequest) (publicops.TreeResult, error) {
	return RunTxRead(ctx, t.provider, func(ctx context.Context, uw UnitOfWork) (publicops.TreeResult, error) {
		return uw.DependencyUseCase().WalkDependencyTree(ctx, req)
	})
}
