//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// LeaseReclaimer returns the lease-sweep surface for this store.
func (s *EmbeddedDoltStore) LeaseReclaimer() (issueops.LeaseReclaimer, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "LeaseReclaimer", Backend: "nil"}
	}
	return &leaseReclaimer{store: s}, nil
}

// leaseReclaimer answers a Reclaim from one connection's transaction around the
// shared body, storeops.ExecuteReclaimInTx, the embedded twin of the
// server-backed store's leaseReclaimer.
//
// THE VERSION-CONTROL ENTRY LANDS AFTER THE SQL COMMIT, through
// runIssueOperationTxWithMessage, Releaser's funnel: unlike the raw
// ReclaimExpiredLeases, which leaves its rows in the working set for the CLI's
// commitPendingIfEmbedded, a role is called by front doors (bd serve, a
// library caller) that have no such second step, so the role records its own
// entry, and the funnel is what lets a deferred version commit on the context
// suppress it.
type leaseReclaimer struct{ store *EmbeddedDoltStore }

var _ issueops.LeaseReclaimer = (*leaseReclaimer)(nil)

func (l *leaseReclaimer) Reclaim(ctx context.Context, request issueops.ReclaimRequest) (issueops.ReclaimResult, error) {
	if err := storeops.ValidateReclaimRequest(request); err != nil {
		return issueops.ReclaimResult{}, err
	}
	var result issueops.ReclaimResult
	if err := l.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		result, err = storeops.ExecuteReclaimInTx(ctx, tx, request)
		if err != nil {
			return nil, "", err
		}
		tables, msg := storeops.ReclaimVersionCommit(result)
		return tables, msg, nil
	}); err != nil {
		return issueops.ReclaimResult{}, err
	}
	return result, nil
}
