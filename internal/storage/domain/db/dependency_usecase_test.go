package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestDependencyUseCase() {
	s.Run("RemoveDependency", func() {
		s.Run("EmptyIDsReturnError", s.ducRemoveDependencyEmptyIDs)
		s.Run("DelegatesToRepoDelete", s.ducRemoveDependencyDelegates)
		s.Run("MissingEdgeIsNoop", s.ducRemoveDependencyMissingNoop)
	})
	s.Run("Reparent", func() {
		s.Run("EmptyChildReturnsError", s.ducReparentEmptyChild)
		s.Run("SelfParentReturnsError", s.ducReparentSelf)
		s.Run("AddsParentWhenNoneExists", s.ducReparentFromNone)
		s.Run("ReplacesExistingParent", s.ducReparentReplaces)
		s.Run("EmptyNewParentUnparents", s.ducReparentUnparent)
		s.Run("SameParentIsNoop", s.ducReparentSameParent)
	})
	s.Run("Wisp", func() {
		s.Run("RemoveWispDependencyRoutesToWispDeps", s.ducRemoveWispDependencyRoutes)
		s.Run("ReparentWispRoutesToWispDeps", s.ducReparentWispRoutes)
	})
}

func (s *testSuite) depUseCase() domain.DependencyUseCase {
	return domain.NewDependencyUseCase(NewDependencySQLRepository(s.Runner()))
}

func (s *testSuite) currentParent(childID string) string {
	res, err := s.depUseCase().ListByIssueIDs(s.Ctx(), []string{childID}, domain.DepListFilter{
		Types:     []types.DependencyType{types.DepParentChild},
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)
	for _, dep := range res.Outgoing[childID] {
		if dep.Type == types.DepParentChild {
			return dep.DependsOnID
		}
	}
	return ""
}

func (s *testSuite) ducRemoveDependencyEmptyIDs() {
	uc := s.depUseCase()
	s.Require().Error(uc.RemoveDependency(s.Ctx(), "", "bd-x", "tester"))
	s.Require().Error(uc.RemoveDependency(s.Ctx(), "bd-x", "", "tester"))
}

func (s *testSuite) ducRemoveDependencyDelegates() {
	s.seedIssueRow("bd-duc-rd-1")
	s.seedIssueRow("bd-duc-rd-2")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-rd-1", "bd-duc-rd-2", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	s.Require().NoError(s.depUseCase().RemoveDependency(s.Ctx(), "bd-duc-rd-1", "bd-duc-rd-2", "tester"))

	res, err := s.depUseCase().ListByIssueIDs(s.Ctx(), []string{"bd-duc-rd-1"},
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Empty(res.Outgoing["bd-duc-rd-1"])
}

func (s *testSuite) ducRemoveDependencyMissingNoop() {
	s.seedIssueRow("bd-duc-rd-miss-a")
	s.seedIssueRow("bd-duc-rd-miss-b")
	s.Require().NoError(s.depUseCase().RemoveDependency(s.Ctx(), "bd-duc-rd-miss-a", "bd-duc-rd-miss-b", "tester"))
}

func (s *testSuite) ducReparentEmptyChild() {
	s.Require().Error(s.depUseCase().Reparent(s.Ctx(), "", "bd-x", "tester"))
}

func (s *testSuite) ducReparentSelf() {
	s.Require().Error(s.depUseCase().Reparent(s.Ctx(), "bd-x", "bd-x", "tester"))
}

func (s *testSuite) ducReparentFromNone() {
	s.seedIssueRow("bd-duc-rp-none-c")
	s.seedIssueRow("bd-duc-rp-none-p")

	s.Require().NoError(s.depUseCase().Reparent(s.Ctx(), "bd-duc-rp-none-c", "bd-duc-rp-none-p", "tester"))

	s.Equal("bd-duc-rp-none-p", s.currentParent("bd-duc-rp-none-c"))
}

func (s *testSuite) ducReparentReplaces() {
	s.seedIssueRow("bd-duc-rp-c")
	s.seedIssueRow("bd-duc-rp-old")
	s.seedIssueRow("bd-duc-rp-new")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-rp-c", "bd-duc-rp-old", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	s.Require().NoError(s.depUseCase().Reparent(s.Ctx(), "bd-duc-rp-c", "bd-duc-rp-new", "tester"))

	s.Equal("bd-duc-rp-new", s.currentParent("bd-duc-rp-c"))
}

func (s *testSuite) ducReparentUnparent() {
	s.seedIssueRow("bd-duc-rp-up-c")
	s.seedIssueRow("bd-duc-rp-up-p")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-rp-up-c", "bd-duc-rp-up-p", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	s.Require().NoError(s.depUseCase().Reparent(s.Ctx(), "bd-duc-rp-up-c", "", "tester"))

	s.Equal("", s.currentParent("bd-duc-rp-up-c"))
}

func (s *testSuite) ducReparentSameParent() {
	s.seedIssueRow("bd-duc-rp-same-c")
	s.seedIssueRow("bd-duc-rp-same-p")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-rp-same-c", "bd-duc-rp-same-p", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	s.Require().NoError(s.depUseCase().Reparent(s.Ctx(), "bd-duc-rp-same-c", "bd-duc-rp-same-p", "tester"))

	s.Equal("bd-duc-rp-same-p", s.currentParent("bd-duc-rp-same-c"))
	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = 'parent-child'",
		"bd-duc-rp-same-c", "bd-duc-rp-same-p").Scan(&count))
	s.Equal(1, count)
}

func (s *testSuite) currentWispParent(childID string) string {
	res, err := s.depUseCase().ListByWispIDs(s.Ctx(), []string{childID}, domain.DepListFilter{
		Types:     []types.DependencyType{types.DepParentChild},
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)
	for _, dep := range res.Outgoing[childID] {
		if dep.Type == types.DepParentChild {
			return dep.DependsOnID
		}
	}
	return ""
}

func (s *testSuite) ducRemoveWispDependencyRoutes() {
	s.seedWispRow("bd-duc-rwd-a")
	s.seedWispRow("bd-duc-rwd-b")
	wispDepRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(wispDepRepo.Insert(s.Ctx(),
		newDep("bd-duc-rwd-a", "bd-duc-rwd-b", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))

	s.Require().NoError(s.depUseCase().RemoveWispDependency(s.Ctx(), "bd-duc-rwd-a", "bd-duc-rwd-b", "tester"))

	wispRes, err := s.depUseCase().ListByWispIDs(s.Ctx(), []string{"bd-duc-rwd-a"},
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Empty(wispRes.Outgoing["bd-duc-rwd-a"])
}

func (s *testSuite) ducReparentWispRoutes() {
	s.seedWispRow("bd-duc-rpw-c")
	s.seedWispRow("bd-duc-rpw-old")
	s.seedWispRow("bd-duc-rpw-new")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-rpw-c", "bd-duc-rpw-old", types.DepParentChild), "seeder", domain.DepInsertOpts{UseWispsTable: true}))

	s.Require().NoError(s.depUseCase().ReparentWisp(s.Ctx(), "bd-duc-rpw-c", "bd-duc-rpw-new", "tester"))

	s.Equal("bd-duc-rpw-new", s.currentWispParent("bd-duc-rpw-c"))

	var issuesCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", "bd-duc-rpw-c").Scan(&issuesCount))
	s.Equal(0, issuesCount, "wisp-routed Reparent must not touch the issues dep table")
}
