package db

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreatePreservesSuppliedUpdatedAtAcrossLabels pins the create-time side
// of GH#5442 on the uow stack. A live label edit now advances updated_at, but
// the labels a create or import persists with the issue, supplied or
// inherited from the parent, are part of that same snapshot: they must leave
// the supplied timestamp alone, or every imported labeled issue would look
// edited at import time and stale-rejection would compare against the wrong
// clock.
func (s *testSuite) TestCreatePreservesSuppliedUpdatedAtAcrossLabels() {
	supplied := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	uc := s.issueUseCase()

	for _, plane := range []struct {
		name string
		wisp bool
	}{{"durable", false}, {"wisp", true}} {
		s.Run(plane.name, func() {
			create, get := uc.CreateIssue, uc.GetIssue
			if plane.wisp {
				create, get = uc.CreateWisp, uc.GetWisp
			}
			parentID := "bd-cpl-" + plane.name
			parent, err := create(s.Ctx(), domain.CreateIssueParams{
				Issue: &types.Issue{
					ID: parentID, Title: "parent", IssueType: types.TypeTask, Priority: 2,
					CreatedAt: supplied, UpdatedAt: supplied,
				},
				Labels: []string{"own", "shared"},
			}, "tester")
			s.Require().NoError(err)
			s.assertUpdatedAt(get, parent.Issue.ID, supplied, "parent with supplied labels")

			child, err := create(s.Ctx(), domain.CreateIssueParams{
				Issue: &types.Issue{
					Title: "child", IssueType: types.TypeTask, Priority: 2,
					CreatedAt: supplied, UpdatedAt: supplied,
				},
				ParentID:                parentID,
				Labels:                  []string{"child-own"},
				InheritLabelsFromParent: true,
			}, "tester")
			s.Require().NoError(err)
			s.Require().ElementsMatch([]string{"own", "shared"}, child.InheritedLabels)
			s.assertUpdatedAt(get, child.Issue.ID, supplied, "child with supplied and inherited labels")
		})
	}
}

// assertUpdatedAt reads id's updated_at through get, the plane's reader.
func (s *testSuite) assertUpdatedAt(get func(context.Context, string) (*types.Issue, error), id string, want time.Time, what string) {
	s.T().Helper()
	got, err := get(s.Ctx(), id)
	s.Require().NoError(err)
	s.Truef(got.UpdatedAt.UTC().Equal(want), "%s %s: updated_at = %v, want supplied %v preserved",
		what, id, got.UpdatedAt.UTC(), want)
}
