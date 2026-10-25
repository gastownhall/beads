package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// LeaseReclaimer returns the lease-sweep surface for this store.
func (s *DoltStore) LeaseReclaimer() (issueops.LeaseReclaimer, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "LeaseReclaimer", Backend: "nil"}
	}
	return &leaseReclaimer{store: s}, nil
}

// leaseReclaimer answers a Reclaim from one retrying write transaction around
// the shared body, storeops.ExecuteReclaimInTx.
//
// THE VERSION-CONTROL ENTRY GOES THROUGH runIssueOperationTxWithMessage, the
// funnel Releaser uses, rather than the raw ReclaimExpiredLeases' in-transaction
// DOLT_COMMIT: the funnel is what honors a deferred version commit on the
// context (dolt.auto-commit batch/off), which a role a front door calls has to,
// and a sweep that reverted nothing composes no staged set and so records no
// entry describing a no-op.
type leaseReclaimer struct{ store *DoltStore }

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
