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

// TestLeaseOutcomeOnUpdate pins the lease decision both update bodies share
// (bd-zprbhg). Without claim it is ManageLeaseOnUpdate and never arms. With
// claim (the fields half of `bd update --claim`), a patch that hands the claim
// on arms a lease for the holder it leaves, so the claim stays recoverable.
// oldIssue is the row the fields leg reads, i.e. the row after the claim leg.
func TestLeaseOutcomeOnUpdate(t *testing.T) {
	tests := []struct {
		name       string
		oldStatus  types.Status
		oldOwner   string
		updates    map[string]interface{}
		claim      bool
		wisp       bool
		wantResult LeaseOutcome
		wantHolder string
	}{
		{
			name:       "claim with an assignee override arms for the override",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"assignee": "bob"},
			claim:      true,
			wantResult: LeaseArm,
			wantHolder: "bob",
		},
		{
			name:       "claim re-asserting its own status and assignee keeps the claim leg's lease",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"status": types.StatusInProgress, "assignee": "alice"},
			claim:      true,
			wantResult: LeaseKeep,
		},
		{
			name:       "claim with no status or assignee keeps the claim leg's lease",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"priority": 1},
			claim:      true,
			wantResult: LeaseKeep,
		},
		{
			name:       "claim with a respelled self-assignee arms for the respelled bytes",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "gastown/alice",
			updates:    map[string]interface{}{"assignee": "gastown.alice"},
			claim:      true,
			wantResult: LeaseArm,
			wantHolder: "gastown.alice",
		},
		{
			name:       "claim whose patch reopens the issue clears",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"status": types.StatusOpen, "assignee": "bob"},
			claim:      true,
			wantResult: LeaseClear,
		},
		{
			name:       "claim whose patch blocks the issue clears",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"status": types.StatusBlocked},
			claim:      true,
			wantResult: LeaseClear,
		},
		{
			name:       "claim whose patch moves to a custom status clears",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"status": "triaged"},
			claim:      true,
			wantResult: LeaseClear,
		},
		{
			name:       "claim whose patch unassigns clears",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"assignee": ""},
			claim:      true,
			wantResult: LeaseClear,
		},
		{
			name:       "claim whose patch nils the assignee clears",
			oldStatus:  types.StatusInProgress,
			oldOwner:   "alice",
			updates:    map[string]interface{}{"assignee": nil},
			claim:      true,
			wantResult: LeaseClear,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldIssue := &types.Issue{Status: tt.oldStatus, Assignee: tt.oldOwner}
			got, holder := LeaseOutcomeOnUpdate(oldIssue, tt.updates, tt.claim, tt.wisp)
			if got != tt.wantResult || holder != tt.wantHolder {
				t.Errorf("LeaseOutcomeOnUpdate = (%v, %q), want (%v, %q)", got, holder, tt.wantResult, tt.wantHolder)
			}
		})
	}
}
