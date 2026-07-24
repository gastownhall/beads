package db

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_CloseIssue() {
	s.Run("ReturnsUpdatedIssue", s.uccCloseReturnsIssue)
	s.Run("AlreadyClosedReportsNotClosed", s.uccCloseAlreadyClosed)
	s.Run("EmptyIDErrors", s.uccCloseEmptyID)
	s.Run("EmptyActorErrors", s.uccCloseEmptyActor)
	s.Run("WispVariantRoutesToWispsTable", s.uccCloseWispRoutes)
}

func (s *testSuite) TestIssueUseCase_ClaimIfOpen() {
	s.Run("OpenIssueClaimedSuccessfully", s.uccClaimIfOpenOpen)
	s.Run("AlreadyClosedReturnsNotClaimable", s.uccClaimIfOpenClosed)
	s.Run("AlreadyClaimedByOtherReturnsAlreadyClaimed", s.uccClaimIfOpenContested)
	s.Run("AlreadyClaimedBySameActorIsIdempotent", s.uccClaimIfOpenIdempotent)
}

func (s *testSuite) TestIssueUseCase_CountOpenChildren() {
	s.Run("CountsNonClosedChildrenOnly", s.uccCountOpenChildrenMixed)
	s.Run("ZeroWhenNoChildren", s.uccCountOpenChildrenNone)
	s.Run("IgnoresNonParentChildEdges", s.uccCountOpenChildrenIgnoresOtherTypes)
	s.Run("EmptyIDErrors", s.uccCountOpenChildrenEmptyID)
}

func (s *testSuite) TestIssueUseCase_GetNewlyUnblockedByClose() {
	s.Run("ReturnsDependent", s.uccUnblockedReturnsDependent)
	s.Run("EmptyIDErrors", s.uccUnblockedEmptyID)
}

func (s *testSuite) TestDependencyUseCase_IsBlocked() {
	s.Run("TrueOnBlocksEdge", s.uccIsBlockedTrue)
	s.Run("FalseWhenNoBlockers", s.uccIsBlockedFalse)
	s.Run("EmptyIDErrors", s.uccIsBlockedEmptyID)
}

func (s *testSuite) uccCloseReturnsIssue() {
	s.seedIssueRow("bd-ucc-cl-1")
	uc := s.issueUseCase()

	res, err := uc.CloseIssue(s.Ctx(), "bd-ucc-cl-1",
		domain.CloseIssueParams{Reason: "done", Session: "sess-1"}, "tester")
	s.Require().NoError(err)
	s.True(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal("bd-ucc-cl-1", res.Issue.ID)
	s.Equal(types.StatusClosed, res.Issue.Status)
}

func (s *testSuite) uccCloseAlreadyClosed() {
	s.seedIssueRow("bd-ucc-cl-2")
	uc := s.issueUseCase()

	_, err := uc.CloseIssue(s.Ctx(), "bd-ucc-cl-2",
		domain.CloseIssueParams{Reason: "first"}, "tester")
	s.Require().NoError(err)

	res, err := uc.CloseIssue(s.Ctx(), "bd-ucc-cl-2",
		domain.CloseIssueParams{Reason: "second"}, "tester")
	s.Require().NoError(err)
	s.False(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusClosed, res.Issue.Status)
}

func (s *testSuite) uccCloseEmptyID() {
	_, err := s.issueUseCase().CloseIssue(s.Ctx(), "",
		domain.CloseIssueParams{Reason: "x"}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) uccCloseEmptyActor() {
	s.seedIssueRow("bd-ucc-cl-noactor")
	_, err := s.issueUseCase().CloseIssue(s.Ctx(), "bd-ucc-cl-noactor",
		domain.CloseIssueParams{Reason: "x"}, "")
	s.Require().Error(err)
}

func (s *testSuite) uccCloseWispRoutes() {
	s.seedWispRow("bd-ucc-cl-wisp")
	uc := s.issueUseCase()

	res, err := uc.CloseWisp(s.Ctx(), "bd-ucc-cl-wisp",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)
	s.True(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusClosed, res.Issue.Status)

	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM wisps WHERE id = ?", "bd-ucc-cl-wisp").Scan(&status))
	s.Equal(string(types.StatusClosed), status)
}

func (s *testSuite) uccClaimIfOpenOpen() {
	s.seedIssueRow("bd-ucc-cif-1")
	res, err := s.issueUseCase().ClaimIssueIfOpen(s.Ctx(), "bd-ucc-cif-1", "tester")
	s.Require().NoError(err)
	s.False(res.AlreadyClaimed)

	var status, assignee string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status, assignee FROM issues WHERE id = ?", "bd-ucc-cif-1").Scan(&status, &assignee))
	s.Equal(string(types.StatusInProgress), status)
	s.Equal("tester", assignee)
}

func (s *testSuite) uccClaimIfOpenClosed() {
	s.seedIssueRow("bd-ucc-cif-2")
	uc := s.issueUseCase()
	_, err := uc.CloseIssue(s.Ctx(), "bd-ucc-cif-2",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)

	_, err = uc.ClaimIssueIfOpen(s.Ctx(), "bd-ucc-cif-2", "tester")
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrNotClaimable))
}

