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
		{
			// Rule 3: the section is gone but its text sits in the
			// description — it moved into the body; the field clears and
			// nothing is kept for the push.
			name:     "absent section moved into the body clears the field",
			existing: newIssue("Body", "", "", "N1"),
			pulled:   newIssue("Body\n\nN1", "", "", ""),
			want:     newIssue("Body\n\nN1", "", "", ""),
		},
		{
			// Rule 3: the section and its text are gone from the body —
			// deleted remotely; the local text is kept and the push
			// restores it.
			name:     "absent section deleted remotely keeps local text",
			existing: newIssue("Body", "", "", "N1"),
			pulled:   newIssue("Body", "", "", ""),
			want:     newIssue("Body", "", "", "N1"),
			wantKept: true,
		},
		{
			// Rule 3: blank locally, absent remotely — the field stays
			// empty and nothing is kept. (The description differs so rule 1
			// does not short-circuit.)
			name:     "absent section blank locally stays blank",
			existing: newIssue("Body", "", "", ""),
			pulled:   newIssue("Body edited", "", "", ""),
			want:     newIssue("Body edited", "", "", ""),
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
