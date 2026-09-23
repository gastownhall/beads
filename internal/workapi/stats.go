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
// The workspace-wide question is a single seam call on every backend, plus the
// one number that seam cannot compute: the durable rows of the workspace's
// configured infra types (CountStatsInfraIssues below). This file exists
// because both answers are partly ASSEMBLED, and assembling either twice is how
// the two front doors would come to disagree about what they mean.

// StatsInfraCountFilter is the per-type count CountStatsInfraIssues folds: the
// durable plane only, with no other restriction. It matches the population
// TotalIssues counts (ScanIssueCountsInTx scans issues alone, templates and
// closed rows included), so InfraIssues is a breakdown of that total and not of
// some other set.
func StatsInfraCountFilter() types.IssueFilter {
	return types.IssueFilter{SkipWisps: true}
}

// CountStatsInfraIssues sums per-type durable counts over the workspace's
// configured infra set - the rows a default `bd list` hides as infra types.
//
// The set comes from cfg, the same ListConfig the listing's
// applyTypeSuppressions reads, and NOT from the built-in names: a configured
// types.infra replaces the built-in set, and changing it moves no rows already
// written. So a durable row of a type that has since become infra is counted
// by bd status and hidden by bd list, and only the configured set can say
// which rows those are. That is also why the count is taken here rather than
// in ScanIssueCountsInTx, which is portable SQL with no configuration seam.
func CountStatsInfraIssues(byType map[string]int, cfg ListConfig) int {
	total := 0
	for _, t := range cfg.InfraTypes() {
		total += byType[t]
	}
	return total
}

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
// GateIssues, TemplateIssues and InfraIssues ARE set. They are not
// workspace-wide numbers borrowed into a scoped answer: this route's filter
// sets only Assignee, so IsTemplate stays nil and the actor's gates, protos and
// infra-typed rows really are among these rows, while `bd list --assignee`
// suppresses all three. Leaving them zero would give the scoped answer the same
// silent disagreement with its own listing that the workspace-wide answer had.
// Which types are infra is cfg's answer - the workspace's configured set, as
// the listing reads it - not the built-in names.
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
func FoldStatsAssigneeSummary(issues []*types.Issue, readyCount int, cfg ListConfig) types.Statistics {
	stats := types.Statistics{}

	blocked := 0
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		stats.TotalIssues++
		// The rows a default `bd list --assignee` will not show. The same
		// reconciliation gap the workspace-wide answer has: this filter sets
		// only Assignee, so IsTemplate stays nil and gates and templates ARE
		// in these rows, while the listing suppresses both.
		if issue.IssueType == types.TypeGate {
			stats.GateIssues++
		}
		if issue.IsTemplate {
			stats.TemplateIssues++
		}
		if cfg.IsInfra(string(issue.IssueType)) {
			stats.InfraIssues++
		}
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
