package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

// showJSONIssueDetails is the CLI-only detail projection. RowVersion remains
// absent from generic Issue JSON and JSONL, while `bd show --json` exposes the
// opaque token required by guarded clients under the storage-neutral name
// `revision`.
//
// The field is emitted WITHOUT omitempty. 0 is a legitimate, comparable token:
// a guarded write that expects 0 matches an un-mutated legacy migration-0054
// row and fails any current (non-zero) row — correct CAS semantics. Omitting it
// would leave a guarded client unable to read the 0 it must send on exactly the
// legacy rows the token protects, and would make an absent field ambiguously
// mean either "legacy-zero" or "no token". `revision` is therefore always
// present, including 0.
type showJSONIssueDetails struct {
	*types.IssueDetails
	Revision int64 `json:"revision"`
}

func projectShowJSONDetails(details *types.IssueDetails) showJSONIssueDetails {
	return showJSONIssueDetails{IssueDetails: details, Revision: details.RowVersion}
}

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
