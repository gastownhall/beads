package workapi

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestValidateStatsAssignee pins the one refusal on the summary role, and pins
// it here because it needs no database: an assignee-scoped summary of nobody is
// ErrValidation, and an accepted assignee comes back BYTE-IDENTICAL. The second
// half is the load-bearing one: trimming would answer for a different actor
// than the caller named.
func TestValidateStatsAssignee(t *testing.T) {
	for _, blank := range []string{"", " ", "\t\n"} {
		got, err := ValidateStatsAssignee(blank)
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateStatsAssignee(%q) error = %v, want ErrValidation", blank, err)
		}
		if got != "" {
			t.Errorf("ValidateStatsAssignee(%q) = %q alongside a refusal, want the empty string", blank, got)
		}
	}
	for _, assignee := range []string{"alice", " alice ", "Alice", "agent:worker-3"} {
		got, err := ValidateStatsAssignee(assignee)
		if err != nil {
			t.Errorf("ValidateStatsAssignee(%q): %v", assignee, err)
		}
		if got != assignee {
			t.Errorf("ValidateStatsAssignee(%q) = %q, want it unchanged", assignee, got)
		}
	}
}

// TestBuildStatsAssigneeFiltersCarryOnlyTheAssignee pins what the two
// assignee-scoped predicates deliberately leave out. A status restriction, a
// limit or a wisp suppression appearing in either of them would change what
// `bd status --assigned` counts without changing a line of either front door.
func TestBuildStatsAssigneeFiltersCarryOnlyTheAssignee(t *testing.T) {
	issueFilter := BuildStatsAssigneeIssueFilter("alice")
	if issueFilter.Assignee == nil || *issueFilter.Assignee != "alice" {
		t.Fatalf("issue filter assignee = %v, want alice", issueFilter.Assignee)
	}
	if issueFilter.Limit != 0 {
		t.Errorf("issue filter limit = %d, want 0 — a capped scan under-reports a busy actor's total", issueFilter.Limit)
	}
	if issueFilter.Status != nil || len(issueFilter.Statuses) != 0 {
		t.Errorf("issue filter restricts status (%v/%v); the fold tallies every status including closed", issueFilter.Status, issueFilter.Statuses)
	}
	if issueFilter.SkipWisps {
		t.Error("issue filter skips wisps; AssigneeStats documents the merged tier")
	}

	workFilter := BuildStatsAssigneeWorkFilter("alice")
	if workFilter.Assignee == nil || *workFilter.Assignee != "alice" {
		t.Fatalf("work filter assignee = %v, want alice", workFilter.Assignee)
	}
}

// TestFoldStatsAssigneeSummary is the definition of what `bd status --assigned`
// means, checked without a database. Both routes read it from this one function,
// so a change to any number below is a change to both surfaces at once.
func TestFoldStatsAssigneeSummary(t *testing.T) {
	tests := []struct {
		name       string
		issues     []*types.Issue
		ready      int
		total      int
		open       int
		inProgress int
		blocked    int
		deferred   int
		closed     int
	}{
		{
			name: "counts every status and ready work",
			issues: []*types.Issue{
				{Status: types.StatusOpen},
				{Status: types.StatusInProgress},
				{Status: types.StatusBlocked},
				{Status: types.StatusDeferred},
				{Status: types.StatusClosed},
			},
			ready: 2, total: 5, open: 1, inProgress: 1, blocked: 1, deferred: 1, closed: 1,
		},
		{
			name:  "empty input retains explicit zero counts",
			ready: 0,
		},
		{
			// A status outside the five the fold knows lands in the total and
			// in no bucket, exactly as the workspace-wide answer's tallies do.
			name:   "an unknown status is counted once and bucketed nowhere",
			issues: []*types.Issue{{Status: types.Status("triage")}, {Status: types.StatusOpen}},
			ready:  1, total: 2, open: 1,
		},
		{
			name:   "a nil row is counted nowhere at all",
			issues: []*types.Issue{nil, {Status: types.StatusOpen}},
			ready:  0, total: 1, open: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FoldStatsAssigneeSummary(tt.issues, tt.ready)
			if got.TotalIssues != tt.total || got.OpenIssues != tt.open || got.InProgressIssues != tt.inProgress || got.DeferredIssues != tt.deferred || got.ClosedIssues != tt.closed {
				t.Errorf("FoldStatsAssigneeSummary() = %+v, want total=%d open=%d in_progress=%d deferred=%d closed=%d", got, tt.total, tt.open, tt.inProgress, tt.deferred, tt.closed)
			}
			if got.BlockedIssues == nil || *got.BlockedIssues != tt.blocked {
				t.Errorf("blocked issues = %v, want %d", got.BlockedIssues, tt.blocked)
			}
			if got.ReadyIssues == nil || *got.ReadyIssues != tt.ready {
				t.Errorf("ready issues = %v, want %d", got.ReadyIssues, tt.ready)
			}
			// The three fields AssigneeStats says are always zero here. They
			// are not "not yet implemented" on this path: the fold has no
			// input that could produce them.
			if got.PinnedIssues != 0 || got.EpicsEligibleForClosure != 0 || got.AverageLeadTime != 0 {
				t.Errorf("extended fields = %d/%d/%v, want zeros", got.PinnedIssues, got.EpicsEligibleForClosure, got.AverageLeadTime)
			}
		})
	}
}
