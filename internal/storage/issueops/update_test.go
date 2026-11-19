package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestManageLeaseOnUpdate pins the clear-only contract (bd-9hpgf, GH#4716):
// generic updates never arm a lease — arming belongs to claim/heartbeat. The
// helper reports that the lease row must be dropped (DeleteLeaseInTx) whenever
// the update leaves the claimed state or changes who holds the claim, and
// leaves the lease untouched when the same claim stays in place.
func TestManageLeaseOnUpdate(t *testing.T) {
	tests := []struct {
		name      string
		oldStatus types.Status
		oldOwner  string
		updates   map[string]interface{}
		wantClear bool // false = lease columns untouched
	}{
		{
			name:      "bare hand-dole claim does not arm",
			oldStatus: types.StatusOpen,
			updates:   map[string]interface{}{"status": "in_progress", "assignee": "crow"},
			wantClear: true,
		},
		{
			name:      "status-only transition into in_progress with existing assignee does not arm",
			oldStatus: types.StatusOpen,
			oldOwner:  "crow",
			updates:   map[string]interface{}{"status": "in_progress"},
			wantClear: true,
		},
		{
			name:      "assignee transfer clears the old owner's lease",
			oldStatus: types.StatusInProgress,
			oldOwner:  "alice",
			updates:   map[string]interface{}{"assignee": "bob"},
			wantClear: true,
		},
		{
			name:      "reopen clears the lease",
			oldStatus: types.StatusInProgress,
			oldOwner:  "alice",
			updates:   map[string]interface{}{"status": "open"},
			wantClear: true,
		},
		{
			name:      "unassign clears the lease",
			oldStatus: types.StatusInProgress,
			oldOwner:  "alice",
			updates:   map[string]interface{}{"assignee": nil},
			wantClear: true,
		},
		{
			name:      "same-claim status re-assert leaves lease alone",
			oldStatus: types.StatusInProgress,
			oldOwner:  "alice",
			updates:   map[string]interface{}{"status": "in_progress"},
			wantClear: false,
		},
		{
			name:      "same-claim assignee re-assert leaves lease alone",
			oldStatus: types.StatusInProgress,
			oldOwner:  "alice",
			updates:   map[string]interface{}{"status": types.StatusInProgress, "assignee": "alice"},
			wantClear: false,
		},
		{
			name:      "no status or assignee in updates is a no-op",
			oldStatus: types.StatusInProgress,
			oldOwner:  "alice",
			updates:   map[string]interface{}{"priority": 1},
			wantClear: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldIssue := &types.Issue{Status: tt.oldStatus, Assignee: tt.oldOwner}

			if cleared := ManageLeaseOnUpdate(oldIssue, tt.updates); cleared != tt.wantClear {
				t.Errorf("clear = %v, want %v", cleared, tt.wantClear)
			}
		})
	}
}

// TestLeaseOutcomeOnUpdate pins the claim-aware lease decision (bd-zprbhg):
// a generic update (claim=false) is clear-only, exactly like
// ManageLeaseOnUpdate; the fields leg of a claim (claim=true) may also ARM a
// lease — for the FINAL assignee the update lands, never the claiming actor —
// so `bd update X --claim --assignee=<other>` leaves the issue in_progress
// under <other> with a lease held by <other> instead of deleting the lease
// the claim leg just armed. The arm is refused for wisps, for patches that
// end the claim, and for pool-alias holders (ApplyLeaseOutcomeInTx keeps
// those leaseless).
func TestLeaseOutcomeOnUpdate(t *testing.T) {
	tests := []struct {
		name        string
		oldStatus   types.Status
		oldOwner    string
		updates     map[string]interface{}
		claim       bool
		isWisp      bool
		wantOutcome LeaseOutcome
		wantHolder  string
	}{
		// claim=false rows mirror TestManageLeaseOnUpdate and never arm.
		{
			name:        "bare hand-dole claim does not arm",
			oldStatus:   types.StatusOpen,
			updates:     map[string]interface{}{"status": "in_progress", "assignee": "crow"},
			wantOutcome: LeaseClear,
		},
		{
			name:        "assignee transfer clears the old owner's lease",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"assignee": "bob"},
			wantOutcome: LeaseClear,
		},
		{
			name:        "same-claim re-assert keeps the lease",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"status": types.StatusInProgress, "assignee": "alice"},
			wantOutcome: LeaseKeep,
		},
		{
			name:        "unrelated field edit keeps the lease",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"priority": 1},
			wantOutcome: LeaseKeep,
		},
		// claim=true rows: the fields leg of a claim.
		{
			name:        "claim with assignee override arms for the final holder",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice", // the claim leg already wrote alice
			updates:     map[string]interface{}{"assignee": "bob"},
			claim:       true,
			wantOutcome: LeaseArm,
			wantHolder:  "bob",
		},
		{
			name:        "claim re-doling the holder's own issue arms for the new holder",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice", // idempotent re-claim wrote nothing
			updates:     map[string]interface{}{"assignee": "bob"},
			claim:       true,
			wantOutcome: LeaseArm,
			wantHolder:  "bob",
		},
		{
			name:        "claim with respelled self-assignee arms for the respelling",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "gastown__mayor",
			updates:     map[string]interface{}{"assignee": "gastown.mayor"},
			claim:       true,
			wantOutcome: LeaseArm,
			wantHolder:  "gastown.mayor",
		},
		{
			name:        "claim override whose patch reverts the status clears",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"assignee": "bob", "status": "open"},
			claim:       true,
			wantOutcome: LeaseClear,
		},
		{
			name:        "claim override landing a custom active status clears",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"assignee": "bob", "status": "triaged"},
			claim:       true,
			wantOutcome: LeaseClear,
		},
		{
			name:        "claim override clearing the assignee clears",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"assignee": nil},
			claim:       true,
			wantOutcome: LeaseClear,
		},
		{
			name:        "claim override on a wisp clears (wisps are never leased)",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"assignee": "bob"},
			claim:       true,
			isWisp:      true,
			wantOutcome: LeaseClear,
		},
		{
			name:        "claim with unchanged fields keeps the claim leg's lease",
			oldStatus:   types.StatusInProgress,
			oldOwner:    "alice",
			updates:     map[string]interface{}{"priority": 1},
			claim:       true,
			wantOutcome: LeaseKeep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldIssue := &types.Issue{Status: tt.oldStatus, Assignee: tt.oldOwner}

			outcome, holder := LeaseOutcomeOnUpdate(oldIssue, tt.updates, tt.claim, tt.isWisp)
			if outcome != tt.wantOutcome || holder != tt.wantHolder {
				t.Errorf("outcome = %v holder = %q, want %v %q", outcome, holder, tt.wantOutcome, tt.wantHolder)
			}
		})
	}
}
