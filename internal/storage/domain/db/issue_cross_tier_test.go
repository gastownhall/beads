package db

import (
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestParentTierControlsInheritedLabels() {
	s.Run("durable parent to wisp child", func() {
		s.resetMintConfig("tier-durable-parent", "")
		uc := s.issueUseCase()
		parent, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
			Issue:  &types.Issue{Title: "durable parent", IssueType: types.TypeEpic, Priority: 2},
			Labels: []string{"durable-parent-label"},
		}, "tester")
		s.Require().NoError(err)

		child, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
			Issue:                   &types.Issue{Title: "wisp child", IssueType: types.TypeTask, Priority: 2, Ephemeral: true},
			ParentID:                parent.Issue.ID,
			InheritLabelsFromParent: true,
		}, "tester")
		s.Require().NoError(err)
		s.True(strings.HasPrefix(child.Issue.ID, parent.Issue.ID+"."), "child ID %q must use canonical parent child minting", child.Issue.ID)
		s.Equal([]string{"durable-parent-label"}, child.InheritedLabels)
		labels, err := s.labelUseCase().GetWispLabels(s.Ctx(), child.Issue.ID)
		s.Require().NoError(err)
		s.Equal([]string{"durable-parent-label"}, labels)
	})

	s.Run("wisp parent to durable child", func() {
		s.resetMintConfig("tier-wisp-parent", "")
		uc := s.issueUseCase()
		parent, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
			Issue:  &types.Issue{Title: "wisp parent", IssueType: types.TypeEpic, Priority: 2, Ephemeral: true},
			Labels: []string{"wisp-parent-label"},
		}, "tester")
		s.Require().NoError(err)

		child, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
			Issue:                   &types.Issue{Title: "durable child", IssueType: types.TypeTask, Priority: 2},
			ParentID:                parent.Issue.ID,
			InheritLabelsFromParent: true,
		}, "tester")
		s.Require().NoError(err)
		s.True(strings.HasPrefix(child.Issue.ID, parent.Issue.ID+"."), "child ID %q must use canonical parent child minting", child.Issue.ID)
		s.Equal([]string{"wisp-parent-label"}, child.InheritedLabels)
		labels, err := s.labelUseCase().GetLabels(s.Ctx(), child.Issue.ID)
		s.Require().NoError(err)
		s.Equal([]string{"wisp-parent-label"}, labels)
	})
}

func (s *testSuite) TestReverseCreateDependencyUsesActualSourceTier() {
	s.resetMintConfig("tier-reverse-dependency", "")
	uc := s.issueUseCase()

	wispSource, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "wisp source", IssueType: types.TypeTask, Priority: 2, Ephemeral: true},
	}, "tester")
	s.Require().NoError(err)
	durableTarget, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "durable target", IssueType: types.TypeTask, Priority: 2},
		Dependencies: []domain.DependencySpec{{
			Type: types.DepRelated, TargetID: wispSource.Issue.ID, SwapDirection: true,
			Metadata: `{"from":"wisp"}`, ThreadID: "wisp-thread",
		}},
	}, "tester")
	s.Require().NoError(err)
	wispRecords, err := s.depUseCase().GetWispDependencyRecords(s.Ctx(), []string{wispSource.Issue.ID})
	s.Require().NoError(err)
	s.Require().Len(wispRecords[wispSource.Issue.ID], 1)
	s.Equal(durableTarget.Issue.ID, wispRecords[wispSource.Issue.ID][0].DependsOnID)
	s.Equal(`{"from":"wisp"}`, wispRecords[wispSource.Issue.ID][0].Metadata)
	s.Equal("wisp-thread", wispRecords[wispSource.Issue.ID][0].ThreadID)

	durableSource, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "durable source", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	wispTarget, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "wisp target", IssueType: types.TypeTask, Priority: 2, Ephemeral: true},
		Dependencies: []domain.DependencySpec{{
			Type: types.DepRelated, TargetID: durableSource.Issue.ID, SwapDirection: true,
			Metadata: `{"from":"durable"}`, ThreadID: "durable-thread",
		}},
	}, "tester")
	s.Require().NoError(err)
	durableRecords, err := s.depUseCase().GetIssueDependencyRecords(s.Ctx(), []string{durableSource.Issue.ID})
	s.Require().NoError(err)
	s.Require().Len(durableRecords[durableSource.Issue.ID], 1)
	s.Equal(wispTarget.Issue.ID, durableRecords[durableSource.Issue.ID][0].DependsOnID)
	s.Equal(`{"from":"durable"}`, durableRecords[durableSource.Issue.ID][0].Metadata)
	s.Equal("durable-thread", durableRecords[durableSource.Issue.ID][0].ThreadID)
}
