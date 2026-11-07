package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// epicChildRow builds one CHILDREN-section row for printEpicChildProgress.
func epicChildRow(id string, status types.Status, closeReason string) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue: types.Issue{
			ID:          id,
			Title:       id,
			Status:      status,
			CloseReason: closeReason,
		},
		DependencyType: types.DepParentChild,
	}
}

// TestPrintEpicChildProgressEligibility pins GH#5026 and GH#6816 on
// `bd show <epic>`, the CHILDREN-section progress line. The halves are
// asserted together on purpose: the closed/total figure stays RAW (mirroring
// the deliberate raw EpicStatus.ClosedChildren convention that the
// conformance test pins), while the verdict suffix is status-dependent — it
// applies the same non-completing-close rule as EpicStatus.EligibleForClose
// while the epic is open (GH#5026), and flips to "already closed" once the
// epic itself is closed, so the line cannot read identically before and
// after the close (GH#6816). Gating the whole line, or gating nothing, each
// fails exactly one half.
func TestPrintEpicChildProgressEligibility(t *testing.T) {
	tests := []struct {
		name           string
		children       []*types.IssueWithDependencyMetadata
		parentStatus   types.Status
		wantProgress   string
		wantEligible   bool
		wantClosedNote bool
	}{
		{
			name:         "sole child closed as completed work",
			children:     []*types.IssueWithDependencyMetadata{epicChildRow("c-1", types.StatusClosed, "")},
			parentStatus: types.StatusOpen,
			wantProgress: "1/1 complete (100%)",
			wantEligible: true,
		},
		{
			name:         "sole child closed as a duplicate",
			children:     []*types.IssueWithDependencyMetadata{epicChildRow("c-1", types.StatusClosed, "duplicate of c-9")},
			parentStatus: types.StatusOpen,
			wantProgress: "1/1 complete (100%)",
			wantEligible: false,
		},
		{
			name: "one completing close alongside a wont-fix close",
			children: []*types.IssueWithDependencyMetadata{
				epicChildRow("c-1", types.StatusClosed, "fixed"),
				epicChildRow("c-2", types.StatusClosed, "wont-fix"),
			},
			parentStatus: types.StatusOpen,
			wantProgress: "2/2 complete (100%)",
			wantEligible: false,
		},
		{
			name: "an open child keeps the epic ineligible",
			children: []*types.IssueWithDependencyMetadata{
				epicChildRow("c-1", types.StatusClosed, ""),
				epicChildRow("c-2", types.StatusOpen, ""),
			},
			parentStatus: types.StatusOpen,
			wantProgress: "1/2 complete (50%)",
			wantEligible: false,
		},
		{
			// GH#6816 defect 1: before the fix this case printed the same
			// "eligible for close" line as the open-epic case above it.
			name:           "closed epic with all children completed says already closed",
			children:       []*types.IssueWithDependencyMetadata{epicChildRow("c-1", types.StatusClosed, "")},
			parentStatus:   types.StatusClosed,
			wantProgress:   "1/1 complete (100%)",
			wantEligible:   false,
			wantClosedNote: true,
		},
		{
			// The verdict slot tracks the parent's own status even when the
			// children never made the epic eligible: the raw figures stay,
			// the stale invitation to act does not come back.
			name: "closed epic with a non-completing child close says already closed",
			children: []*types.IssueWithDependencyMetadata{
				epicChildRow("c-1", types.StatusClosed, "fixed"),
				epicChildRow("c-2", types.StatusClosed, "wont-fix"),
			},
			parentStatus:   types.StatusClosed,
			wantProgress:   "2/2 complete (100%)",
			wantEligible:   false,
			wantClosedNote: true,
		},
		{
			name: "closed epic with an open child keeps raw progress and says already closed",
			children: []*types.IssueWithDependencyMetadata{
				epicChildRow("c-1", types.StatusClosed, ""),
				epicChildRow("c-2", types.StatusOpen, ""),
			},
			parentStatus:   types.StatusClosed,
			wantProgress:   "1/2 complete (50%)",
			wantEligible:   false,
			wantClosedNote: true,
		},
		{
			name:         "in-progress epic keeps the open-epic verdict",
			children:     []*types.IssueWithDependencyMetadata{epicChildRow("c-1", types.StatusClosed, "")},
			parentStatus: types.StatusInProgress,
			wantProgress: "1/1 complete (100%)",
			wantEligible: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() error {
				printEpicChildProgress(tt.children, tt.parentStatus)
				return nil
			})
			if !strings.Contains(out, tt.wantProgress) {
				t.Errorf("output %q does not contain raw progress %q", out, tt.wantProgress)
			}
			if got := strings.Contains(out, "eligible for close"); got != tt.wantEligible {
				t.Errorf("output %q: %q present = %v, want %v", out, "eligible for close", got, tt.wantEligible)
			}
			if got := strings.Contains(out, "already closed"); got != tt.wantClosedNote {
				t.Errorf("output %q: %q present = %v, want %v", out, "already closed", got, tt.wantClosedNote)
			}
		})
	}
}
