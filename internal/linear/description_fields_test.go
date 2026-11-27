package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSettleDescriptionFields pins the settle rules on the tracker itself.
func TestSettleDescriptionFields(t *testing.T) {
	tr := &Tracker{}
	newIssue := func(description, acceptance, design, notes string) *types.Issue {
		return &types.Issue{
			Description:        description,
			AcceptanceCriteria: acceptance,
			Design:             design,
			Notes:              notes,
		}
	}
	cases := []struct {
		name     string
		existing *types.Issue
		pulled   *types.Issue
		want     *types.Issue
		wantKept bool
	}{
		{
			// Rule 1: the body holds what the local fields push, so the
			// local fields stand (and nothing is kept for the push).
			name:     "unchanged body keeps the local shape",
			existing: newIssue("Body", "", "", "N1"),
			pulled:   newIssue("Body", "", "", "N1"),
			want:     newIssue("Body", "", "", "N1"),
		},
		{
			// Rule 1: a description that itself ends in a section-shaped run
			// parses apart, but the body still holds what the local fields
			// push, so the parse must not reshape the issue.
			name:     "template heading description is not reshaped",
			existing: newIssue("Intro\n\n## Notes\ntemplate text", "", "", ""),
			pulled:   newIssue("Intro", "", "", "template text"),
			want:     newIssue("Intro\n\n## Notes\ntemplate text", "", "", ""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kept := tr.SettleDescriptionFields(tc.existing, tc.pulled)
			if kept != tc.wantKept ||
				tc.pulled.Description != tc.want.Description ||
				tc.pulled.AcceptanceCriteria != tc.want.AcceptanceCriteria ||
				tc.pulled.Design != tc.want.Design ||
				tc.pulled.Notes != tc.want.Notes {
				t.Fatalf("SettleDescriptionFields\n got  %+v kept=%v\n want %+v kept=%v", *tc.pulled, kept, *tc.want, tc.wantKept)
			}
		})
	}
}
