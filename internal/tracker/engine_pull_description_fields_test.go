package tracker

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// descriptionFieldsMockTracker is a mockTracker whose description carries
// acceptance criteria, design and notes; its settle keeps the pulled fields.
type descriptionFieldsMockTracker struct {
	*mockTracker
}

func (m *descriptionFieldsMockTracker) SettleDescriptionFields(_, _ *types.Issue) bool {
	return false
}

// TestEnginePullDescriptionFieldsNeedTheCapability pins AC-8: a pull from a
// tracker that does not implement DescriptionFieldsTracker writes the
// description and leaves acceptance criteria, design and notes at their local
// values, even when its mapper fills them. The control runs the same pull
// through a tracker that implements it, which writes all four fields.
func TestEnginePullDescriptionFieldsNeedTheCapability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability bool
		want       types.Issue
	}{
		{
			name: "without the capability",
			want: types.Issue{Description: "remote body", AcceptanceCriteria: "local AC", Design: "local design", Notes: "local notes"},
		},
		{
			name:       "control: with the capability",
			capability: true,
			want:       types.Issue{Description: "remote body", AcceptanceCriteria: "remote AC", Design: "remote design", Notes: "remote notes"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushHashFixture()
			f.local.Description = "local body"
			f.local.AcceptanceCriteria = "local AC"
			f.local.Design = "local design"
			f.local.Notes = "local notes"
			f.mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
				return &IssueConversion{Issue: &types.Issue{
					Title: ti.Title, Description: ti.Description, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
					AcceptanceCriteria: "remote AC", Design: "remote design", Notes: "remote notes",
				}}
			}}
			f.mock.issues[0].Description = "remote body"
			f.editRemote("remote title")
			if tc.capability {
				f.engine = NewEngine(&descriptionFieldsMockTracker{f.mock}, f.store, "sync")
				f.engine.PushHooks = titlePushHooks()
			}

			result := runSync(t, f.engine, SyncOptions{Pull: true})
			if result.PullStats.Updated != 1 || f.local.Title != "remote title" {
				t.Fatalf("PullStats = %+v, title %q; want the remote edit pulled", result.PullStats, f.local.Title)
			}
			got := types.Issue{Description: f.local.Description, AcceptanceCriteria: f.local.AcceptanceCriteria, Design: f.local.Design, Notes: f.local.Notes}
			if got.Description != tc.want.Description || got.AcceptanceCriteria != tc.want.AcceptanceCriteria || got.Design != tc.want.Design || got.Notes != tc.want.Notes {
				t.Fatalf("pulled fields: description %q, acceptance %q, design %q, notes %q; want %q, %q, %q, %q",
					got.Description, got.AcceptanceCriteria, got.Design, got.Notes,
					tc.want.Description, tc.want.AcceptanceCriteria, tc.want.Design, tc.want.Notes)
			}
		})
	}
}
