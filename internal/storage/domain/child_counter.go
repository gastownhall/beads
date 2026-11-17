package domain

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

type ChildCounterOpts struct {
	UseWispsTable bool
}

type ChildCounterSQLRepository interface {
	NextChildID(ctx context.Context, parentID string, opts ChildCounterOpts) (string, error)
	// AdvanceForChild raises the parent's counter to the child suffix of
	// issue.ID when the caller named a hierarchical child ID (an explicit
	// --id, or an Issue.ID the caller set) instead of taking one from
	// NextChildID. It never lowers a counter, and it skips an ID that is not
	// a child or whose parent row does not exist.
	AdvanceForChild(ctx context.Context, issue *types.Issue) error
}
