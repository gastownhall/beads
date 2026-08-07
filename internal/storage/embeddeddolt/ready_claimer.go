//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// ReadyClaimer returns the guarded take-ready-work surface for this store.
func (s *EmbeddedDoltStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return NewReadyClaimer(s)
}

// NewReadyClaimer returns a guarded ready claimer backed by store.
func NewReadyClaimer(store *EmbeddedDoltStore) (issueops.ReadyClaimer, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewReadyClaimer", Backend: "nil"}
	}
	return &readyClaimer{store: store}, nil
}

type readyClaimer struct{ store *EmbeddedDoltStore }

// ClaimNext runs selection, the compare-and-set and hydration in one
// transaction. There is no verify-after-write wrapper here, and that is not an
// omission: the embedded store is in-process, so the commit-phase ambiguity
// the server-backed store recovers from cannot arise.
func (c *readyClaimer) ClaimNext(ctx context.Context, request issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	if err := storageissueops.ValidateClaimNextRequest(request); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	filter, err := workapi.BuildReadyFilter(request.Filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}

	// Wake expired dated defers before selecting, so a bead whose snooze just
	// ended is claimable the moment its date passes. Advisory, in a write tx
	// of its own: a failed sweep must not cost the claim.
	c.store.wakeExpiredDefers(ctx)

	var result issueops.ClaimNextResult
	err = c.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		attempt, tables, err := storageissueops.ExecuteClaimNext(ctx, tx, request.Actor, filter)
		if err != nil {
			return nil, "", err
		}
		result = attempt
		if attempt.Claimed == nil {
			return tables, "", nil
		}
		// Same id-bearing commit message as the server-backed store, so `bd
		// dolt log` reads the same on both backends.
		return tables, storageissueops.ClaimNextCommitMessage(attempt.Claimed.ID), nil
	})
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	return result, nil
}

var _ issueops.ReadyClaimer = (*readyClaimer)(nil)
