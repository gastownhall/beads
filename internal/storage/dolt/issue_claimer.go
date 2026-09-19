package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// IssueClaimer returns the guarded atomic-claim surface for this store.
func (s *DoltStore) IssueClaimer() (issueops.Claimer, error) {
	return NewIssueClaimer(s)
}

// NewIssueClaimer returns the guarded atomic claim backed by store.
func NewIssueClaimer(store *DoltStore) (issueops.Claimer, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewIssueClaimer", Backend: "nil"}
	}
	return &issueClaimer{store: store}, nil
}

type issueClaimer struct{ store *DoltStore }

// Claim runs the compare-and-set under claim-family verify-after-write
// (bd-zccb9): under a degraded server the write's exit status is not truth in
// either direction, and a phantom claim is a duplicated implementation. Replay
// is safe because the CAS is re-checked inside the replayed transaction.
func (c *issueClaimer) Claim(ctx context.Context, request issueops.ClaimRequest) (issueops.ClaimResult, error) {
	// The write and its verify sit under withCircuitWrite so terminal circuit
	// success is recorded once at the boundary, only after verifiedClaimWrite
	// returns nil — matching the store's own ClaimIssue. write is defined inside
	// the boundary so its runIssueOperationTx captures the circuit-managed ctx
	// and defers success to the boundary.
	var result issueops.ClaimResult
	err := c.store.withCircuitWrite(ctx, func(ctx context.Context) error {
		write := func() error {
			return c.store.runIssueOperationTx(ctx, storageissueops.ClaimCommitMessage(request.IssueID, request.Actor),
				func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
					var err error
					var tables storageissueops.ChangedTables
					result, tables, err = storageissueops.ExecuteClaim(ctx, tx, request)
					return tables, err
				})
		}
		return c.store.verifiedClaimWrite(ctx, request.IssueID, claimedBy(request.Actor), write)
	})
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	return result, nil
}

var _ issueops.Claimer = (*issueClaimer)(nil)
