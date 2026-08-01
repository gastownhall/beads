package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildAssignedStats(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAssignedStats(tt.issues, tt.ready)
			if got.TotalIssues != tt.total || got.OpenIssues != tt.open || got.InProgressIssues != tt.inProgress || got.DeferredIssues != tt.deferred || got.ClosedIssues != tt.closed {
				t.Errorf("buildAssignedStats() = %+v, want total=%d open=%d in_progress=%d deferred=%d closed=%d", got, tt.total, tt.open, tt.inProgress, tt.deferred, tt.closed)
			}
			if got.BlockedIssues == nil || *got.BlockedIssues != tt.blocked {
				t.Errorf("blocked issues = %v, want %d", got.BlockedIssues, tt.blocked)
			}
			if got.ReadyIssues == nil || *got.ReadyIssues != tt.ready {
				t.Errorf("ready issues = %v, want %d", got.ReadyIssues, tt.ready)
			}
		})
	}
}

func TestRenderStatusJSON(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "0")

	count := func(n int) *int { return &n }
	tests := []struct {
		name    string
		stats   *types.Statistics
		skipped bool
		blocked *int
		ready   *int
	}{
		{
			name:    "includes computed counts",
			stats:   &types.Statistics{TotalIssues: 3, OpenIssues: 1, BlockedIssues: count(1), ReadyIssues: count(1)},
			blocked: count(1), ready: count(1),
		},
		{
			name:    "preserves skipped counts as null",
			stats:   &types.Statistics{TotalIssues: 3, OpenIssues: 2, ClosedIssues: 1},
			skipped: true,
		},
	}

	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() error { return renderStatus(tt.stats, nil) })
			var got StatusOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("unmarshal status JSON: %v\n%s", err, out)
			}
			if got.Summary == nil || got.Summary.TotalIssues != tt.stats.TotalIssues {
				t.Errorf("summary = %+v, want %+v", got.Summary, tt.stats)
			}
			if got.BlockedCountSkipped != tt.skipped {
				t.Errorf("blocked_count_skipped = %t, want %t", got.BlockedCountSkipped, tt.skipped)
			}
			if (got.Summary.BlockedIssues == nil) != (tt.blocked == nil) || (got.Summary.BlockedIssues != nil && *got.Summary.BlockedIssues != *tt.blocked) {
				t.Errorf("blocked_issues = %v, want %v", got.Summary.BlockedIssues, tt.blocked)
			}
			if (got.Summary.ReadyIssues == nil) != (tt.ready == nil) || (got.Summary.ReadyIssues != nil && *got.Summary.ReadyIssues != *tt.ready) {
				t.Errorf("ready_issues = %v, want %v", got.Summary.ReadyIssues, tt.ready)
			}
			if got.RecentActivity != nil {
				t.Errorf("recent activity = %+v, want nil", got.RecentActivity)
			}
			if tt.skipped && (!strings.Contains(out, `"blocked_issues": null`) || !strings.Contains(out, `"ready_issues": null`)) {
				t.Errorf("skipped counts must be encoded as null:\n%s", out)
			}
		})
	}
}

func TestRenderStatusHuman(t *testing.T) {
	count := func(n int) *int { return &n }
	tests := []struct {
		name     string
		stats    *types.Statistics
		activity *RecentActivitySummary
		contains []string
		absent   []string
	}{
		{
			name:     "normal counts",
			stats:    &types.Statistics{TotalIssues: 3, OpenIssues: 1, InProgressIssues: 1, ClosedIssues: 1, BlockedIssues: count(1), ReadyIssues: count(2)},
			contains: []string{"Issue Database Status", "Total Issues:           3", "Blocked:                1", "Ready to Work:          2"},
			absent:   []string{"(skipped)"},
		},
		{
			name:     "skipped counts",
			stats:    &types.Statistics{TotalIssues: 1},
			contains: []string{"Blocked:                (skipped)", "Ready to Work:          (skipped)"},
		},
		{
			name:     "extended statistics",
			stats:    &types.Statistics{BlockedIssues: count(0), ReadyIssues: count(0), PinnedIssues: 2, EpicsEligibleForClosure: 3, AverageLeadTime: 4.5},
			contains: []string{"Extended:", "Pinned:                 2", "Epics Ready to Close:   3", "Avg Lead Time:          4.5 hours"},
		},
		{
			name:     "recent activity",
			stats:    &types.Statistics{BlockedIssues: count(0), ReadyIssues: count(0)},
			activity: &RecentActivitySummary{HoursTracked: 24, CommitCount: 1, TotalChanges: 2, IssuesCreated: 3, IssuesClosed: 4, IssuesReopened: 5, IssuesUpdated: 6},
			contains: []string{"Recent Activity (last 24 hours):", "Commits:                1", "Total Changes:          2", "Issues Created:         3", "Issues Closed:          4", "Issues Reopened:        5", "Issues Updated:         6"},
		},
	}

	oldJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = oldJSON }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() error { return renderStatus(tt.stats, tt.activity) })
			for _, want := range tt.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output does not contain %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tt.absent {
				if strings.Contains(out, unwanted) {
					t.Errorf("output unexpectedly contains %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

func TestGetGitActivityReturnsNil(t *testing.T) {
	if got := getGitActivity(24); got != nil {
		t.Errorf("getGitActivity(24) = %+v, want nil", got)
	}
}

func TestStatusCommandAliases(t *testing.T) {
	if len(statusCmd.Aliases) != 1 || statusCmd.Aliases[0] != "stats" {
		t.Errorf("status aliases = %q, want [stats]", statusCmd.Aliases)
	}
}
