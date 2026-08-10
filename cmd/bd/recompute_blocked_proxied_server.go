package main

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// runRecomputeBlockedProxiedServer routes bd recompute-blocked through the
// proxied-server plane (bd-04vav).
//
// The repair itself is not reimplemented here: the guard, the fixpoint
// recompute, the staging set and the commit message all come from
// versioncontrolops.RecomputeAllBlockedOnConn, the same code the embedded and
// server stores run, so the two modes cannot compute different flags or land
// different history. What is mode-specific is only how the connection is got.
//
// It runs on a PINNED non-transactional connection rather than through
// uow.RunTx*, for two reasons the other proxied ports do not have:
//
//   - The repair's post-commit DOLT_ADD/DOLT_COMMIT are stored procedures,
//     which Dolt refuses inside an explicit transaction — the same reason every
//     other versioncontrolops entry point takes a conn.
//   - The uow commit path stages with DOLT_COMMIT('-Am', …). This repair must
//     stage `issues` ALONE: it derives a flag from the graph and has no business
//     sweeping an unrelated dirty working set into its commit, which is exactly
//     what the dirty-graph guard exists to prevent on the read side.
//
// The exit code is the whole downstream contract (wh-bridge-sync runs this under
// a timeout cap, treats rc 124 as "still running server-side", and retries up to
// 3 times because the operation is idempotent): success is 0, and every failure
// — an unreachable server, a dirty graph, a killed query — is a nonzero HandleError.
// Nothing here invents a green.
func runRecomputeBlockedProxiedServer(ctx context.Context) error {
	var changed int64
	if err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var rerr error
		changed, rerr = versioncontrolops.RecomputeAllBlockedOnConn(ctx, conn, "")
		return rerr
	}); err != nil {
		// runProxiedNonTx reports its own provider failures and returns an
		// already-rendered exit; re-wrapping would print a second Error line.
		if _, reported := exitCodeFromError(err); reported {
			return err
		}
		return HandleError("recompute is_blocked: %v", err)
	}
	return renderRecomputeBlocked(int(changed))
}
