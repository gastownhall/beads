package sqlite

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeduplicateIncomingIssues tests that duplicate issues within the incoming batch are consolidated
func TestDeduplicateIncomingIssues(t *testing.T) {
	tests := []struct {
		name     string
		incoming []*types.Issue
		want     int // expected number of issues after deduplication
		wantIDs  []string
	}{
		{
			name: "no duplicates",
			incoming: []*types.Issue{
				{ID: "bd-1", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "bd-2", Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			want:    2,
			wantIDs: []string{"bd-1", "bd-2"},
		},
		{
			name: "exact content duplicates - keep smallest ID",
			incoming: []*types.Issue{
				{ID: "bd-226", Title: "Epic: Fix status/closed_at inconsistency", Description: "Implement hybrid solution", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
				{ID: "bd-367", Title: "Epic: Fix status/closed_at inconsistency", Description: "Implement hybrid solution", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
				{ID: "bd-396", Title: "Epic: Fix status/closed_at inconsistency", Description: "Implement hybrid solution", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
			},
			want:    1,
			wantIDs: []string{"bd-226"}, // Keep smallest ID
		},
		{
			name: "partial duplicates - keep unique ones",
			incoming: []*types.Issue{
				{ID: "bd-1", Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "bd-2", Title: "Task A", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, // Dup of bd-1
				{ID: "bd-3", Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, // Unique
				{ID: "bd-4", Title: "Task B", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, // Dup of bd-3
			},
			want:    2,
			wantIDs: []string{"bd-1", "bd-3"},
		},
		{
			name: "duplicates with different timestamps - timestamps ignored",
			incoming: []*types.Issue{
				{ID: "bd-100", Title: "Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "bd-101", Title: "Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			want:    1,
			wantIDs: []string{"bd-100"}, // Keep smallest ID
		},
		{
			name: "different priority - not duplicates",
			incoming: []*types.Issue{
				{ID: "bd-1", Title: "Task", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "bd-2", Title: "Task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			},
			want:    2,
			wantIDs: []string{"bd-1", "bd-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deduplicateIncomingIssues(tt.incoming)

			if len(result) != tt.want {
				t.Errorf("deduplicateIncomingIssues() returned %d issues, want %d", len(result), tt.want)
			}

			// Check that the expected IDs are present
			resultIDs := make(map[string]bool)
			for _, issue := range result {
				resultIDs[issue.ID] = true
			}

			for _, wantID := range tt.wantIDs {
				if !resultIDs[wantID] {
					t.Errorf("expected ID %s not found in result", wantID)
				}
			}
		})
	}
}
