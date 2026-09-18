package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// runDoltCommitProxiedServer is the proxied dual of `bd dolt commit`: the
// explicit commit point that discharges dolt.auto-commit=batch/off on this
// route (GH#4995). Proxied mode returns from the root pre-run before
// newDoltStore ever runs, so getStore() is nil here and the UOW provider is the
// only handle on the server — without this dual, batch/off on a proxied
// checkout meant "never commit" and writes piled up in the working set forever.
//
// It deliberately does not open a unit of work: doltServerTx.Commit blanks its
// message under a deferred context (that is the deferral this command exists to
// discharge), so routing the flush through it would make the flush itself a
// no-op. It runs DOLT_COMMIT('-Am') on a pinned maintenance connection instead,
// which is the uow-side equivalent of DoltStorage.CommitAll: "everything in the
// working set", including out-of-band and config-table dirt.
func runDoltCommitProxiedServer(ctx context.Context, message string) (bool, error) {
	if uowProvider == nil {
		return false, fmt.Errorf("proxied-server UOW provider not initialized")
	}
	mp, ok := uowProvider.(uow.MaintenanceProvider)
	if !ok {
		return false, fmt.Errorf("proxied-server provider %T does not support maintenance operations", uowProvider)
	}

	committed := false
	err := mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		// The same gate doltServerTx.Commit uses, and for the same reason: an
		// empty DOLT_COMMIT is rejected server-side with "nothing to commit"
		// and floods the server log.
		pending, err := issueops.HasPendingChanges(ctx, conn)
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?);", message); err != nil {
			if isDoltNothingToCommit(err) {
				return nil
			}
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}
