package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// Releaser returns the claim-release surface for this store.
func (s *DoltStore) Releaser() (issueops.Releaser, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Releaser", Backend: "nil"}
	}
	return &releaser{store: s}, nil
}

// releaser gives up a claim inside ONE transaction.
//
// There is no shared constructor package for this role, for the reason
// metadataCAS gives: the classifying read, the release and the post-state
// snapshot must see one snapshot, and a transaction is not reachable through
// storage.DoltStorage. The sharing happens one level down — this body and the
// embedded store's are a few lines each around
// issueops.ReleaseIssueInTx, and the unit-of-work leg reaches the same function
// through the domain issue repository — so all three legs are ONE body, which
// the conformance contract's header states.
type releaser struct{ store *DoltStore }

var _ issueops.Releaser = (*releaser)(nil)

// Release gives up the claim on one issue.
//
// VALIDATION HAPPENS BEFORE THE TRANSACTION, which makes the role's "a refusal
// changes nothing" true of the connection as well as of the row.
//
// THE RETRY IS LOAD-BEARING, not defensive. Dolt has no row locks, so a
// concurrent writer is caught at commit time and withRetryTx re-runs the WHOLE
// body against the winner's committed row. That replay is what makes a
// conditional release lose honestly rather than clobber a claim that moved.
//
// THE VERIFY-BY-RE-READ IS THE ONE THING THIS LEG ADDS, and it is not
// decoration: the direct UnclaimIssue route has carried it since bd-zccb9,
// because a degraded server can report a release that did not land and leave
// the caller believing it let go of work it still holds. A role that dropped it
// would be a quieter route to the same incident. The postcondition is
// unclaimed()'s — the release's post-state is exactly the one it names.
//
// THE VERSION-CONTROL ENTRY IS ONE PER RELEASE THAT WROTE. An ephemeral release
// reports an empty table set, so there is nothing to stage and none is
// recorded.
func (r *releaser) Release(ctx context.Context, req issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	if err := workapi.ValidateReleaseRequest(req); err != nil {
		return issueops.ReleaseResult{}, err
	}

	var result issueops.ReleaseResult
	if err := r.store.withCircuitWrite(ctx, func(ctx context.Context) error {
		return r.store.verifiedClaimWrite(ctx, req.IssueID, unclaimed(), func() error {
			// runIssueOperationTxWithMessage already carries the retry, the
			// empty-table skip an ephemeral release needs, and the staging
			// order — so this leg spells none of them itself.
			return r.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
				released, write, err := storeops.ReleaseIssueInTx(ctx, tx, req)
				if err != nil {
					return nil, "", err
				}
				result = released
				return write.Tables, fmt.Sprintf("bd: unclaim %s", req.IssueID), nil
			})
		})
	}); err != nil {
		return issueops.ReleaseResult{}, err
	}
	return result, nil
}
