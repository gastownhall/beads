package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestPrintHumanStats(t *testing.T) {
	tests := []struct {
		name   string
		issues []*types.Issue
		// We just verify no panic; output goes to stdout
	}{
		{
			name:   "empty list",
			issues: nil,
		},
		{
			name: "mixed statuses",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "open"},
				{ID: "bd-2", Status: "in_progress"},
				{ID: "bd-3", Status: "blocked"},
				{ID: "bd-4", Status: "closed", CloseReason: "Responded"},
				{ID: "bd-5", Status: "closed", CloseReason: "Dismissed: not needed"},
				{ID: "bd-6", Status: "hooked"},
			},
		},
		{
			name: "all closed responded",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "closed", CloseReason: "Responded"},
				{ID: "bd-2", Status: "closed", CloseReason: "Responded"},
			},
		},
		{
			name: "all dismissed",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "closed", CloseReason: "Dismissed"},
				{ID: "bd-2", Status: "closed", CloseReason: "Dismissed: stale"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify no panic
			printHumanStats(tt.issues)
		})
	}
}

func TestPrintHumanList(t *testing.T) {
	tests := []struct {
		name   string
		issues []*types.Issue
	}{
		{
			name:   "empty list",
			issues: nil,
		},
		{
			name: "single issue",
			issues: []*types.Issue{
				{ID: "bd-abc", Title: "Need human input", Status: "open", Priority: 1},
			},
		},
		{
			name: "multiple issues with varied status",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Review needed", Status: "open"},
				{ID: "bd-2", Title: "Approval required", Status: "blocked", Priority: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify no panic
			printHumanList(tt.issues)
		})
	}
}

func TestHumanCmdSubcommands(t *testing.T) {
	// Verify all subcommands are registered
	subCmds := humanCmd.Commands()
	names := make([]string, len(subCmds))
	for i, cmd := range subCmds {
		names[i] = cmd.Name()
	}
	joined := strings.Join(names, ",")

	for _, expected := range []string{"list", "respond", "dismiss", "stats"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing subcommand %q in human command", expected)
		}
	}
}

func TestHumanRespondRequiresResponseFlag(t *testing.T) {
	flag := humanRespondCmd.Flags().Lookup("response")
	if flag == nil {
		t.Fatal("respond command should have --response flag")
	}
}

func TestHumanDismissHasReasonFlag(t *testing.T) {
	flag := humanDismissCmd.Flags().Lookup("reason")
	if flag == nil {
		t.Fatal("dismiss command should have --reason flag")
	}
}

func TestHumanListHasStatusFlag(t *testing.T) {
	flag := humanListCmd.Flags().Lookup("status")
	if flag == nil {
		t.Fatal("list command should have --status flag")
	}
}

// sk-1pc: closing a bead does not clear its 'human' label, so resolved beads
// kept showing up in the operator's decision queue until someone stripped the
// label by hand. The default listing must drop them; an explicit --status must
// still reach any status, closed included.
func TestHumanListFilterExcludesClosed(t *testing.T) {
	t.Parallel()

	t.Run("default excludes closed", func(t *testing.T) {
		t.Parallel()
		filter := humanListFilter("")
		if got := filter.ExcludeStatus; len(got) != 1 || got[0] != types.StatusClosed {
			t.Errorf("ExcludeStatus = %v, want [%s]", got, types.StatusClosed)
		}
		if filter.Status != nil {
			t.Errorf("Status = %v, want nil (every non-closed status is still pending)", *filter.Status)
		}
		if len(filter.Labels) != 1 || filter.Labels[0] != "human" {
			t.Errorf("Labels = %v, want [human]", filter.Labels)
		}
	})

	t.Run("explicit status wins, including closed", func(t *testing.T) {
		t.Parallel()
		for _, status := range []string{"closed", "open", "in_progress"} {
			filter := humanListFilter(status)
			if filter.Status == nil || string(*filter.Status) != status {
				t.Errorf("humanListFilter(%q).Status = %v, want %q", status, filter.Status, status)
			}
			if len(filter.ExcludeStatus) != 0 {
				t.Errorf("humanListFilter(%q).ExcludeStatus = %v, want empty", status, filter.ExcludeStatus)
			}
		}
	})

	// The stats are the reason the label survives a close: Responded and
	// Dismissed are counts of closed beads, so that query must not inherit
	// the list's exclusion.
	t.Run("stats still sees closed", func(t *testing.T) {
		t.Parallel()
		filter := humanStatsFilter()
		if len(filter.ExcludeStatus) != 0 || filter.Status != nil {
			t.Errorf("humanStatsFilter must not constrain status, got Status=%v ExcludeStatus=%v",
				filter.Status, filter.ExcludeStatus)
		}
	})
}
