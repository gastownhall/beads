package storage

import (
	"context"
)

// BlockedMaintainedByServer is RecomputeBlockedResult.MaintainedBy for a
// backend whose blocked state lives in, and is maintained by, a remote server.
const BlockedMaintainedByServer = "server"

// RecomputeBlockedResult is what one is_blocked repair did.
type RecomputeBlockedResult struct {
	// RowsCorrected is how many rows' persisted flag changed. A consistent
	// store corrects none.
	RowsCorrected int
	// MaintainedBy is "" when THIS backend recomputed its own column, and
	// BlockedMaintainedByServer when the column belongs to a remote server
	// that maintains it, in which case nothing was recomputed here and
	// RowsCorrected is 0. A caller that must tell "nothing was stale" from
	// "nothing was checked here" reads it.
	MaintainedBy string
}

// ServerMaintainedBlockedState is implemented by a store whose blocked state a
// remote server derives and keeps — today the http backend.
type ServerMaintainedBlockedState interface {
	BlockedStateMaintainedByServer() bool
}

// RecomputeBlocked is the one library entry for the is_blocked repair
// (`bd recompute-blocked`), the named repair of
// issueops.BlockedStateInvariant's merge clause — the one way that
// invariant's persisted column can be stale. Every backend goes through it.
//
// A LOCAL store (BlockedRecomputer) recomputes its whole column from the
// dependency graph and commits the result, idempotently.
//
// A REMOTE store (ServerMaintainedBlockedState) recomputes nothing and answers
// BlockedMaintainedByServer. Every write its client makes is committed by the
// server's own store under the invariant's local-write clause, so the client
// cannot leave the column stale and holds no copy of it; the merge clause's
// staleness happens to the server's database, and its repair is this same
// entry run against that database, in the server's workspace. There is no
// wire operation for it: a whole-column rewrite any bearer of a workspace
// credential could trigger, for state no client can have caused, is not one
// to publish.
//
// It looks through the decorator chain, as the repair fires no hooks. Any
// other store refuses with *ErrUnsupported.
func RecomputeBlocked(ctx context.Context, s DoltStorage) (RecomputeBlockedResult, error) {
	inner := UnwrapStore(s)
	if remote, ok := inner.(ServerMaintainedBlockedState); ok && remote.BlockedStateMaintainedByServer() {
		return RecomputeBlockedResult{MaintainedBy: BlockedMaintainedByServer}, nil
	}
	local, ok := inner.(BlockedRecomputer)
	if !ok {
		return RecomputeBlockedResult{}, &ErrUnsupported{Op: "RecomputeAllBlocked", Backend: "this storage"}
	}
	changed, err := local.RecomputeAllBlocked(ctx)
	if err != nil {
		return RecomputeBlockedResult{}, err
	}
	return RecomputeBlockedResult{RowsCorrected: changed}, nil
}
