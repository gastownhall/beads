//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// Heartbeater returns the lease-refresh surface for this store.
func (s *EmbeddedDoltStore) Heartbeater() (issueops.Heartbeater, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Heartbeater", Backend: "nil"}
	}
	return &heartbeater{store: s}, nil
}

// heartbeater answers a Heartbeat from one connection's transaction around the
// shared body, storeops.ExecuteHeartbeatInTx, the embedded twin of the
// server-backed store's heartbeater. A REFRESH commits as SQL only: the
// leases table is dolt_ignored, so there is nothing versioned to record and
// no version-control entry follows it. A RE-ARM is recorded as a mutation and
// gets the version-control entry LeaseRearmVersionCommit names.
type heartbeater struct{ store *EmbeddedDoltStore }

var _ issueops.Heartbeater = (*heartbeater)(nil)

func (h *heartbeater) Heartbeat(ctx context.Context, request issueops.HeartbeatRequest) (issueops.HeartbeatResult, error) {
	if err := storeops.ValidateHeartbeatRequest(request); err != nil {
		return issueops.HeartbeatResult{}, err
	}
	var result issueops.HeartbeatResult
	var refusal error
	if err := h.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		result, err = storeops.ExecuteHeartbeatInTx(ctx, tx, request)
		// A lease refusal commits (it may have deleted an orphaned lease
		// row) and is reported after the commit; see HeartbeatRefusalCommits.
		refusal = nil
		if storeops.HeartbeatRefusalCommits(err) {
			refusal = err
			return nil, "", nil
		}
		if err != nil {
			return nil, "", err
		}
		tables, msg := storeops.LeaseRearmVersionCommit(request.IssueID, result.Rearmed)
		return tables, msg, nil
	}); err != nil {
		return issueops.HeartbeatResult{}, err
	}
	if refusal != nil {
		return issueops.HeartbeatResult{}, refusal
	}
	return result, nil
}
