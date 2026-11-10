package uow

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

var _ issueops.EventsMaintenanceRunner = (*doltSQLProvider)(nil)

// RunEventsMaintenanceTx runs one events-journal auto-prune step in its own
// transaction on this provider's connection pool.
//
// It goes through BeginTx rather than a unit of work because maintenance needs
// none of the repository surface a UOW assembles — just a transaction-bound
// runner — and because the EPHEMERAL commit form is the whole point here: an
// empty commit message persists the delete into the working set without minting
// a Dolt commit, which is what the dolt_ignored journal tables require. The
// leases heartbeat takes the same route for the same reason.
//
// This is the arm proxied-server mode reaches. It matters because it is the one
// topology where the writer is a short-lived CLI process talking to a long-lived
// SQL server: without it, a proxied workspace would journal forever and prune
// only when someone remembered to.
func (p *doltSQLProvider) RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, issueops.DBTX) error) error {
	tx, err := p.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.RollbackUnlessCommitted(ctx)
	if err := fn(ctx, tx.Runner()); err != nil {
		return err
	}
	return tx.Commit(ctx, "")
}
