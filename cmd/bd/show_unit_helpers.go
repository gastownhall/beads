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
// transfers (park) work without --force (--force and --if-assignee are
// mutually exclusive).
func validateIssueReassignable(id string, issue *types.Issue, actor, newAssignee string, poolAliases func() []string, force bool) error {
	return validation.AssigneeNotStolen(actor, newAssignee, poolAliases, force)(id, issue)
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

func applyLabelUpdates(ctx context.Context, st storage.DoltStorage, issueID, actor string, setLabels, addLabels, removeLabels []string) error {
	// Set labels (replaces all existing labels)
	if len(setLabels) > 0 {
		currentLabels, err := st.GetLabels(ctx, issueID)
		if err != nil {
			return err
		}
		for _, label := range currentLabels {
			if err := st.RemoveLabel(ctx, issueID, label, actor); err != nil {
				return err
			}
		}
		for _, label := range setLabels {
			if err := st.AddLabel(ctx, issueID, label, actor); err != nil {
				return err
			}
		}
	}

	// Add labels
	for _, label := range addLabels {
		if err := st.AddLabel(ctx, issueID, label, actor); err != nil {
			return err
		}
	}

	// Remove labels
	for _, label := range removeLabels {
		if err := st.RemoveLabel(ctx, issueID, label, actor); err != nil {
			return err
		}
	}

	return nil
}
