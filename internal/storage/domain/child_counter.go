package domain

import "context"

type ChildCounterOpts struct {
	UseWispsTable bool
}

type ChildCounterSQLRepository interface {
	NextChildID(ctx context.Context, parentID string, opts ChildCounterOpts) (string, error)
	// AdvanceForChild raises the counter of childID's parent to childID's
	// suffix when it is lower, so a child the caller named is never minted
	// again. It never lowers a counter, routes a wisp parent to
	// wisp_child_counters, and does nothing for an id with no hierarchical
	// parent or whose parent does not exist.
	AdvanceForChild(ctx context.Context, childID string) error
}
