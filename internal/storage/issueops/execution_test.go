package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestClaimAdvancedTheRow pins ga-v2k49 (steveyegge's #5479 re-review, the
// blocking fix-class): ExecuteUpdate's --claim path decided whether to stage
// a mutation by comparing claimed.OldIssue.Assignee to attempt.Actor
// verbatim, so a holder re-claiming under a respelled identity (dotted vs
// sanitized separator, ga-wzl83's repro shape) staged a phantom issues+events
// mutation for a CAS that ClaimIssueInTx itself correctly treated as a no-op.
// claimAdvancedTheRow is the extracted decision, tested directly the same
// way ManageLeaseOnUpdate is above: no SQL involved, ClaimIssueInTx's own
// CAS semantics are pinned separately by public_claim_test.go.
func TestClaimAdvancedTheRow(t *testing.T) {
	tests := []struct {
		name        string
		oldStatus   types.Status
		oldAssignee string
		actor       string
		want        bool
	}{
		{
			name: "fresh claim from open and unassigned", oldStatus: types.StatusOpen, oldAssignee: "",
			actor: "alice", want: true,
		},
		{
			name: "idempotent reclaim by the holder, same spelling", oldStatus: types.StatusInProgress,
			oldAssignee: "alice", actor: "alice", want: false,
		},
		{
			// ga-v2k49's own repro shape (ga-wzl83): the same identity, spelled
			// under two layers' different separator conventions.
			name: "idempotent reclaim by the holder, cross-spelling", oldStatus: types.StatusInProgress,
			oldAssignee: "gastown__mayor", actor: "gastown.mayor", want: false,
		},
		{
			// Not-a-no-op control (canonicalActor's own doc-comment pair): a
			// canonicalization broad enough to match everyone would pass the
			// case above for the wrong reason.
			name: "genuinely different identity despite similar separator style", oldStatus: types.StatusInProgress,
			oldAssignee: "gastown.mayor", actor: "gastown.dog-3", want: true,
		},
		{
			// A pool-alias claim (ClaimPoolAliasesInTx) can win the CAS while
			// already in_progress: the row transitions from the pool alias to
			// the real claimant, which is a genuine mutation despite the status
			// staying in_progress throughout.
			name: "pool-alias claim transitions to the real claimant", oldStatus: types.StatusInProgress,
			oldAssignee: "pool:dogs", actor: "alice", want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claimed := &ClaimResult{OldIssue: &types.Issue{Status: tt.oldStatus, Assignee: tt.oldAssignee}}
			if got := claimAdvancedTheRow(claimed, tt.actor); got != tt.want {
				t.Errorf("claimAdvancedTheRow() = %v, want %v", got, tt.want)
			}
		})
	}
}
