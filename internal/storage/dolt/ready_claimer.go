package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// ReadyClaimer returns the guarded take-ready-work surface for this store.
func (s *DoltStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return NewReadyClaimer(s)
}

// NewReadyClaimer returns a guarded ready claimer backed by store.
func NewReadyClaimer(store *DoltStore) (issueops.ReadyClaimer, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewReadyClaimer", Backend: "nil"}
	}
	return &readyClaimer{store: store}, nil
}

type readyClaimer struct{ store *DoltStore }

// ClaimNext runs selection, the compare-and-set and hydration in one
// transaction, under the claim-family verify-after-write (bd-zccb9) that
// ClaimReadyIssue runs: this reaches the same writes, so under a degraded
// server its exit status is no more trustworthy than that one's. A replay is
// safe and may legitimately win a DIFFERENT issue, because the replay re-scans
// the ready front rather than re-asserting a decision the first attempt made.
func (c *readyClaimer) ClaimNext(ctx context.Context, request issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	if err := storageissueops.ValidateClaimNextRequest(request); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	// The same builder Reader.Ready runs, over the same request type: the
	// claim's predicate IS the listing's predicate, because there is only one
	// place that turns the request into a filter.
	filter, err := workapi.BuildReadyFilter(request.Filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}

	// Wake expired dated defers before selecting, so a bead whose snooze just
	// ended is claimable the moment its date passes. Advisory, in a write tx
	// of its own: a failed sweep must not cost the claim.
	c.store.wakeExpiredDefers(ctx)

	// The write and its verify sit under withCircuitWrite so terminal circuit
	// success is recorded once at the boundary, only after verifiedReadyClaim
	// returns nil — matching the store's own ClaimReadyIssue. write is defined
	// inside the boundary so its runIssueOperationTxWithMessage captures the
	// circuit-managed ctx and defers success to the boundary.
	var result issueops.ClaimNextResult
	err = c.store.withCircuitWrite(ctx, func(ctx context.Context) error {
		write := func() (*types.Issue, error) {
			var claimed *types.Issue
			err := c.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
				attempt, tables, err := storageissueops.ExecuteClaimNext(ctx, tx, request.Actor, filter)
				if err != nil {
					return nil, "", err
				}
				result = attempt
				if attempt.Claimed == nil {
					return tables, "", nil
				}
				claimed = attempt.Claimed.Issue
				// The message names the claimed issue because that is the one `bd
				// dolt log` affordance callers actually grep, and it is what the
				// store's own ClaimReadyIssue wrote before the claim moved here.
				return tables, storageissueops.ClaimNextCommitMessage(attempt.Claimed.ID), nil
			})
			return claimed, err
		}
		_, verr := c.store.verifiedReadyClaim(ctx, request.Actor, write)
		return verr
	})
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	return result, nil
}

var _ issueops.ReadyClaimer = (*readyClaimer)(nil)
