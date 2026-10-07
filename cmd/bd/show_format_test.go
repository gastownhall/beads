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

// TestPrintEpicChildProgressEligibility pins GH#5026 on `bd show <epic>`, the
// CHILDREN-section progress line. Two halves are asserted together on purpose:
// the closed/total figure stays RAW (mirroring the deliberate raw
// EpicStatus.ClosedChildren convention that the conformance test pins), while
// the "eligible for close" suffix is a closeability verdict and applies the
// same non-completing-close rule as EpicStatus.EligibleForClose. Gating the
// whole line, or gating nothing, each fails exactly one half.
func TestPrintEpicChildProgressEligibility(t *testing.T) {
	tests := []struct {
		name         string
		children     []*types.IssueWithDependencyMetadata
		wantProgress string
		wantEligible bool
	}{
		{
			name:         "sole child closed as completed work",
			children:     []*types.IssueWithDependencyMetadata{epicChildRow("c-1", types.StatusClosed, "")},
			wantProgress: "1/1 complete (100%)",
			wantEligible: true,
		},
		{
			name:         "sole child closed as a duplicate",
			children:     []*types.IssueWithDependencyMetadata{epicChildRow("c-1", types.StatusClosed, "duplicate of c-9")},
			wantProgress: "1/1 complete (100%)",
			wantEligible: false,
		},
		{
			name: "one completing close alongside a wont-fix close",
			children: []*types.IssueWithDependencyMetadata{
				epicChildRow("c-1", types.StatusClosed, "fixed"),
				epicChildRow("c-2", types.StatusClosed, "wont-fix"),
			},
			wantProgress: "2/2 complete (100%)",
			wantEligible: false,
		},
		{
			name: "an open child keeps the epic ineligible",
			children: []*types.IssueWithDependencyMetadata{
				epicChildRow("c-1", types.StatusClosed, ""),
				epicChildRow("c-2", types.StatusOpen, ""),
			},
			wantProgress: "1/2 complete (50%)",
			wantEligible: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() error {
				printEpicChildProgress(tt.children)
				return nil
			})
			if !strings.Contains(out, tt.wantProgress) {
				t.Errorf("output %q does not contain raw progress %q", out, tt.wantProgress)
			}
			if got := strings.Contains(out, "eligible for close"); got != tt.wantEligible {
				t.Errorf("output %q: %q present = %v, want %v", out, "eligible for close", got, tt.wantEligible)
			}
		})
	}
}
