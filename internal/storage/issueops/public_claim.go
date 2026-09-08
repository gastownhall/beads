package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ClaimCommitMessage names the claimed issue and its claimant in the storage
// commit. It lives here so every implementation of the claim role spells it
// identically and `bd dolt log` reads the same on all of them. The actor is in
// it because that line IS the audit trail — which is why every surface
// reaching the role validates the actor before calling it.
func ClaimCommitMessage(issueID, actor string) string {
	return fmt.Sprintf("bd: claim %s by %s", issueID, actor)
}

// ExecuteClaim applies a guarded claim in tx and reports durable tables changed.
func ExecuteClaim(ctx context.Context, tx *sql.Tx, request publicops.ClaimRequest) (publicops.ClaimResult, ChangedTables, error) {
	if request.Actor == "" || request.IssueID == "" {
		return publicops.ClaimResult{}, nil, fmt.Errorf("%w: claim requires actor and issue ID", storage.ErrValidation)
	}
	// ClaimIssueInTx routes a wisp id to the wisp tables and claims it there.
	// The role deliberately does not: the wisp plane is not claimable through
	// it, and the refusal lands here — before the pre-image read, and before
	// any write the enclosing transaction would have to roll back.
	if IsActiveWispInTx(ctx, tx, request.IssueID) {
		return publicops.ClaimResult{}, nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, request.IssueID)
	}
	claimed, err := ClaimIssueInTx(ctx, tx, request.IssueID, request.Actor)
	if err != nil {
		return publicops.ClaimResult{}, nil, classifyClaimRefusalInTx(ctx, tx, request.IssueID, err)
	}
	// The CAS matches no row when the actor already holds the issue in
	// progress, and ClaimIssueInTx reports that as success. The pre-image is
	// what tells the two apart, and staging nothing for the idempotent case is
	// what keeps a polling caller from minting empty version-control commits.
	// Judged under actorMatches, not verbatim (ga-v2k49): a caller re-claiming
	// its own in-progress issue under a different layer's spelling of its own
	// identity (ga-wzl83) is still a no-op — ClaimIssueInTx's own idempotency
	// check already agrees under the same comparison, and this site would
	// otherwise disagree with it and stage a phantom mutation for a request
	// that wrote nothing (caught by TestExecuteClaimIdempotentReclaimAcross-
	// SpellingStagesNothing during this fix, not cited by the original review).
	tables := ChangedTables{}
	changed := claimed.OldIssue.Status != types.StatusInProgress || !actorMatches(claimed.OldIssue.Assignee, request.Actor)
	if changed {
		issueTable, _, eventTable, _ := WispTableRouting(claimed.IsWisp)
		tables.Add(issueTable, eventTable)
	}
	issue, err := GetIssueInTx(ctx, tx, request.IssueID)
	if err != nil {
		return publicops.ClaimResult{}, nil, err
	}
	// The relations are dropped deliberately, and this is the one place in the
	// facade family where that is true. GetIssueInTx hydrates labels on its
	// way past; the snapshot this role promises is the BARE post-state row,
	// because that is what the frozen v0 claim response carries and what
	// `bd update --claim --json` prints beside it — verified against a labeled
	// issue, which neither surface reports labels for. Hydrating here would
	// change an already-published body.
	issue.Labels = nil
	issue.Dependencies = nil
	issue.Comments = nil
	return publicops.ClaimResult{Issue: issue, Changed: changed}, tables, nil
}

// classifyClaimRefusalInTx attaches the state that lost a compare-and-set to a
// claim refusal, reading it inside the same transaction that lost it. Anything
// that is not a refusal passes through untouched, and so does a refusal whose
// state could not be re-read: the refusal itself is never downgraded to the
// read's failure. The table is the durable one because ExecuteClaim has
// already refused every wisp id by the time a refusal can reach here.
func classifyClaimRefusalInTx(ctx context.Context, tx DBTX, id string, err error) error {
	if !errors.Is(err, storage.ErrAlreadyClaimed) && !errors.Is(err, storage.ErrNotClaimable) {
		return err
	}
	assignee, status, readErr := readClaimStateInTx(ctx, tx, "issues", id)
	if readErr != nil {
		return err
	}
	return &publicops.ClaimConflictError{IssueID: id, Assignee: assignee, Status: status, Err: err}
}
