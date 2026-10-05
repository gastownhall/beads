package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

// validateIssueUpdatable checks if an issue can be updated.
// Uses the centralized validation package for consistency.
func validateIssueUpdatable(id string, issue *types.Issue) error {
	// Note: We use NotTemplate() directly instead of ForUpdate() to maintain
	// backward compatibility - the original didn't check for nil issues.
	return validation.NotTemplate()(id, issue)
}

// validateIssueClosable checks if an issue can be closed.
// Uses the centralized validation package for consistency.
//
// actor is the current actor identity (may be empty in early-init contexts);
// AssigneeMatches refuses the close when the bead is assigned to someone else
// unless force is true. This is the authority guard for be-035.
func validateIssueClosable(id string, issue *types.Issue, actor string, force bool) error {
	// Note: We use individual validators instead of ForClose() to maintain
	// backward compatibility - the original didn't check for nil issues.
	return validation.Chain(
		validation.NotTemplate(),
		validation.NotPinned(force),
		validation.AssigneeMatches(actor, force),
	)(id, issue)
}

// validateIssueReassignable checks whether an assignee update may proceed:
// plain `bd update -a` / `bd assign` must not silently overwrite another
// actor's live in_progress claim (bd-98s5c) — it was the last unfenced
// cross-actor takeover path after --claim, unclaim, and close were fenced.
//
// Call it only when the update actually carries an assignee change;
// newAssignee may be "" (unassign), which is fenced the same way. Do NOT call
// it when the caller passed an explicit --if-assignee guard: that CAS names
// the holder, so nothing about it is silent, and it is how sanctioned X→Y
// transfers (park) work without needing the fence bypass (under
// --if-assignee, --force never arms the assignee half).
func validateIssueReassignable(id string, issue *types.Issue, actor, newAssignee string, poolAliases func() []string, force bool) error {
	return validation.AssigneeNotStolen(actor, newAssignee, poolAliases, force)(id, issue)
}

// ifRevisionAlreadyStale reports whether issue's own already-read RowVersion
// no longer matches ifRevision — that is, whether this pre-write read already
// knows the write is a foregone --if-revision precondition failure. Every
// validateIssueReassignable call site that also honors --if-revision
// (mc-zndi7.74) checks this FIRST and skips the fence when it is true.
//
// Why: --if-revision's correctness comes from the atomic compare-and-set
// inside the guarded write itself, which already orders its own version check
// ahead of the assignee-transfer fence (ExecuteUpdate checks ExpectedVersion
// before calling AuthorizeAssigneeTransfer; the uow leg's
// updatePreconditionsHold stands the fence down the same way before
// ApplyUpdate's own version check runs). validateIssueReassignable's callers
// above, though, read the issue in their OWN earlier, separately-timed
// request — not inside that write's transaction — so a racing winner's commit
// can land in the gap and leave this read already showing the winner's new
// assignee. Without this check the loser's pre-read trips the live-claim
// fence and reports the plain "already claimed" policy refusal (exit 1)
// before ever reaching the guarded write that would have reported
// precondition_failed (exit 13) — the wrong outcome for a caller whose whole
// point in passing --if-revision was to get a crisp, retryable signal that it
// lost a race rather than an ordinary policy refusal.
//
// ifRevision == nil (no active guard) always returns false, leaving every
// unguarded caller's behavior exactly as before.
func ifRevisionAlreadyStale(issue *types.Issue, ifRevision *int64) bool {
	return ifRevision != nil && issue.RowVersion != *ifRevision
}

// storeClaimPoolAliases returns a lazy claim.pools reader against a direct
// store, for the reassign fence's pool-alias carve-out. Config read errors
// yield no aliases — the fence fails closed (refuses) rather than allowing a
// takeover it can't verify.
func storeClaimPoolAliases(ctx context.Context, st storage.DoltStorage) func() []string {
	return func() []string {
		raw, err := st.GetConfig(ctx, "claim.pools")
		if err != nil {
			return nil
		}
		return issueops.ParseClaimPools(raw)
	}
}

// uowClaimPoolAliases is storeClaimPoolAliases' proxied-server sibling: the
// same lazy claim.pools read through the unit of work's transaction.
func uowClaimPoolAliases(ctx context.Context, uw uow.UnitOfWork) func() []string {
	return func() []string {
		raw, err := uw.ConfigUseCase().GetConfig(ctx, "claim.pools")
		if err != nil {
			return nil
		}
		return issueops.ParseClaimPools(raw)
	}
}
