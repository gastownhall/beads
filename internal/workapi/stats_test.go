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
		gates      int
		templates  int
		infra      int
		// cfg is the workspace's list configuration; the zero value is the
		// built-in infra set (agent, role, message), as it is for the listing.
		cfg ListConfig
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
		{
			// The rows a default `bd list --assignee` will not show. This
			// route's filter sets only Assignee, so IsTemplate stays nil and
			// the actor's gates and protos really are in these rows - leaving
			// the two counts at zero would give the scoped answer the same
			// silent disagreement with its own listing the workspace-wide
			// answer had. They overlap the status buckets rather than forming
			// their own, exactly as PinnedIssues does workspace-wide.
			name: "gates and protos are broken out of the total, not removed from it",
			issues: []*types.Issue{
				{Status: types.StatusOpen},
				{Status: types.StatusOpen, IssueType: types.TypeGate},
				{Status: types.StatusClosed, IssueType: types.TypeGate},
				{Status: types.StatusOpen, IsTemplate: true},
			},
			ready: 1, total: 4, open: 3, closed: 1, gates: 2, templates: 1,
		},
		{
			// A gate that is also a proto is counted in both, since the two
			// listing suppressions are independent.
			name:   "a templated gate is counted in both breakdowns",
			issues: []*types.Issue{{Status: types.StatusOpen, IssueType: types.TypeGate, IsTemplate: true}},
			ready:  0, total: 1, open: 1, gates: 1, templates: 1,
		},
		{
			// `bd list --assignee` excludes the infra types too, so the actor's
			// infra-typed rows are the third breakdown. With nothing configured
			// that is the built-in set.
			name: "infra-typed rows are broken out under the built-in set",
			issues: []*types.Issue{
				{Status: types.StatusOpen, IssueType: types.TypeTask},
				{Status: types.StatusOpen, IssueType: types.IssueType("agent")},
				{Status: types.StatusClosed, IssueType: types.IssueType("message")},
			},
			ready: 1, total: 3, open: 2, closed: 1, infra: 2,
		},
		{
			// A configured types.infra REPLACES the built-in set, and the
			// breakdown must follow it: agent is an ordinary type here and
			// gate is infra, so a gate lands in both the gate and infra
			// counts, as the listing suppresses it on both grounds. The rows
			// are chosen so the two sets DISAGREE - two gates and one agent
			// give 2 under the configured set and 1 under the built-in one -
			// so a fold that ignored cfg fails here.
			name: "a configured infra set replaces the built-in one",
			issues: []*types.Issue{
				{Status: types.StatusOpen, IssueType: types.IssueType("agent")},
				{Status: types.StatusOpen, IssueType: types.TypeGate},
				{Status: types.StatusClosed, IssueType: types.TypeGate},
			},
			cfg:   ListConfig{InfraSet: map[string]bool{"gate": true}},
			ready: 0, total: 3, open: 2, closed: 1, gates: 2, infra: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FoldStatsAssigneeSummary(tt.issues, tt.ready, tt.cfg)
			if got.TotalIssues != tt.total || got.OpenIssues != tt.open || got.InProgressIssues != tt.inProgress || got.DeferredIssues != tt.deferred || got.ClosedIssues != tt.closed {
				t.Errorf("FoldStatsAssigneeSummary() = %+v, want total=%d open=%d in_progress=%d deferred=%d closed=%d", got, tt.total, tt.open, tt.inProgress, tt.deferred, tt.closed)
			}
			if got.BlockedIssues == nil || *got.BlockedIssues != tt.blocked {
				t.Errorf("blocked issues = %v, want %d", got.BlockedIssues, tt.blocked)
			}
			if got.ReadyIssues == nil || *got.ReadyIssues != tt.ready {
				t.Errorf("ready issues = %v, want %d", got.ReadyIssues, tt.ready)
			}
			if got.GateIssues != tt.gates || got.TemplateIssues != tt.templates || got.InfraIssues != tt.infra {
				t.Errorf("gate/template/infra issues = %d/%d/%d, want %d/%d/%d", got.GateIssues, got.TemplateIssues, got.InfraIssues, tt.gates, tt.templates, tt.infra)
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

// TestCountStatsInfraIssues pins that the workspace-wide infra breakdown sums
// the CONFIGURED set, with the listing's fallback to the built-in names when
// nothing is configured, and ignores every other type.
func TestCountStatsInfraIssues(t *testing.T) {
	byType := map[string]int{"task": 5, "agent": 2, "message": 1, "gate": 4}

	if got := CountStatsInfraIssues(byType, ListConfig{}); got != 3 {
		t.Errorf("built-in set: got %d, want 3 (agent + message)", got)
	}
	configured := ListConfig{InfraSet: map[string]bool{"gate": true, "role": true}}
	if got := CountStatsInfraIssues(byType, configured); got != 4 {
		t.Errorf("configured set {gate, role}: got %d, want 4 - agent is not infra once the set is replaced", got)
	}
	if got := CountStatsInfraIssues(nil, ListConfig{}); got != 0 {
		t.Errorf("no rows: got %d, want 0", got)
	}
}
