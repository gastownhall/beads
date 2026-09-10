package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The shared half of issueops.StatsReporter: the assignee-scoped question,
// which every implementation answers by asking storage two questions and
// folding the answers, rather than by running one aggregate.
//
// The workspace-wide question needs nothing here — it is a single seam call on
// every backend. This file exists because the assignee-scoped answer is
// ASSEMBLED, and assembling it twice is how the two front doors would come to
// disagree about what "your work" means.

// ValidateStatsAssignee resolves the actor an assignee-scoped summary answers
// for, refusing an empty or whitespace-only one with ErrValidation.
//
// It returns the value UNCHANGED when it accepts: an assignee is an opaque
// identifier this layer has no vocabulary for, so trimming it would silently
// answer for a different actor than the caller named.
func ValidateStatsAssignee(assignee string) (string, error) {
	if strings.TrimSpace(assignee) == "" {
		return "", fmt.Errorf("assignee must not be empty for an assignee-scoped summary%.0w", issueops.ErrValidation)
	}
	return assignee, nil
}

// BuildStatsAssigneeIssueFilter is the predicate that selects one actor's rows
// for the fold below.
//
// It carries the assignee and NOTHING else, which is a decision and not an
// omission: no status restriction (the fold tallies every status, including
// closed), no limit (a capped scan would silently under-report a busy actor's
// total), and no wisp suppression — the search seam merges the ephemeral tier
// unless told not to.
func BuildStatsAssigneeIssueFilter(assignee string) types.IssueFilter {
	return types.IssueFilter{Assignee: &assignee}
}

// BuildStatsAssigneeWorkFilter is the ready-work predicate for the same actor.
// It is a second filter type because ready work is a different question with
// its own exclusions — which is why AssigneeStats can report a ready count the
// workspace-wide answer's subtraction would not produce.
func BuildStatsAssigneeWorkFilter(assignee string) types.WorkFilter {
	return types.WorkFilter{Assignee: &assignee}
}

// FoldStatsAssigneeSummary turns one actor's rows and their ready-work count
// into the summary both front doors print.
//
// It is the single definition of what `bd status --assigned` means. What it
// does NOT set is part of that definition: PinnedIssues,
// EpicsEligibleForClosure and AverageLeadTime stay zero.
//
// BlockedIssues and ReadyIssues are always non-nil here, including for an actor
// with no rows at all. The nil pointers are the workspace-wide answer's
// skipped-scan signal (issueops.StatsRequest.SkipBlocked) and mean "not
// computed"; this path always computes both, so leaving them nil would render
// as "(skipped)" on a summary that skipped nothing.
//
// A nil element is skipped and counted nowhere, TotalIssues included: counting
// a row the seam failed to hydrate while it lands in no status bucket would
// publish an inconsistency as data.
func FoldStatsAssigneeSummary(issues []*types.Issue, readyCount int) types.Statistics {
	stats := types.Statistics{}

	blocked := 0
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		stats.TotalIssues++
		switch issue.Status {
		case types.StatusOpen:
			stats.OpenIssues++
		case types.StatusInProgress:
			stats.InProgressIssues++
		case types.StatusBlocked:
			// The STATUS, not the transitive is_blocked flag the
			// workspace-wide answer counts. The two disagree in both
			// directions and AssigneeStats says so.
			blocked++
		case types.StatusDeferred:
			stats.DeferredIssues++
		case types.StatusClosed:
			stats.ClosedIssues++
		}
	}
	stats.BlockedIssues = &blocked
	stats.ReadyIssues = &readyCount
	return stats
}
