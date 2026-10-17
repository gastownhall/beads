package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// settleBlockedRecheck runs the post-commit blocked-state recheck of what a
// committed write recorded (gastownhall/beads#6716) and logs a failure
// instead of returning it: the write is durable, and an error would read as
// "the write did not land" to every caller.
func (s *DoltStore) settleBlockedRecheck(ctx context.Context, pending issueops.BlockedRecheck) {
	logBlockedRecheckFailure(ctx, pending, s.recheckBlockedAfterCommit(ctx, pending))
}