func (s *testSuite) uccClaimIfOpenContested() {
	s.seedIssueRow("bd-ucc-cif-3")
	uc := s.issueUseCase()
	_, err := uc.ClaimIssueIfOpen(s.Ctx(), "bd-ucc-cif-3", "alice")
	s.Require().NoError(err)

	_, err = uc.ClaimIssueIfOpen(s.Ctx(), "bd-ucc-cif-3", "bob")
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrAlreadyClaimed))
}

func (s *testSuite) uccClaimIfOpenIdempotent() {
	s.seedIssueRow("bd-ucc-cif-4")
	uc := s.issueUseCase()
	_, err := uc.ClaimIssueIfOpen(s.Ctx(), "bd-ucc-cif-4", "alice")
	s.Require().NoError(err)

	res, err := uc.ClaimIssueIfOpen(s.Ctx(), "bd-ucc-cif-4", "alice")
	s.Require().NoError(err)
	s.True(res.AlreadyClaimed)
	s.Equal("alice", res.PriorAssignee)
}

func (s *testSuite) uccCountOpenChildrenMixed() {
	s.seedIssueRow("bd-ucc-coc-parent")
	s.seedIssueRow("bd-ucc-coc-c1")
	s.seedIssueRow("bd-ucc-coc-c2")
	s.seedIssueRow("bd-ucc-coc-c3")
	dep := s.depRepo()
	for _, child := range []string{"bd-ucc-coc-c1", "bd-ucc-coc-c2", "bd-ucc-coc-c3"} {
		s.Require().NoError(dep.Insert(s.Ctx(),
			newDep(child, "bd-ucc-coc-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	}
	_, err := s.issueUseCase().CloseIssue(s.Ctx(), "bd-ucc-coc-c2",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)

	got, err := s.issueUseCase().CountOpenChildren(s.Ctx(), "bd-ucc-coc-parent")
	s.Require().NoError(err)
	s.Equal(2, got, "two children remain open after one is closed")
}

func (s *testSuite) uccCountOpenChildrenNone() {
	s.seedIssueRow("bd-ucc-coc-lonely")
	got, err := s.issueUseCase().CountOpenChildren(s.Ctx(), "bd-ucc-coc-lonely")
	s.Require().NoError(err)
	s.Equal(0, got)
}

func (s *testSuite) uccCountOpenChildrenIgnoresOtherTypes() {
	s.seedIssueRow("bd-ucc-coc-mixed-p")
	s.seedIssueRow("bd-ucc-coc-mixed-c")
	s.seedIssueRow("bd-ucc-coc-mixed-r")
	dep := s.depRepo()
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-ucc-coc-mixed-c", "bd-ucc-coc-mixed-p", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-ucc-coc-mixed-r", "bd-ucc-coc-mixed-p", types.DepRelated), "tester", domain.DepInsertOpts{}))

	got, err := s.issueUseCase().CountOpenChildren(s.Ctx(), "bd-ucc-coc-mixed-p")
	s.Require().NoError(err)
	s.Equal(1, got, "related edges must not be counted as children")
}

func (s *testSuite) uccCountOpenChildrenEmptyID() {
	_, err := s.issueUseCase().CountOpenChildren(s.Ctx(), "")
	s.Require().Error(err)
}

func (s *testSuite) uccUnblockedReturnsDependent() {
	s.seedIssueRow("bd-ucc-un-dep")
	s.seedIssueRow("bd-ucc-un-blocker")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-un-dep", "bd-ucc-un-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	uc := s.issueUseCase()
	_, err := uc.CloseIssue(s.Ctx(), "bd-ucc-un-blocker",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)

	unblocked, err := uc.GetNewlyUnblockedByClose(s.Ctx(), "bd-ucc-un-blocker")
	s.Require().NoError(err)
	s.Require().Len(unblocked, 1)
	s.Equal("bd-ucc-un-dep", unblocked[0].ID)
}

func (s *testSuite) uccUnblockedEmptyID() {
	_, err := s.issueUseCase().GetNewlyUnblockedByClose(s.Ctx(), "")
	s.Require().Error(err)
}

func (s *testSuite) uccIsBlockedTrue() {
	s.seedIssueRow("bd-ucc-isb-src")
	s.seedIssueRow("bd-ucc-isb-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-isb-src", "bd-ucc-isb-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	blocked, blockers, err := s.depUseCase().IsBlocked(s.Ctx(), "bd-ucc-isb-src")
	s.Require().NoError(err)
	s.True(blocked)
	s.Contains(blockers, "bd-ucc-isb-tgt")
}

func (s *testSuite) uccIsBlockedFalse() {
	s.seedIssueRow("bd-ucc-isb-free")
	blocked, blockers, err := s.depUseCase().IsBlocked(s.Ctx(), "bd-ucc-isb-free")
	s.Require().NoError(err)
	s.False(blocked)
	s.Empty(blockers)
}

func (s *testSuite) uccIsBlockedEmptyID() {
	_, _, err := s.depUseCase().IsBlocked(s.Ctx(), "")
	s.Require().Error(err)
}
