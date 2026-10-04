package main

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// A discarded close reason is a refusal to AMEND, not a failure to CLOSE. The
// distinction is invisible at one id — reportCloseFailures short-circuits on
// total <= 1 with a bare SilentExit() — so the route's single-id integration
// tests stay green either way. These assertions are at the two helpers the
// multi-id path actually consults, which is the only place the two channels can
// be told apart without a live server.
//
// Deliberately untagged, unlike close_reason_amend_test.go: that file needs the
// cgo-gated parityEnv harness, while nothing here does, so this pin also runs in
// the CGO_ENABLED=0 lanes.

const (
	proxiedStoredReason   = "STORED-A"
	proxiedSuppliedReason = "SUPPLIED-B"
)

// closedIssue is a post-close snapshot as the batch engine hands one back.
func closedIssue(id, reason string) *types.Issue {
	return &types.Issue{ID: id, Title: id, Status: types.StatusClosed, CloseReason: reason}
}

// TestCloseProxiedOutcomesDiscardedReasonIsNotAFailure pins the proxied route's
// half of the be-ctr contract: the id whose reason was discarded is reported
// CLOSED and carries a refusal on stderr, but never enters the failure channel.
//
// Before the fix, closeProxiedOutcomes wrote that refusal into pre.errors —
// the slot closeProxiedFailures reads — so the same id printed "✓ Closed" AND
// was counted in "N of M issues failed to close" (and appeared in both the
// closed and the failed --json document), while the direct route, given the
// same batch, produced no failure summary at all. It also made the route's own
// `if reasonDiscarded { return SilentExit() }` unreachable, because a non-empty
// errors slot always returns through reportCloseFailures first.
func TestCloseProxiedOutcomesDiscardedReasonIsNotAFailure(t *testing.T) {
	// amd-1 is already closed and the caller spelled a DIFFERENT reason;
	// amd-2 is a real close; amd-3 is an engine refusal, the control that keeps
	// the failure-channel assertions below from passing vacuously.
	args := []string{"amd-1", "amd-2", "amd-3"}
	blocked := errors.New("cannot close blocked issue: amd-3 is blocked by [amd-9]")

	pre := closeProxiedPreflight{
		items: []issueops.BatchCloseItem{
			{IssueID: "amd-1", Reason: proxiedSuppliedReason},
			{IssueID: "amd-2", Reason: proxiedSuppliedReason},
			{IssueID: "amd-3", Reason: proxiedSuppliedReason},
		},
		itemArgs: []int{0, 1, 2},
		before: map[string]*types.Issue{
			"amd-1": closedIssue("amd-1", proxiedStoredReason),
			"amd-2": {ID: "amd-2", Title: "amd-2", Status: types.StatusOpen},
			"amd-3": {ID: "amd-3", Title: "amd-3", Status: types.StatusOpen},
		},
		errors:         make([]string, len(args)),
		failureErrors:  make([]string, len(args)),
		reasonRefusals: make([]string, len(args)),
	}
	result := issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{
			// Already closed: first-close-wins kept STORED-A.
			{IssueID: "amd-1", Issue: closedIssue("amd-1", proxiedStoredReason), Changed: false},
			{IssueID: "amd-2", Issue: closedIssue("amd-2", proxiedSuppliedReason), Changed: true},
			{IssueID: "amd-3", Err: blocked},
		},
	}

	outcomes, reasons, reasonDiscarded := closeProxiedOutcomes(&pre, result, true)

	if !reasonDiscarded {
		t.Error("reasonDiscarded = false; the caller spelled a reason first-close-wins dropped")
	}
	// amd-1 and amd-2 are closed; amd-3 refused. The discarded reason must not
	// cost amd-1 its outcome — it IS closed, and the ✓ line reports it.
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2 (amd-1 closed, amd-2 closed)", len(outcomes))
	}
	if outcomes[0].id != "amd-1" {
		t.Errorf("outcomes[0].id = %q, want amd-1", outcomes[0].id)
	}
	if reasons[0] != proxiedStoredReason {
		t.Errorf("reported reason = %q, want the stored %q, never the discarded text",
			reasons[0], proxiedStoredReason)
	}

	// The refusal is printed, in the argument's own slot so it stays in typed
	// order — but in the discarded-reason list, not the failure list.
	if pre.reasonRefusals[0] == "" {
		t.Error("no discarded-reason refusal recorded for amd-1; the caller is told nothing")
	}
	if pre.errors[0] != "" {
		t.Errorf("amd-1 entered the failure channel: pre.errors[0] = %q", pre.errors[0])
	}
	if pre.failureErrors[0] != "" {
		t.Errorf("amd-1 entered the machine-readable failure channel: pre.failureErrors[0] = %q", pre.failureErrors[0])
	}

	// The whole point: "N of M issues failed to close" must count amd-3 alone.
	failures := closeProxiedFailures(&pre, args)
	if len(failures) != 1 {
		t.Fatalf("failures = %d (%+v), want exactly 1 — amd-3, the id that did not close", len(failures), failures)
	}
	if failures[0].ID != "amd-3" {
		t.Errorf("failures[0].ID = %q, want amd-3; a discarded reason is not a failed close", failures[0].ID)
	}
}

// TestCloseProxiedOutcomesIdenticalReasonIsSilent is the retry arm, and the
// negative control for the test above: a re-close replaying its OWN reason asks
// for nothing that was refused, so it records no refusal anywhere and the route
// exits 0.
func TestCloseProxiedOutcomesIdenticalReasonIsSilent(t *testing.T) {
	args := []string{"amd-1"}
	pre := closeProxiedPreflight{
		items:          []issueops.BatchCloseItem{{IssueID: "amd-1", Reason: proxiedStoredReason}},
		itemArgs:       []int{0},
		before:         map[string]*types.Issue{"amd-1": closedIssue("amd-1", proxiedStoredReason)},
		errors:         make([]string, len(args)),
		failureErrors:  make([]string, len(args)),
		reasonRefusals: make([]string, len(args)),
	}
	result := issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{
			{IssueID: "amd-1", Issue: closedIssue("amd-1", proxiedStoredReason), Changed: false},
		},
	}

	_, _, reasonDiscarded := closeProxiedOutcomes(&pre, result, true)

	if reasonDiscarded {
		t.Error("reasonDiscarded = true for a replay of the stored reason; the retry must stay silent")
	}
	if pre.reasonRefusals[0] != "" || pre.errors[0] != "" {
		t.Errorf("a replay recorded a refusal: reasonRefusals=%q errors=%q", pre.reasonRefusals[0], pre.errors[0])
	}
	if got := closeProxiedFailures(&pre, args); len(got) != 0 {
		t.Errorf("failures = %+v, want none for an idempotent replay", got)
	}
}
