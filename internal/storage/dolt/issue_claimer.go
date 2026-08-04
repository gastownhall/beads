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
	var result issueops.ClaimResult
	write := func() error {
		return c.store.runIssueOperationTx(ctx, storageissueops.ClaimCommitMessage(request.IssueID, request.Actor),
			func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
				var err error
				var tables storageissueops.ChangedTables
				result, tables, err = storageissueops.ExecuteClaim(ctx, tx, request)
				return tables, err
			})
	}
	if err := c.store.verifiedClaimWrite(ctx, request.IssueID, claimedBy(request.Actor), write); err != nil {
		return issueops.ClaimResult{}, err
	}
	return result, nil
}

var _ issueops.Claimer = (*issueClaimer)(nil)
