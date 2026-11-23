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
	// RowsAbstained is how many open rows the repair left as found because
	// they depend on a wisp this database has no row for and have no blocker
	// it can see (issueops.CountIsBlockedUndecidedInTx). Neither value of the
	// flag can be derived here for such a row, so it is not a correction and
	// not a confirmation. Zero on a database that holds every wisp its edges
	// name, and zero from a store that does not count them
	// (BlockedRecomputeCounter).
	RowsAbstained int
	// MaintainedBy is "" when THIS backend recomputed its own column, and
	// BlockedMaintainedByServer when the column belongs to a remote server
	// that maintains it, in which case nothing was recomputed here and
	// RowsCorrected is 0. A caller that must tell "nothing was stale" from
	// "nothing was checked here" reads it.
	MaintainedBy string
}

// BlockedRecomputeCounts is what one full is_blocked repair did to a local
// store: the rows it corrected, and the rows it could not decide.
type BlockedRecomputeCounts struct {
	// Corrected is how many rows' persisted flag changed.
	Corrected int
	// Abstained is RecomputeBlockedResult.RowsAbstained, read in the repair's
	// own transaction after its last pass.
	Abstained int
}

// BlockedRecomputeCounter is a BlockedRecomputer that also reports how many
// rows its repair left as found. It is a second interface, not a wider
// RecomputeAllBlocked, so the callers that only need the corrected count
// (bd sync, bd doctor --fix) and any store outside this module keep the
// signature they have.
type BlockedRecomputeCounter interface {
	RecomputeAllBlockedCounts(ctx context.Context) (BlockedRecomputeCounts, error)
}

// ServerMaintainedBlockedState is implemented by a store whose blocked state a
// remote server derives and keeps — today the http backend.
type ServerMaintainedBlockedState interface {
	BlockedStateMaintainedByServer() bool
}

// RecomputeBlocked is the one library entry for the is_blocked repair
// (`bd recompute-blocked`), the named repair of
// issueops.BlockedStateInvariant's merge clause — the one way that
// invariant's persisted column can be stale. Every store goes through it. The
// `--proxied-server` route deliberately does not: it runs
// versioncontrolops.RecomputeAllBlockedOnConn (the guard, recompute, staging
// set and commit message a local store uses) on a pinned non-transactional
// connection, because the repair's DOLT_ADD/DOLT_COMMIT are stored procedures
// Dolt refuses inside an explicit transaction and its commit must stage
// `issues` alone (cmd/bd/recompute_blocked_proxied_server.go).
//
// A LOCAL store (BlockedRecomputer) recomputes its whole column from the
// dependency graph and commits the result, idempotently. One that is also a
// BlockedRecomputeCounter is asked through that interface, so the result
// carries RowsAbstained.
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
	if counter, ok := inner.(BlockedRecomputeCounter); ok {
		counts, err := counter.RecomputeAllBlockedCounts(ctx)
		if err != nil {
			return RecomputeBlockedResult{}, err
		}
		return RecomputeBlockedResult{RowsCorrected: counts.Corrected, RowsAbstained: counts.Abstained}, nil
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
