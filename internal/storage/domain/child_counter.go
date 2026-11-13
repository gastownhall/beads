package domain

import "context"

type ChildCounterOpts struct {
	UseWispsTable bool
}

type ChildCounterSQLRepository interface {
	NextChildID(ctx context.Context, parentID string, opts ChildCounterOpts) (string, error)
	// AdvanceForChild raises the child counter of childID's parent to
	// childID's suffix: what a create the caller named the id for
	// (`bd create --id P.8`) owes the parent's counter once the row is in
	// (GH#4750). It never lowers a counter, routes a wisp parent to
	// wisp_child_counters, and costs no SQL for a flat or parentless id.
	AdvanceForChild(ctx context.Context, childID string) error
}
